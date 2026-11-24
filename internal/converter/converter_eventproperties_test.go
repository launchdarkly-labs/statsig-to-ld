package converter

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/warehouse"
)

func dimensionMetric(columns ...string) string {
	cols, _ := json.Marshal(columns)
	return fmt.Sprintf(`{
	  "type":"user_warehouse","name":"Edits","id":"Edits::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"sum","metricSourceName":"File Events","valueColumn":"edits",
	    "metricDimensionColumns":%s}}`, cols)
}

var fileEventColumns = []warehouse.EventPropertyColumn{
	{Name: "TS", Unsupported: "it is the timestamp column"},
	{Name: "DEVICE"},
	{Name: "PLAN_TIER"},
	{Name: "IS_PRO"},
	{Name: "EDITS", Unsupported: `its type is "FLOAT"`},
}

func eventPropertyOpts(cols []warehouse.EventPropertyColumn) Options {
	return Options{
		LDDataSource:                   "file-events",
		DataSourceEventPropertyColumns: map[string][]warehouse.EventPropertyColumn{"file-events": cols},
	}
}

func eventPropertyCodes(res *Result) []string {
	var out []string
	for _, c := range res.WarningCodes {
		if strings.HasPrefix(c, "event_propert") {
			out = append(out, c)
		}
	}
	return out
}

func warningWithCode(t *testing.T, res *Result, code string) string {
	t.Helper()
	for i, c := range res.WarningCodes {
		if c == code {
			return res.Warnings[i]
		}
	}
	t.Fatalf("no %s warning; codes=%v warnings=%v", code, res.WarningCodes, res.Warnings)
	return ""
}

func TestConvert_WHN_DimensionColumnsBecomeEventProperties(t *testing.T) {
	res := mustConvert(t, dimensionMetric("device", "plan_tier"), eventPropertyOpts(fileEventColumns))
	if got, want := res.LDMetric.SlicedEventPropertyColumns, []string{"DEVICE", "PLAN_TIER"}; !slices.Equal(got, want) {
		t.Errorf("SlicedEventPropertyColumns = %v, want %v", got, want)
	}
	if codes := eventPropertyCodes(res); len(codes) != 0 {
		t.Errorf("unexpected event property warnings %v: %v", codes, res.Warnings)
	}
	if slices.Contains(res.WarningCodes, WarnAnalysisFeaturesDropped) {
		t.Errorf("dimension columns still reported as dropped: %v", res.Warnings)
	}
	if res.IsLossy() {
		t.Errorf("LossyReasons = %v", res.LossyReasons)
	}
	raw, _ := json.Marshal(res.LDMetric)
	if !strings.Contains(string(raw), `"slicedEventPropertyColumns":["DEVICE","PLAN_TIER"]`) {
		t.Errorf("wire payload = %s", raw)
	}
}

func TestConvert_WHN_BlankDimensionColumnsAreIgnored(t *testing.T) {
	res := mustConvert(t, dimensionMetric("", " ", "device"), eventPropertyOpts(fileEventColumns))
	if got, want := res.LDMetric.SlicedEventPropertyColumns, []string{"DEVICE"}; !slices.Equal(got, want) {
		t.Errorf("SlicedEventPropertyColumns = %v, want %v", got, want)
	}
	if codes := eventPropertyCodes(res); len(codes) != 0 {
		t.Errorf("unexpected warnings %v: %v", codes, res.Warnings)
	}
}

func TestConvert_WHN_NoDimensionColumnsSendsNone(t *testing.T) {
	res := mustConvert(t, dimensionMetric(), eventPropertyOpts(fileEventColumns))
	raw, _ := json.Marshal(res.LDMetric)
	if strings.Contains(string(raw), "slicedEventPropertyColumns") {
		t.Errorf("wire payload = %s, want no slicedEventPropertyColumns", raw)
	}
}

// LaunchDarkly rejects the whole metric for one bad column, so the converter drops
// those columns and keeps the metric.
func TestConvert_WHN_EventPropertiesLaunchDarklyWouldRejectAreDropped(t *testing.T) {
	res := mustConvert(t, dimensionMetric("edits", "device", "ts", "browser"), eventPropertyOpts(fileEventColumns))
	if got, want := res.LDMetric.SlicedEventPropertyColumns, []string{"DEVICE"}; !slices.Equal(got, want) {
		t.Errorf("SlicedEventPropertyColumns = %v, want %v", got, want)
	}
	if res.IsLossy() {
		t.Errorf("dropping event properties leaves the metric's numbers alone; LossyReasons = %v", res.LossyReasons)
	}
	unsupported := warningWithCode(t, res, WarnEventPropertyUnsupported)
	for _, want := range []string{`"EDITS" (its type is "FLOAT")`, `"TS" (it is the timestamp column)`} {
		if !strings.Contains(unsupported, want) {
			t.Errorf("unsupported warning %q lacks %s", unsupported, want)
		}
	}
	missing := warningWithCode(t, res, WarnEventPropertyNotInDataSource)
	if !strings.Contains(missing, `"browser"`) || !strings.Contains(missing, `"file-events"`) || strings.Contains(missing, "EDITS") {
		t.Errorf("missing warning = %q", missing)
	}
}

