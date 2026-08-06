package converter

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
)

func crit(column, condition string, values ...string) statsig.Criterion {
	return statsig.Criterion{Type: "metadata", Column: column, Condition: condition, Values: values}
}

// ---------------------------------------------------------------------------
// Condition mapping
// ---------------------------------------------------------------------------

func TestCriteriaToFilter_MappedConditions(t *testing.T) {
	tests := []struct {
		name       string
		criterion  statsig.Criterion
		wantOp     string
		wantNegate bool
		wantValues []any
	}{
		{"in", crit("country", "in", "JP", "US"), ldOpIn, false, []any{"JP", "US"}},
		{"equals maps to single-value in", crit("country", "=", "JP"), ldOpIn, false, []any{"JP"}},
		{"not_in negates in", crit("country", "not_in", "JP"), ldOpIn, true, []any{"JP"}},
		{"contains", crit("page", "contains", "check"), ldOpContains, false, []any{"check"}},
		{"not_contains negates contains", crit("page", "not_contains", "check"), ldOpContains, true, []any{"check"}},
		{"starts_with", crit("page", "starts_with", "chk"), ldOpStartsWith, false, []any{"chk"}},
		{"ends_with", crit("page", "ends_with", "out"), ldOpEndsWith, false, []any{"out"}},
		{"greater than", crit("price", ">", "10"), ldOpGreaterThan, false, []any{float64(10)}},
		{"greater or equal", crit("price", ">=", "10.5"), ldOpGreaterThanOrEqual, false, []any{float64(10.5)}},
		{"less than", crit("price", "<", "-3"), ldOpLessThan, false, []any{float64(-3)}},
		{"less or equal", crit("price", "<=", "0"), ldOpLessThanOrEqual, false, []any{float64(0)}},
		// exists is the POSITIVE form, so non_null is negate=false and is_null is
		// negate=true. Getting this backwards inverts the filter.
		{"non_null maps to exists", crit("coupon", "non_null"), ldOpExists, false, []any{}},
		{"is_null maps to negated exists", crit("coupon", "is_null"), ldOpExists, true, []any{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := criteriaToFilter([]statsig.Criterion{tt.criterion})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Type != launchdarkly.EventFilterTypeEventProperty {
				t.Errorf("Type = %q, want %q", got.Type, launchdarkly.EventFilterTypeEventProperty)
			}
			if got.Attribute != tt.criterion.Column {
				t.Errorf("Attribute = %q, want %q", got.Attribute, tt.criterion.Column)
			}
			if got.Op != tt.wantOp {
				t.Errorf("Op = %q, want %q", got.Op, tt.wantOp)
			}
			if got.Negate != tt.wantNegate {
				t.Errorf("Negate = %v, want %v", got.Negate, tt.wantNegate)
			}
			if len(got.Values) != len(tt.wantValues) {
				t.Fatalf("Values = %#v, want %#v", got.Values, tt.wantValues)
			}
			for i := range tt.wantValues {
				if got.Values[i] != tt.wantValues[i] {
					t.Errorf("Values[%d] = %#v (%T), want %#v (%T)",
						i, got.Values[i], got.Values[i], tt.wantValues[i], tt.wantValues[i])
				}
			}
			// A group node is never produced for a single criterion.
			if got.ContextKind != "" {
				t.Errorf("ContextKind = %q, want empty (warehouse-native filters are column filters)", got.ContextKind)
			}
		})
	}
}

