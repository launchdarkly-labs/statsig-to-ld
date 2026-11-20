package statsig

import "testing"

func TestSQLFilterColumn_NamesTheColumnAfterTheSQL(t *testing.T) {
	for sql, want := range map[string]string{
		"COALESCE(plan_tier, 0) > 2":         "coalesce_plan_tier_0_gt_2",
		"  LOWER(region) = 'eu'\n":           "lower_region_eq_eu",
		"amount <> 10 OR amount != 20":       "amount_ne_10_or_amount_ne_20",
		"a >= 1 AND b <= 2 AND c < 3":        "a_ge_1_and_b_le_2_and_c_lt_3",
		"DATEDIFF('day', signup_ts, ts) < 7": "datediff_day_signup_ts_ts_lt_7",
		"__x__ IS NOT NULL":                  "x_is_not_null",
		"1 = 1":                              "sql_1_eq_1",
		"TRUE":                               "true_filter",
		"()":                                 "sql_filter",
	} {
		if got := SQLFilterColumn(sql); got != want {
			t.Errorf("SQLFilterColumn(%q) = %q, want %q", sql, got, want)
		}
	}
}

func TestSQLFilterColumn_ShortensLongSQLAtAWord(t *testing.T) {
	got := SQLFilterColumn("COALESCE(first_purchase_amount, 0) + COALESCE(second_purchase_amount, 0) > 100")
	if want := "coalesce_first_purchase_amount_0_coalesce_second_purchase"; got != want {
		t.Errorf("SQLFilterColumn = %q, want %q", got, want)
	}
}

func TestCriterion_SQLFilter(t *testing.T) {
	tests := []struct {
		name   string
		c      Criterion
		want   string
		wantOK bool
	}{
		{"one snippet", Criterion{Condition: "sql_filter", Values: []string{" plan_tier > 2\n"}}, "plan_tier > 2", true},
		{"not a sql_filter", Criterion{Condition: "in", Column: "plan_tier", Values: []string{"2"}}, "", false},
		{"no snippet", Criterion{Condition: "sql_filter"}, "", false},
		{"blank snippet", Criterion{Condition: "sql_filter", Values: []string{"  "}}, "", false},
		{"two snippets", Criterion{Condition: "sql_filter", Values: []string{"a = 1", "b = 2"}}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.c.SQLFilter()
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("SQLFilter() = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