func TestConvert_WHN_EventPropertiesKeepTheFirstColumnsUpToTheLimit(t *testing.T) {
	cols := []warehouse.EventPropertyColumn{{Name: "TS", Unsupported: "it is the timestamp column"}}
	statsigCols := []string{"ts"}
	for i := 1; i <= 12; i++ {
		cols = append(cols, warehouse.EventPropertyColumn{Name: fmt.Sprintf("C%d", i)})
		statsigCols = append(statsigCols, fmt.Sprintf("c%d", i))
	}

	res := mustConvert(t, dimensionMetric(statsigCols...), eventPropertyOpts(cols))
	if got := res.LDMetric.SlicedEventPropertyColumns; len(got) != 10 || got[0] != "C1" || got[9] != "C10" {
		t.Errorf("SlicedEventPropertyColumns = %v, want C1..C10", got)
	}
	limit := warningWithCode(t, res, WarnEventPropertyLimit)
	if !strings.Contains(limit, `"C11", "C12"`) || !strings.Contains(limit, "--max-event-properties") || strings.Contains(limit, `"C10"`) {
		t.Errorf("limit warning = %q", limit)
	}

	opts := eventPropertyOpts(cols)
	opts.MaxEventProperties = 12
	res = mustConvert(t, dimensionMetric(statsigCols...), opts)
	if got := res.LDMetric.SlicedEventPropertyColumns; len(got) != 12 {
		t.Errorf("with a limit of 12, SlicedEventPropertyColumns = %v", got)
	}
	if slices.Contains(res.WarningCodes, WarnEventPropertyLimit) {
		t.Errorf("unexpected limit warning: %v", res.Warnings)
	}
}

func TestConvert_WHN_EventPropertyNamedTwiceIsSentOnce(t *testing.T) {
	res := mustConvert(t, dimensionMetric("device", "DEVICE", "Device"), eventPropertyOpts(fileEventColumns))
	if got, want := res.LDMetric.SlicedEventPropertyColumns, []string{"DEVICE"}; !slices.Equal(got, want) {
		t.Errorf("SlicedEventPropertyColumns = %v, want %v", got, want)
	}
	if codes := eventPropertyCodes(res); len(codes) != 0 {
		t.Errorf("unexpected warnings %v: %v", codes, res.Warnings)
	}
}

// LaunchDarkly matches names exactly, so the converter sends the data source's spelling
// and, among columns that differ only in case, one it accepts.
func TestConvert_WHN_EventPropertyTakesTheColumnLaunchDarklyAccepts(t *testing.T) {
	cols := []warehouse.EventPropertyColumn{
		{Name: "device", Unsupported: `its type is "FLOAT"`},
		{Name: "DEVICE"},
		{Name: "plan"},
	}
	res := mustConvert(t, dimensionMetric("Device", "plan"), eventPropertyOpts(cols))
	if got, want := res.LDMetric.SlicedEventPropertyColumns, []string{"DEVICE", "plan"}; !slices.Equal(got, want) {
		t.Errorf("SlicedEventPropertyColumns = %v, want %v", got, want)
	}

	cols = []warehouse.EventPropertyColumn{{Name: "Plan"}, {Name: "plan"}}
	res = mustConvert(t, dimensionMetric("plan"), eventPropertyOpts(cols))
	if got, want := res.LDMetric.SlicedEventPropertyColumns, []string{"plan"}; !slices.Equal(got, want) {
		t.Errorf("SlicedEventPropertyColumns = %v, want the exact match %v", got, want)
	}
}

// The column Statsig names exactly is the one it sliced by, so a case variant never stands in for it.
func TestConvert_WHN_EventPropertyExactMatchWinsEvenWhenUnsupported(t *testing.T) {
	cols := []warehouse.EventPropertyColumn{
		{Name: "device", Unsupported: `its type is "FLOAT"`},
		{Name: "DEVICE"},
	}
	res := mustConvert(t, dimensionMetric("device"), eventPropertyOpts(cols))
	if got := res.LDMetric.SlicedEventPropertyColumns; len(got) != 0 {
		t.Errorf("SlicedEventPropertyColumns = %v, want none", got)
	}
	if w := warningWithCode(t, res, WarnEventPropertyUnsupported); !strings.Contains(w, `"device" (its type is "FLOAT")`) {
		t.Errorf("warning = %q", w)
	}
}

func TestConvert_WHN_EventPropertiesNeedADataSource(t *testing.T) {
	res := mustConvert(t, dimensionMetric("device"), Options{})
	if got := res.LDMetric.SlicedEventPropertyColumns; got != nil {
		t.Errorf("SlicedEventPropertyColumns = %v, want none", got)
	}
	if w := warningWithCode(t, res, WarnEventPropertyNoDataSource); !strings.Contains(w, `"device"`) {
		t.Errorf("warning = %q", w)
	}
}

