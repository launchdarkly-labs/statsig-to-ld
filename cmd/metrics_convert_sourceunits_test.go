package cmd

import (
	"testing"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
)

func TestNeedsSourceUnitTypes(t *testing.T) {
	whnNoUnits := statsig.Metric{Type: "user_warehouse", WarehouseNative: &statsig.WarehouseNative{Aggregation: "sum"}}
	whnWithUnits := statsig.Metric{Type: "user_warehouse", UnitTypes: []string{"userID"}, WarehouseNative: &statsig.WarehouseNative{Aggregation: "sum"}}
	cloudNoUnits := statsig.Metric{Type: "event_count"}

	cases := []struct {
		name    string
		metrics []statsig.Metric
		want    bool
	}{
		{"whn without unitTypes", []statsig.Metric{cloudNoUnits, whnNoUnits}, true},
		{"whn with unitTypes only", []statsig.Metric{whnWithUnits}, false},
		{"cloud only", []statsig.Metric{cloudNoUnits}, false},
		{"empty", nil, false},
	}
	for _, tc := range cases {
		if got := needsSourceUnitTypes(tc.metrics); got != tc.want {
			t.Errorf("%s: needsSourceUnitTypes = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestBuildSourceUnitTypes(t *testing.T) {
	sources := []statsig.MetricSourceConfig{
		{Name: "checkout", IDTypeMapping: []statsig.IDTypeMapping{
			{StatsigUnitID: "userID", Column: "user_id"},
			{StatsigUnitID: "companyID", Column: "company_id"},
		}},
		{Name: "no_mapping", IDTypeMapping: nil},                                   // skipped (no ids)
		{Name: "", IDTypeMapping: []statsig.IDTypeMapping{{StatsigUnitID: "x"}}},    // skipped (no name)
		{Name: "blank_id", IDTypeMapping: []statsig.IDTypeMapping{{Column: "c"}}},   // skipped (blank id)
	}
	got := buildSourceUnitTypes(sources)

	if len(got) != 1 {
		t.Fatalf("expected 1 usable source, got %d: %v", len(got), got)
	}
	units := got["checkout"]
	if len(units) != 2 || units[0] != "userID" || units[1] != "companyID" {
		t.Errorf("checkout units = %v, want [userID companyID]", units)
	}
}
