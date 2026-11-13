package warehouse

import (
	"reflect"
	"strings"
	"testing"
)

func TestCustomFields_ReadsKeyAndFormula(t *testing.T) {
	source := map[string]any{"customFieldMapping": []any{
		map[string]any{"key": "price_usd_cents", "formula": "price_usd/100"},
		map[string]any{"fieldName": "event_key", "column": "event_key"},
		map[string]any{"key": "no_formula"},
	}}
	want := []CustomField{{Key: "price_usd_cents", Formula: "price_usd/100"}, {Key: "no_formula"}}
	if got := CustomFields(source); !reflect.DeepEqual(got, want) {
		t.Errorf("CustomFields = %+v, want %+v", got, want)
	}
	if got := CustomFields(map[string]any{}); got != nil {
		t.Errorf("no mapping: %+v, want nil", got)
	}
}

func TestWithCustomFields_AddsEachFieldAsAColumn(t *testing.T) {
	for _, tc := range []struct {
		name   string
		sql    string
		fields []CustomField
		want   string
	}{
		{
			"one field, trailing terminator dropped",
			"SELECT ts, user_id, price_usd FROM analytics.orders;",
			[]CustomField{{"price_usd_cents", "price_usd/100"}},
			"SELECT *, price_usd/100 AS price_usd_cents FROM (\nSELECT ts, user_id, price_usd FROM analytics.orders\n) AS statsig_src",
		},
		{
			"fields keep their order",
			"SELECT * FROM analytics.orders",
			[]CustomField{{"cents", " price_usd * 100 "}, {"is_big", "CASE WHEN price_usd > 100 THEN 1 ELSE 0 END"}},
			"SELECT *, price_usd * 100 AS cents, CASE WHEN price_usd > 100 THEN 1 ELSE 0 END AS is_big FROM (\nSELECT * FROM analytics.orders\n) AS statsig_src",
		},
		{
			"a line comment cannot swallow the alias",
			"SELECT * FROM analytics.orders",
			[]CustomField{{"cents", "price_usd * 100 -- to cents"}},
			"SELECT *, (\nprice_usd * 100 -- to cents\n) AS cents FROM (\nSELECT * FROM analytics.orders\n) AS statsig_src",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := WithCustomFields(tc.sql, tc.fields, "snowflake")
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

func TestWithCustomFields_KeepsTheConstantKeyWrapperReadable(t *testing.T) {
	inner, err := WithCustomFields("SELECT * FROM analytics.orders", []CustomField{{"cents", "price_usd * 100"}}, "snowflake")
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := WrapWithConstantEventKey(inner, "orders", "LD_EVENT_KEY", "snowflake")
	if err != nil {
		t.Fatal(err)
	}
	w, state := ClassifyConstantKey(wrapped, "LD_EVENT_KEY", "orders", "snowflake")
	if state != ConstantKeyWrapped || w.Inner != inner {
		t.Errorf("state = %v, inner = %q, want ConstantKeyWrapped with %q", state, w.Inner, inner)
	}
}

func TestWithCustomFields_RefusesWhatItCannotNest(t *testing.T) {
	for _, tc := range []struct {
		name   string
		field  CustomField
		reason string
	}{
		{"no name", CustomField{"", "price_usd/100"}, "has no name"},
		{"name is not an identifier", CustomField{"price (cents)", "price_usd/100"}, "letters, digits, and underscores"},
		{"no formula", CustomField{"cents", "  "}, "has no formula"},
		{"two statements", CustomField{"cents", "1; DROP TABLE orders"}, "single expression"},
		{"Statsig macro", CustomField{"recent", "ts > {statsig_start_date}"}, "Statsig macros"},
		{"unterminated string", CustomField{"label", "'cents"}, "could not read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := WithCustomFields("SELECT * FROM t", []CustomField{tc.field}, "snowflake")
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("err = %v, want one containing %q", err, tc.reason)
			}
		})
	}
}
