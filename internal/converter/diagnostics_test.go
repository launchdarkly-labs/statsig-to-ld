package converter

import (
	"encoding/json"
	"testing"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
)

// WarningCodes runs parallel to Warnings and LossyCodes to LossyReasons. That
// invariant only holds while addWarning and addLossy are the sole writers, so it
// is worth pinning: a stray `append(result.Warnings, ...)` would silently
// misalign every code after it, and the report would attribute the wrong code to
// the wrong message.
func TestResult_WarningCodesStayAlignedWithWarnings(t *testing.T) {
	cases := map[string]string{
		"whn sum with a filter": `{"type":"user_warehouse","name":"A","id":"A::user_warehouse","directionality":"increase",
		  "warehouseNative":{"aggregation":"sum","metricSourceName":"S","valueColumn":"v","cap":5,"useLogTransform":true,
		    "criteria":[{"type":"metadata","column":"EVENT","condition":"in","values":["purchase"]}]}}`,
		"whn blocked filter, no data source": `{"type":"user_warehouse","name":"B","id":"B::user_warehouse","directionality":"increase",
		  "warehouseNative":{"aggregation":"sum","metricSourceName":"S","valueColumn":"v",
		    "criteria":[{"type":"metadata","condition":"sql_filter","values":["1=1"]}]}}`,
		"whn ratio, both terms filtered": whnRatioWithFilters,
		"cloud count with criteria": `{"type":"event_count_custom","name":"C","id":"C::event_count_custom","directionality":"increase",
		  "unitTypes":["planKey"],"metricEvents":[{"name":"purchase","type":"count",
		    "criteria":[{"type":"metadata","column":"cat","condition":"=","values":["x"]}]}]}`,
		"whn daily participation rate": `{"type":"user_warehouse","name":"D","id":"D::user_warehouse","directionality":"increase",
		  "warehouseNative":{"aggregation":"daily_participation","metricSourceName":"S"}}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			res := mustConvert(t, raw, Options{LDDataSource: "ds"})
			if len(res.Warnings) != len(res.WarningCodes) {
				t.Fatalf("Warnings (%d) and WarningCodes (%d) out of sync:\n  warnings=%v\n  codes=%v",
					len(res.Warnings), len(res.WarningCodes), res.Warnings, res.WarningCodes)
			}
			if len(res.LossyReasons) != len(res.LossyCodes) {
				t.Fatalf("LossyReasons (%d) and LossyCodes (%d) out of sync:\n  reasons=%v\n  codes=%v",
					len(res.LossyReasons), len(res.LossyCodes), res.LossyReasons, res.LossyCodes)
			}
			for i, c := range res.WarningCodes {
				if c == "" {
					t.Errorf("warning %d has no code: %q", i, res.Warnings[i])
				}
			}
			// Every lossy reason must also appear in the full warning list.
			for _, lr := range res.LossyReasons {
				found := false
				for _, w := range res.Warnings {
					if w == lr {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("lossy reason missing from Warnings: %q", lr)
				}
			}
		})
	}
}

func TestFilterOutcome_AppliedRecorded(t *testing.T) {
	raw := `{"type":"user_warehouse","name":"A","id":"A::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"sum","metricSourceName":"S","valueColumn":"v",
	    "criteria":[{"type":"metadata","column":"EVENT","condition":"in","values":["purchase"]},
	                {"type":"metadata","column":"PAGE","condition":"=","values":["checkout"]}]}}`
	res := mustConvert(t, raw, Options{LDDataSource: "ds"})
	if len(res.FilterOutcomes) != 1 {
		t.Fatalf("expected 1 filter outcome, got %#v", res.FilterOutcomes)
	}
	got := res.FilterOutcomes[0]
	if got.Term != "warehouse-native" || got.Criteria != 2 || !got.Applied {
		t.Errorf("outcome = %#v, want warehouse-native/2/applied", got)
	}
	if got.BlockedBy != "" || got.BlockedCondition != "" {
		t.Errorf("an applied outcome should carry no blocked fields, got %#v", got)
	}
}

func TestFilterOutcome_BlockedRecordsCodeAndCondition(t *testing.T) {
	tests := []struct {
		name          string
		raw           string
		opts          Options
		wantBlockedBy string
		wantCondition string
	}{
		{
			name: "unsupported condition names the condition",
			raw: `{"type":"user_warehouse","name":"A","id":"A::user_warehouse","directionality":"increase",
			  "warehouseNative":{"aggregation":"sum","metricSourceName":"S","valueColumn":"v",
			    "criteria":[{"type":"metadata","condition":"sql_filter","values":["1=1"]}]}}`,
			opts:          Options{LDDataSource: "ds"},
			wantBlockedBy: FilterBlockedCondition,
			wantCondition: "sql_filter",
		},
		{
			name: "no data source is not a condition problem",
			raw: `{"type":"user_warehouse","name":"B","id":"B::user_warehouse","directionality":"increase",
			  "warehouseNative":{"aggregation":"sum","metricSourceName":"S","valueColumn":"v",
			    "criteria":[{"type":"metadata","column":"EVENT","condition":"in","values":["purchase"]}]}}`,
			opts:          Options{},
			wantBlockedBy: FilterBlockedNoDataSource,
			wantCondition: "",
		},
		{
			name: "a bad value is reported as such, not as the condition",
			raw: `{"type":"user_warehouse","name":"C","id":"C::user_warehouse","directionality":"increase",
			  "warehouseNative":{"aggregation":"sum","metricSourceName":"S","valueColumn":"v",
			    "criteria":[{"type":"metadata","column":"PRICE","condition":">","values":["cheap"]}]}}`,
			opts:          Options{LDDataSource: "ds"},
			wantBlockedBy: FilterBlockedValue,
			wantCondition: ">",
		},
		{
			name: "cloud metric",
			raw: `{"type":"event_count_custom","name":"D","id":"D::event_count_custom","directionality":"increase",
			  "unitTypes":["userID"],"metricEvents":[{"name":"purchase","type":"count",
			    "criteria":[{"type":"metadata","column":"cat","condition":"=","values":["x"]}]}]}`,
			opts:          Options{LDDataSource: "ds"},
			wantBlockedBy: FilterBlockedCloudMetric,
			wantCondition: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := mustConvert(t, tt.raw, tt.opts)
			if len(res.FilterOutcomes) != 1 {
				t.Fatalf("expected 1 filter outcome, got %#v", res.FilterOutcomes)
			}
			got := res.FilterOutcomes[0]
			if got.Applied {
				t.Error("outcome should not be applied")
			}
			if got.BlockedBy != tt.wantBlockedBy {
				t.Errorf("BlockedBy = %q, want %q", got.BlockedBy, tt.wantBlockedBy)
			}
			if got.BlockedCondition != tt.wantCondition {
				t.Errorf("BlockedCondition = %q, want %q", got.BlockedCondition, tt.wantCondition)
			}
		})
	}
}

func TestFilterOutcome_RatioRecordsOnePerTerm(t *testing.T) {
	res := mustConvert(t, whnRatioWithFilters, Options{SourceMapping: map[string]string{
		"Checkout": "ld-checkout",
		"Visits":   "ld-visits",
	}})
	if len(res.FilterOutcomes) != 2 {
		t.Fatalf("a ratio with both terms filtered should record 2 outcomes, got %#v", res.FilterOutcomes)
	}
	terms := map[string]FilterOutcome{}
	for _, f := range res.FilterOutcomes {
		terms[f.Term] = f
	}
	for _, want := range []string{"numerator", "denominator"} {
		f, ok := terms[want]
		if !ok {
			t.Fatalf("no outcome recorded for the %s term: %#v", want, res.FilterOutcomes)
		}
		if !f.Applied || f.Criteria != 1 {
			t.Errorf("%s outcome = %#v, want applied with 1 criterion", want, f)
		}
	}
}

func TestFilterOutcome_NoneWhenNoCriteria(t *testing.T) {
	raw := `{"type":"user_warehouse","name":"A","id":"A::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"sum","metricSourceName":"S","valueColumn":"v"}}`
	res := mustConvert(t, raw, Options{LDDataSource: "ds"})
	if len(res.FilterOutcomes) != 0 {
		t.Errorf("a metric with no criteria should record no filter outcomes, got %#v", res.FilterOutcomes)
	}
}

// The rollup window is what distinguishes a lossy daily-participation rate from a
// clean one-time or windowed unit count. It has to reach the report, because it is
// not otherwise recoverable from a shared run.
func TestDiagnosticFields_AvailableOnResult(t *testing.T) {
	raw := `{"type":"user_warehouse","name":"A","id":"A::user_warehouse","directionality":"increase",
	  "unitTypes":["companyID"],
	  "warehouseNative":{"aggregation":"daily_participation","metricSourceName":"Checkout","rollupTimeWindow":"max"}}`
	var m statsig.Metric
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := m.EffectiveRollupTimeWindow(); got != "max" {
		t.Errorf("EffectiveRollupTimeWindow = %q, want max", got)
	}
	if got := m.NumeratorSourceName(); got != "Checkout" {
		t.Errorf("NumeratorSourceName = %q, want Checkout", got)
	}
	res := mustConvert(t, raw, Options{LDDataSource: "ds"})
	// An unmapped unit type is lowercased as a best guess, not translated, and
	// warned about. --unit-type-mapping is how you turn companyID into "company".
	if len(res.LDMetric.RandomizationUnits) != 1 || res.LDMetric.RandomizationUnits[0] != "companyid" {
		t.Errorf("RandomizationUnits = %v, want [companyid]", res.LDMetric.RandomizationUnits)
	}
	assertHasWarning(t, res.Warnings, "may not match an LD context kind")
	if res.LDMetric.DataSource == nil || res.LDMetric.DataSource.Key != "ds" {
		t.Errorf("DataSource = %#v, want key ds", res.LDMetric.DataSource)
	}

	// And with a mapping supplied, it resolves properly.
	mapped := mustConvert(t, raw, Options{LDDataSource: "ds",
		UnitTypeMapping: map[string]string{"companyID": "company"}})
	if len(mapped.LDMetric.RandomizationUnits) != 1 || mapped.LDMetric.RandomizationUnits[0] != "company" {
		t.Errorf("with --unit-type-mapping, RandomizationUnits = %v, want [company]", mapped.LDMetric.RandomizationUnits)
	}
}