// A dry run without LaunchDarkly access can't see the data source's columns.
func TestConvert_WHN_EventPropertiesUnverifiedWithoutTheColumnList(t *testing.T) {
	cols := []string{"device", "device"}
	for i := 1; i <= 11; i++ {
		cols = append(cols, fmt.Sprintf("c%d", i))
	}
	res := mustConvert(t, dimensionMetric(cols...), Options{LDDataSource: "file-events"})
	got := res.LDMetric.SlicedEventPropertyColumns
	if len(got) != 10 || got[0] != "device" || got[1] != "c1" {
		t.Errorf("SlicedEventPropertyColumns = %v, want Statsig's first 10 distinct names", got)
	}
	if !slices.Contains(res.NoteCodes, WarnColumnUnverified) {
		t.Errorf("NoteCodes = %v, want %s", res.NoteCodes, WarnColumnUnverified)
	}
	warningWithCode(t, res, WarnEventPropertyLimit)
}

func TestConvert_WHN_EventPropertiesOnADataSourceWithNoColumns(t *testing.T) {
	res := mustConvert(t, dimensionMetric("device"), eventPropertyOpts(nil))
	if got := res.LDMetric.SlicedEventPropertyColumns; got != nil {
		t.Errorf("SlicedEventPropertyColumns = %v, want none", got)
	}
	warningWithCode(t, res, WarnEventPropertyNotInDataSource)
	if slices.Contains(res.NoteCodes, WarnColumnUnverified) {
		t.Errorf("a listed data source's columns are known, even when there are none: %v", res.NoteCodes)
	}
}

// LaunchDarkly slices a ratio's numerator, checking the columns on its data source.
func TestConvert_WHN_RatioEventPropertiesUseTheNumeratorDataSource(t *testing.T) {
	raw := `{
	  "type":"user_warehouse","name":"Rev per Visit","id":"Rev per Visit::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"ratio",
	    "metricSourceName":"Checkout","valueColumn":"revenue","numeratorAggregation":"sum",
	    "denominatorMetricSourceName":"Visits","denominatorValueColumn":"visit_id","denominatorAggregation":"count",
	    "metricDimensionColumns":["device","referrer"]}}`
	res := mustConvert(t, raw, Options{
		SourceMapping: map[string]string{"Checkout": "ld-checkout", "Visits": "ld-visits"},
		DataSourceEventPropertyColumns: map[string][]warehouse.EventPropertyColumn{
			"ld-checkout": {{Name: "DEVICE"}},
			"ld-visits":   {{Name: "REFERRER"}},
		},
	})
	if got, want := res.LDMetric.SlicedEventPropertyColumns, []string{"DEVICE"}; !slices.Equal(got, want) {
		t.Errorf("SlicedEventPropertyColumns = %v, want %v", got, want)
	}
	if w := warningWithCode(t, res, WarnEventPropertyNotInDataSource); !strings.Contains(w, `"referrer"`) || !strings.Contains(w, `"ld-checkout"`) {
		t.Errorf("warning = %q", w)
	}
	if slices.Contains(res.WarningCodes, WarnAnalysisFeaturesDropped) {
		t.Errorf("dimension columns still reported as dropped: %v", res.Warnings)
	}
}

func TestResult_DropEventPropertiesNotEnabled(t *testing.T) {
	res := mustConvert(t, dimensionMetric("device", "plan_tier"), eventPropertyOpts(fileEventColumns))
	res.DropEventPropertiesNotEnabled()
	if got := res.LDMetric.SlicedEventPropertyColumns; got != nil {
		t.Errorf("SlicedEventPropertyColumns = %v, want none", got)
	}
	w := warningWithCode(t, res, WarnEventPropertiesNotEnabled)
	if !strings.Contains(w, `"DEVICE", "PLAN_TIER"`) {
		t.Errorf("warning = %q", w)
	}
	if len(res.Warnings) != len(res.WarningCodes) {
		t.Errorf("Warnings and WarningCodes out of step: %v / %v", res.Warnings, res.WarningCodes)
	}

	before := len(res.Warnings)
	res.DropEventPropertiesNotEnabled()
	if len(res.Warnings) != before {
		t.Errorf("a metric without event properties got another warning: %v", res.Warnings)
	}
}

func TestResult_DropEventPropertiesRejected(t *testing.T) {
	res := mustConvert(t, dimensionMetric("device", "plan_tier"), eventPropertyOpts(fileEventColumns))
	res.DropEventPropertiesRejected("`slicedEventPropertyColumns` can have at most 1 columns")
	if got := res.LDMetric.SlicedEventPropertyColumns; got != nil {
		t.Errorf("SlicedEventPropertyColumns = %v, want none", got)
	}
	w := warningWithCode(t, res, WarnEventPropertiesRejected)
	if !strings.Contains(w, `"DEVICE", "PLAN_TIER"`) || !strings.Contains(w, "can have at most 1 columns") {
		t.Errorf("warning = %q, want the columns and LaunchDarkly's reason", w)
	}
	if len(res.Warnings) != len(res.WarningCodes) {
		t.Errorf("Warnings and WarningCodes out of step: %v / %v", res.Warnings, res.WarningCodes)
	}
}