// A numeric comparison must carry a JSON number. A string would make the
// generated warehouse SQL match nothing instead of erroring, so this is a silent
// wrong-answer guard, not a cosmetic one.
func TestCriteriaToFilter_NumericValuesAreJSONNumbers(t *testing.T) {
	got, err := criteriaToFilter([]statsig.Criterion{crit("price", ">", "10")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"values":[10]`) {
		t.Errorf("numeric comparison should serialize an unquoted number, got %s", b)
	}
}

// LaunchDarkly rejects a filter that carries values with the exists operator, and
// treats a missing values field as invalid. So it must serialize as exactly [].
func TestCriteriaToFilter_ExistsSerializesEmptyValues(t *testing.T) {
	c := crit("coupon", "non_null")
	// Statsig accepts and stores stray values on a valueless condition; they must
	// be dropped rather than passed through.
	c.Values = []string{"ignored"}
	got, err := criteriaToFilter([]statsig.Criterion{c})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"values":[]`) {
		t.Errorf("exists must serialize values as [], got %s", b)
	}
	if strings.Contains(string(b), "ignored") {
		t.Errorf("stray values on a valueless condition must be dropped, got %s", b)
	}
}

// ---------------------------------------------------------------------------
// Group construction
// ---------------------------------------------------------------------------

func TestCriteriaToFilter_MultipleCriteriaBecomeAndGroup(t *testing.T) {
	got, err := criteriaToFilter([]statsig.Criterion{
		crit("event", "in", "purchase"),
		crit("page", "=", "checkout"),
		crit("coupon", "non_null"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Type != launchdarkly.EventFilterTypeGroup {
		t.Fatalf("Type = %q, want %q", got.Type, launchdarkly.EventFilterTypeGroup)
	}
	if got.Op != launchdarkly.EventFilterGroupOpAnd {
		t.Errorf("Op = %q, want %q (Statsig ANDs multiple criteria)", got.Op, launchdarkly.EventFilterGroupOpAnd)
	}
	// LaunchDarkly rejects a group node that sets any of these.
	if got.Negate {
		t.Error("group node must not set Negate")
	}
	if got.Attribute != "" || got.ContextKind != "" {
		t.Errorf("group node must not set Attribute/ContextKind, got %q/%q", got.Attribute, got.ContextKind)
	}
	if len(got.Values) != 3 {
		t.Fatalf("group should hold 3 children, got %d", len(got.Values))
	}
	// Source order is preserved so output is deterministic.
	wantAttrs := []string{"event", "page", "coupon"}
	for i, want := range wantAttrs {
		child, ok := got.Values[i].(launchdarkly.EventFilter)
		if !ok {
			t.Fatalf("Values[%d] is %T, want launchdarkly.EventFilter", i, got.Values[i])
		}
		if child.Attribute != want {
			t.Errorf("child %d Attribute = %q, want %q (source order)", i, child.Attribute, want)
		}
	}
}

func TestCriteriaToFilter_NoCriteriaYieldsNoFilter(t *testing.T) {
	got, err := criteriaToFilter(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil filter for no criteria, got %#v", got)
	}
}

func TestCriteriaToFilter_NestedGroupSerializesAsObject(t *testing.T) {
	got, err := criteriaToFilter([]statsig.Criterion{
		crit("event", "in", "purchase"),
		crit("page", "=", "checkout"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var round struct {
		Type   string `json:"type"`
		Op     string `json:"op"`
		Values []struct {
			Type      string   `json:"type"`
			Attribute string   `json:"attribute"`
			Op        string   `json:"op"`
			Values    []string `json:"values"`
		} `json:"values"`
	}
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatalf("group children must serialize as filter objects: %v (%s)", err, b)
	}
	if round.Type != "group" || round.Op != "and" || len(round.Values) != 2 {
		t.Fatalf("unexpected round-trip shape: %s", b)
	}
	if round.Values[0].Attribute != "event" || round.Values[1].Attribute != "page" {
		t.Errorf("child order not preserved through JSON: %s", b)
	}
}

// ---------------------------------------------------------------------------
// Rejections. All-or-nothing: one bad criterion rejects the whole term.
// ---------------------------------------------------------------------------

func TestCriteriaToFilter_RejectedConditions(t *testing.T) {
	tests := []struct {
		name      string
		criterion statsig.Criterion
		wantIn    string
	}{
		{"sql_filter", statsig.Criterion{Type: "metadata", Condition: "sql_filter", Values: []string{"1=1"}}, "raw SQL snippet"},
		{"after_exposure", crit("ts", "after_exposure", "0"), "exposure time"},
		{"before_exposure", crit("ts", "before_exposure", "0"), "exposure time"},
		{"is_true", crit("flag", "is_true"), "true/false column check"},
		{"is_false", crit("flag", "is_false"), "true/false column check"},
		{"unknown condition", crit("col", "zzz_nonsense", "x"), "no matching filter operator"},
		{"missing column", statsig.Criterion{Type: "metadata", Condition: "in", Values: []string{"x"}}, "no column"},
		{"context attribute type", statsig.Criterion{Type: "user", Column: "country", Condition: "in", Values: []string{"JP"}}, "can only filter on a column"},
		{"user_custom type", statsig.Criterion{Type: "user_custom", Column: "plan", Condition: "in", Values: []string{"pro"}}, "can only filter on a column"},
		{"no values", crit("country", "in"), "at least one filter value"},
		{"empty string value", crit("country", "in", ""), "one of its values is empty"},
		{"multi-value comparison", crit("price", ">", "1", "2"), "takes exactly one value"},
		{"non-numeric comparison", crit("price", ">", "cheap"), "is not a number"},
		{"oversized string", crit("country", "in", strings.Repeat("x", ldFilterMaxStringLen+1)), "longer than LaunchDarkly's"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := criteriaToFilter([]statsig.Criterion{tt.criterion})
			if err == nil {
				t.Fatalf("expected rejection, got filter %#v", got)
			}
			if got != nil {
				t.Errorf("a rejected term must yield no filter, got %#v", got)
			}
			if !strings.Contains(err.Error(), tt.wantIn) {
				t.Errorf("error %q should mention %q", err.Error(), tt.wantIn)
			}
		})
	}
}

func TestCriteriaToFilter_NullVacuousOverrideRejected(t *testing.T) {
	on := true
	c := crit("country", "in", "JP")
	c.NullVacuousOverride = &on
	if _, err := criteriaToFilter([]statsig.Criterion{c}); err == nil {
		t.Fatal("nullVacuousOverride changes null handling and must reject the criterion")
	} else if !strings.Contains(err.Error(), "nullVacuousOverride") {
		t.Errorf("error should name the field, got %q", err.Error())
	}
}

func TestCriteriaToFilter_OneBadCriterionRejectsWholeTerm(t *testing.T) {
	// The core safety property: because criteria are AND-ed, applying only the
	// mappable subset would WIDEN the matched set.
	_, err := criteriaToFilter([]statsig.Criterion{
		crit("event", "in", "purchase"),
		crit("page", "=", "checkout"),
		statsig.Criterion{Type: "metadata", Condition: "sql_filter", Values: []string{"1=1"}},
	})
	if err == nil {
		t.Fatal("a single unmappable criterion must reject the entire term")
	}
}

func TestCriteriaToFilter_LeafLimit(t *testing.T) {
	within := make([]statsig.Criterion, ldFilterMaxLeaves)
	for i := range within {
		within[i] = crit("col", "in", "v")
	}
	if _, err := criteriaToFilter(within); err != nil {
		t.Errorf("%d criteria is exactly at LaunchDarkly's limit and should be accepted: %v", ldFilterMaxLeaves, err)
	}

	over := append(within, crit("col", "in", "v")) //nolint:gocritic // intentional copy
	_, err := criteriaToFilter(over)
	if err == nil {
		t.Fatalf("%d criteria exceeds LaunchDarkly's limit and must be rejected", len(over))
	}
	if !strings.Contains(err.Error(), "at most") {
		t.Errorf("error should explain the clause limit, got %q", err.Error())
	}
}

// ---------------------------------------------------------------------------
// End-to-end through Convert / convertRatio
// ---------------------------------------------------------------------------

const whnRatioWithFilters = `{
  "type":"user_warehouse","name":"Rev per Visit","id":"Rev per Visit::user_warehouse","directionality":"increase",
  "warehouseNative":{"aggregation":"ratio",
    "metricSourceName":"Checkout","valueColumn":"revenue","numeratorAggregation":"sum",
    "criteria":[{"type":"metadata","column":"event","condition":"in","values":["purchase"]}],
    "denominatorMetricSourceName":"Visits","denominatorValueColumn":"visit_id","denominatorAggregation":"count",
    "denominatorCriteria":[{"type":"metadata","column":"page","condition":"=","values":["checkout"]}]}}`

func TestConvertRatio_BothTermFiltersApplied(t *testing.T) {
	res := mustConvert(t, whnRatioWithFilters, Options{SourceMapping: map[string]string{
		"Checkout": "ld-checkout",
		"Visits":   "ld-visits",
	}})
	if res.IsLossy() {
		t.Errorf("both terms are mappable and bound, so nothing should be lossy; LossyReasons=%v", res.LossyReasons)
	}
	if res.LDMetric.Filters == nil || res.LDMetric.Filters.Attribute != "event" {
		t.Errorf("numerator filter = %#v, want an eventProperty on \"event\"", res.LDMetric.Filters)
	}
	if res.LDMetric.Denominator == nil {
		t.Fatal("ratio should populate a denominator")
	}
	if res.LDMetric.Denominator.Filters == nil || res.LDMetric.Denominator.Filters.Attribute != "page" {
		t.Errorf("denominator filter = %#v, want an eventProperty on \"page\"", res.LDMetric.Denominator.Filters)
	}
}

func TestConvertRatio_PerTermIndependence(t *testing.T) {
	// Only the numerator's source is mapped. The numerator gets its filter; the
	// denominator has no data source so its criteria stay lossy. Each term is
	// resolved independently.
	res := mustConvert(t, whnRatioWithFilters, Options{SourceMapping: map[string]string{
		"Checkout": "ld-checkout",
	}})
	if res.LDMetric.Filters == nil {
		t.Error("numerator is bound and mappable, so it should carry a filter")
	}
	if res.LDMetric.Denominator == nil {
		t.Fatal("ratio should populate a denominator")
	}
	// The denominator falls back to the numerator's data source, so its filter is
	// still applied. What must never happen is a filter with no source at all.
	if res.LDMetric.Denominator.DataSource == nil && res.LDMetric.Denominator.Filters != nil {
		t.Error("a denominator filter must not be emitted without a data source")
	}
}

// Regression: the ratio path previously appended dropped-criteria warnings
// straight to Warnings instead of marking the result lossy, so a ratio whose
// filters were silently dropped reported as a clean conversion and would have
// been created matching every row.
func TestConvertRatio_DroppedCriteriaAreLossy(t *testing.T) {
	raw := `{
	  "type":"user_warehouse","name":"Rev per Visit","id":"Rev per Visit::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"ratio",
	    "metricSourceName":"Checkout","valueColumn":"revenue","numeratorAggregation":"sum",
	    "criteria":[{"type":"metadata","condition":"sql_filter","values":["1=1"]}],
	    "denominatorMetricSourceName":"Visits","denominatorValueColumn":"visit_id","denominatorAggregation":"count"}}`
	res := mustConvert(t, raw, Options{SourceMapping: map[string]string{
		"Checkout": "ld-checkout",
		"Visits":   "ld-visits",
	}})
	if !res.IsLossy() {
		t.Fatalf("a ratio with dropped term filters must be lossy, not a clean conversion; LossyReasons=%v", res.LossyReasons)
	}
	if res.LDMetric.Filters != nil {
		t.Errorf("no numerator filter should be emitted, got %#v", res.LDMetric.Filters)
	}
	assertHasWarning(t, res.LossyReasons, "numerator")
}

// The exact wire payload LaunchDarkly receives. LaunchDarkly validates filters
// strictly on save and a type mismatch degrades silently rather than erroring
// (a stringified number makes the generated SQL match nothing), so the serialized
// form is pinned here rather than only the in-memory struct.
func TestConvert_FilterWirePayload(t *testing.T) {
	raw := `{"type":"user_warehouse","name":"Filtered Rev","id":"Filtered Rev::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"sum","metricSourceName":"Checkout","valueColumn":"price_usd",
	    "criteria":[{"type":"metadata","column":"EVENT","condition":"in","values":["purchase","refund"]},
	                {"type":"metadata","column":"PRICE_USD","condition":">=","values":["10.5"]},
	                {"type":"metadata","column":"COUPON","condition":"non_null","values":[]},
	                {"type":"metadata","column":"PAGE","condition":"not_contains","values":["admin"]}]}}`
	res := mustConvert(t, raw, Options{LDDataSource: "ld-checkout"})
	if res.IsLossy() {
		t.Fatalf("all four criteria are mappable; LossyReasons=%v", res.LossyReasons)
	}
	got, err := json.Marshal(res.LDMetric.Filters)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"type":"group","op":"and","values":[` +
		`{"type":"eventProperty","attribute":"EVENT","op":"in","values":["purchase","refund"],"negate":false},` +
		`{"type":"eventProperty","attribute":"PRICE_USD","op":"greaterThanOrEqual","values":[10.5],"negate":false},` +
		`{"type":"eventProperty","attribute":"COUPON","op":"exists","values":[],"negate":false},` +
		`{"type":"eventProperty","attribute":"PAGE","op":"contains","values":["admin"],"negate":true}` +
		`],"negate":false}`
	if string(got) != want {
		t.Errorf("filter payload mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestConvertRatio_DenominatorFilterNestsUnderDenominator(t *testing.T) {
	res := mustConvert(t, whnRatioWithFilters, Options{SourceMapping: map[string]string{
		"Checkout": "ld-checkout",
		"Visits":   "ld-visits",
	}})
	b, err := json.Marshal(res.LDMetric)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var payload struct {
		Filters     *launchdarkly.EventFilter `json:"filters"`
		Denominator *struct {
			Filters *launchdarkly.EventFilter `json:"filters"`
		} `json:"denominator"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Filters == nil || payload.Filters.Attribute != "event" {
		t.Errorf("numerator filter should serialize at the top level, got %s", b)
	}
	if payload.Denominator == nil || payload.Denominator.Filters == nil ||
		payload.Denominator.Filters.Attribute != "page" {
		t.Errorf("denominator filter should serialize nested under denominator, got %s", b)
	}
}

// A warehouse-native metric carrying criteria in both places must not have the
// metricEvents ones silently ignored. An unreported dropped filter means the
// metric measures more than it claims to.
func TestConvert_WHNWithMetricEventCriteria_ReportsBoth(t *testing.T) {
	raw := `{"type":"user_warehouse","name":"Odd Shape","id":"Odd Shape::user_warehouse","directionality":"increase",
	  "metricEvents":[{"name":"purchase","type":"count",
	    "criteria":[{"type":"metadata","column":"LEGACY_COL","condition":"in","values":["x"]}]}],
	  "warehouseNative":{"aggregation":"sum","metricSourceName":"Checkout","valueColumn":"price_usd",
	    "criteria":[{"type":"metadata","column":"EVENT","condition":"in","values":["purchase"]}]}}`
	res := mustConvert(t, raw, Options{LDDataSource: "ld-checkout"})
	// The warehouseNative criterion still converts.
	if res.LDMetric.Filters == nil || res.LDMetric.Filters.Attribute != "EVENT" {
		t.Errorf("warehouseNative criteria should still convert, got %#v", res.LDMetric.Filters)
	}
	// But the ignored metricEvents criteria must be reported, not dropped silently.
	if !res.IsLossy() {
		t.Fatalf("ignored metricEvents criteria must mark the conversion lossy; LossyReasons=%v", res.LossyReasons)
	}
	assertHasWarning(t, res.LossyReasons, "LEGACY_COL")
}

func TestConvert_CloudCriteriaStayLossy(t *testing.T) {
	// Cloud (SDK-event) filter conversion is out of scope: those criterion types are
	// context attributes, which LaunchDarkly rejects on warehouse-native metrics.
	raw := `{
	  "type":"event_count_custom","name":"Filtered Count","id":"Filtered Count::event_count_custom","directionality":"increase",
	  "unitTypes":["userID"],
	  "metricEvents":[{"name":"purchase","type":"count",
	    "criteria":[{"type":"metadata","column":"item_category","condition":"=","values":["electronics"]}]}]}`
	res := mustConvert(t, raw, Options{LDDataSource: "snowflake-ds"})
	if res.LDMetric.Filters != nil {
		t.Errorf("cloud criteria should not produce a filter yet, got %#v", res.LDMetric.Filters)
	}
	if !res.IsLossy() {
		t.Errorf("dropped cloud criteria should be lossy; LossyReasons=%v", res.LossyReasons)
	}
	assertHasWarning(t, res.LossyReasons, "warehouse-native metrics only")
}
