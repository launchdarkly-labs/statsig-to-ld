package statsig

import "testing"

// warehouse and metrics convert each derive the column name, possibly in different
// versions, so the scheme is pinned.
func TestSQLFilterColumn_IsPinnedAndIgnoresSurroundingSpace(t *testing.T) {
	if got := SQLFilterColumn("plan_tier > 2"); got != "sql_filter_6e4004268a3c8fc1" {
		t.Errorf("SQLFilterColumn = %q, want sql_filter_6e4004268a3c8fc1", got)
	}
	if got := SQLFilterColumn("  plan_tier > 2\n"); got != "sql_filter_6e4004268a3c8fc1" {
		t.Errorf("surrounding space changed the column: %q", got)
	}
	if SQLFilterColumn("plan_tier > 3") == SQLFilterColumn("plan_tier > 2") {
		t.Error("different filters share a column")
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
