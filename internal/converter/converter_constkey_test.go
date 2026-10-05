package converter

import (
	"slices"
	"testing"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
)

func constKeyOpts() Options {
	return Options{
		SourceMapping: map[string]string{"Checkout Events": "checkout-events", "Page Views": "page-views"},
		ConstantEventKeys: map[string]string{
			"checkout-events": "checkout-events",
			"page-views":      "page-views",
		},
		DataSourceColumns: map[string][]string{
			"checkout-events": {"TS", "USER_ID", "ORDER_TOTAL", "EVENT", "PLAN", "LD_EVENT_KEY"},
			"page-views":      {"TS", "USER_ID", "PAGE_ID", "LD_EVENT_KEY"},
		},
	}
}

func hasCode(codes []string, code string) bool { return slices.Contains(codes, code) }

func TestConvert_ConstKey_SumUsesDataSourceKeyAndValueColumn(t *testing.T) {
	raw := `{"type":"user_warehouse","name":"Order Total","id":"Order Total::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"sum","metricSourceName":"Checkout Events","valueColumn":"order_total",
	    "criteria":[{"type":"metadata","column":"event","condition":"in","values":["purchase"]},
	                {"type":"metadata","column":"Plan","condition":"in","values":["pro"]}]}}`
	res := mustConvert(t, raw, constKeyOpts())
	m := res.LDMetric
	if m.EventKey != "checkout-events" {
		t.Errorf("EventKey = %q, want the data source key", m.EventKey)
	}
	if m.ValueColumn != "ORDER_TOTAL" {
		t.Errorf("ValueColumn = %q, want ORDER_TOTAL (Statsig column, data source case)", m.ValueColumn)
	}
	if m.Filters == nil || m.Filters.Type != launchdarkly.EventFilterTypeGroup || len(m.Filters.Values) != 2 {
		t.Fatalf("Filters = %#v, want a two-leaf group", m.Filters)
	}
	for i, want := range []string{"EVENT", "PLAN"} {
		if got := m.Filters.Values[i].(launchdarkly.EventFilter).Attribute; got != want {
			t.Errorf("filter leaf %d attribute = %q, want %q", i, got, want)
		}
	}
	if res.IsLossy() || res.IsBlocked() {
		t.Errorf("unexpected lossy=%v blocked=%v", res.LossyReasons, res.BlockingReasons)
	}
}

func TestConvert_ConstKey_CountHasNoProvisionalWarning(t *testing.T) {
	raw := `{"type":"user_warehouse","name":"Checkouts","id":"Checkouts::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"count","metricSourceName":"Checkout Events"}}`
	res := mustConvert(t, raw, constKeyOpts())
	if res.LDMetric.EventKey != "checkout-events" {
		t.Errorf("EventKey = %q, want checkout-events", res.LDMetric.EventKey)
	}
	if res.LDMetric.ValueColumn != "" {
		t.Errorf("count metric must not set ValueColumn, got %q", res.LDMetric.ValueColumn)
	}
	if hasCode(res.WarningCodes, WarnNoValueColumn) {
		t.Error("constant-key metrics should not carry the provisional event-key warning")
	}
}

func TestConvert_ConstKey_CountDistinctUsesFieldNotValueColumn(t *testing.T) {
	raw := `{"type":"user_warehouse","name":"Distinct Pages","id":"Distinct Pages::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"count_distinct","metricSourceName":"Page Views","valueColumn":"page_id"}}`
	res := mustConvert(t, raw, constKeyOpts())
	if res.LDMetric.EventKey != "page-views" || res.LDMetric.UnitAggregationField != "PAGE_ID" || res.LDMetric.ValueColumn != "" {
		t.Errorf("got eventKey=%q field=%q valueColumn=%q", res.LDMetric.EventKey, res.LDMetric.UnitAggregationField, res.LDMetric.ValueColumn)
	}
}

func TestConvert_ConstKey_RatioTermsUseTheirOwnDataSourceKeys(t *testing.T) {
	raw := `{"type":"user_warehouse","name":"Order Total per Page View","id":"Order Total per Page View::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"ratio",
	    "metricSourceName":"Checkout Events","valueColumn":"order_total","numeratorAggregation":"sum",
	    "denominatorMetricSourceName":"Page Views","denominatorValueColumn":"page_id","denominatorAggregation":"count"}}`
	res := mustConvert(t, raw, constKeyOpts())
	m := res.LDMetric
	if m.EventKey != "checkout-events" || m.ValueColumn != "ORDER_TOTAL" {
		t.Errorf("numerator eventKey=%q valueColumn=%q, want checkout-events/ORDER_TOTAL", m.EventKey, m.ValueColumn)
	}
	if m.Denominator.EventName != "page-views" {
		t.Errorf("denominator EventName = %q, want page-views (its own data source key)", m.Denominator.EventName)
	}
	if m.Denominator.ValueColumn != "" {
		t.Errorf("count denominator must not set ValueColumn, got %q", m.Denominator.ValueColumn)
	}
}

func TestConvert_ConstKey_RatioSameSourceBothTermsSameKey(t *testing.T) {
	raw := `{"type":"user_warehouse","name":"R","id":"R::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"ratio",
	    "metricSourceName":"Checkout Events","valueColumn":"order_total","numeratorAggregation":"sum",
	    "denominatorAggregation":"count"}}`
	res := mustConvert(t, raw, constKeyOpts())
	m := res.LDMetric
	if m.EventKey != "checkout-events" || m.Denominator == nil || m.Denominator.EventName != "checkout-events" {
		t.Errorf("eventKey=%q den=%+v, want checkout-events on both terms", m.EventKey, m.Denominator)
	}
	if m.ValueColumn != "ORDER_TOTAL" {
		t.Errorf("ValueColumn=%q", m.ValueColumn)
	}
}

// A warehouse-native ratio term carries its column as the term's value column,
// not in MetadataKey where a cloud event carries it. A count_distinct term
// must keep that column rather than fall back to counting units.
func TestConvert_ConstKey_RatioCountDistinctTermsKeepTheirColumns(t *testing.T) {
	raw := `{"type":"user_warehouse","name":"Distinct Orders per Page","id":"Distinct Orders per Page::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"ratio",
	    "metricSourceName":"Checkout Events","valueColumn":"order_total","numeratorAggregation":"count_distinct",
	    "denominatorMetricSourceName":"Page Views","denominatorValueColumn":"page_id","denominatorAggregation":"count_distinct"}}`
	res := mustConvert(t, raw, constKeyOpts())
	m := res.LDMetric
	if m.UnitAggregationType != "count_distinct" || m.UnitAggregationField != "ORDER_TOTAL" {
		t.Errorf("numerator unitAgg=%q field=%q, want count_distinct/ORDER_TOTAL (lossy=%v)", m.UnitAggregationType, m.UnitAggregationField, res.LossyReasons)
	}
	if m.Denominator.UnitAggregationType != "count_distinct" || m.Denominator.UnitAggregationField != "PAGE_ID" {
		t.Errorf("denominator unitAgg=%q field=%q, want count_distinct/PAGE_ID", m.Denominator.UnitAggregationType, m.Denominator.UnitAggregationField)
	}
	if m.ValueColumn != "" || m.Denominator.ValueColumn != "" {
		t.Errorf("count_distinct terms must not set valueColumn: %q / %q", m.ValueColumn, m.Denominator.ValueColumn)
	}
}

// Filter columns nested in a group are case-mapped.
func TestConvert_ConstKey_FilterCaseMappingNested(t *testing.T) {
	raw := `{"type":"user_warehouse","name":"S","id":"S::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"count","metricSourceName":"Checkout Events",
	    "criteria":[{"type":"metadata","column":"plan","condition":"in","values":["pro","team"]},
	                {"type":"metadata","column":"event","condition":"not_in","values":["bot"]}]}}`
	res := mustConvert(t, raw, constKeyOpts())
	attrs := filterAttributes(res.LDMetric.Filters)
	if !slices.Equal(attrs, []string{"PLAN", "EVENT"}) {
		t.Errorf("filter attributes = %v, want [PLAN EVENT]; warnings=%v", attrs, res.Warnings)
	}
}

func TestConvert_ConstKey_MissingColumnIsLossy(t *testing.T) {
	raw := `{"type":"user_warehouse","name":"Rev","id":"Rev::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"sum","metricSourceName":"Checkout Events","valueColumn":"revenue"}}`
	res := mustConvert(t, raw, constKeyOpts())
	if !res.IsLossy() {
		t.Error("a value column the data source lacks should be lossy")
	}
}

// With --assume-constant-event-key and no LaunchDarkly access, the data
// source's columns are unknown, so value and filter columns keep Statsig's
// case. That must be visible on the metric rather than read as verified.
func TestConvert_ConstKey_UnknownColumnsAreFlaggedUnverified(t *testing.T) {
	raw := `{"type":"user_warehouse","name":"Order Total","id":"Order Total::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"sum","metricSourceName":"Checkout Events","valueColumn":"order_total",
	    "criteria":[{"type":"metadata","column":"plan","condition":"in","values":["pro"]}]}}`
	opts := constKeyOpts()
	opts.DataSourceColumns = nil
	res := mustConvert(t, raw, opts)
	if res.LDMetric.ValueColumn != "order_total" {
		t.Errorf("ValueColumn = %q, want Statsig's order_total unchanged", res.LDMetric.ValueColumn)
	}
	if !hasCode(res.WarningCodes, WarnColumnUnverified) {
		t.Errorf("want a %s warning; codes=%v", WarnColumnUnverified, res.WarningCodes)
	}
	if res.IsLossy() {
		t.Errorf("unverified is advisory, not lossy: %v", res.LossyReasons)
	}
}

// A data source not created by the wrapper (for example a hand-built one passed
// with --ld-data-source) keeps today's behavior.
func TestConvert_ConstKey_OnlyForConstantDataSources(t *testing.T) {
	raw := `{"type":"user_warehouse","name":"Rev","id":"Rev::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"sum","metricSourceName":"Unmapped","valueColumn":"price_usd"}}`
	opts := constKeyOpts()
	opts.LDDataSource = "hand-built"
	res := mustConvert(t, raw, opts)
	if res.LDMetric.EventKey != "price_usd" || res.LDMetric.ValueColumn != "" {
		t.Errorf("got eventKey=%q valueColumn=%q, want legacy price_usd / empty", res.LDMetric.EventKey, res.LDMetric.ValueColumn)
	}
	if hasCode(res.WarningCodes, WarnColumnUnverified) {
		t.Error("a legacy data source sends no value column, so there is nothing unverified to warn about")
	}
}

// A cloud (SDK-event) metric bound through --ld-data-source to a wrapped
// source keeps its event name, which can never equal the constant.
func TestConvert_ConstKey_CloudMetricOnWrappedSourceWarns(t *testing.T) {
	raw := `{"type":"event_count","name":"Clicks","id":"Clicks::event_count","directionality":"increase",
	  "metricEvents":[{"name":"add_to_cart","type":"count"}]}`
	opts := constKeyOpts()
	opts.LDDataSource = "checkout-events"
	res := mustConvert(t, raw, opts)
	if res.LDMetric.EventKey != "add_to_cart" {
		t.Errorf("EventKey = %q, want the cloud event name unchanged", res.LDMetric.EventKey)
	}
	if !hasCode(res.WarningCodes, WarnCloudMetricConstantKey) {
		t.Errorf("want a %s warning; warnings=%v", WarnCloudMetricConstantKey, res.Warnings)
	}
}

func TestConvert_ConstKey_CloudRatioOnWrappedSourceWarns(t *testing.T) {
	raw := `{"type":"ratio","name":"Carts per View","id":"Carts per View::ratio","directionality":"increase",
	  "metricEvents":[{"name":"page_view","type":"count"},{"name":"add_to_cart","type":"count"}]}`
	opts := constKeyOpts()
	opts.LDDataSource = "checkout-events"
	res := mustConvert(t, raw, opts)
	n := 0
	for _, c := range res.WarningCodes {
		if c == WarnCloudMetricConstantKey {
			n++
		}
	}
	if n != 2 {
		t.Errorf("want one %s warning per term, got %d; warnings=%v", WarnCloudMetricConstantKey, n, res.Warnings)
	}
}

// On a constant-key data source the event key matches every row, so a filter
// that did not convert leaves the metric counting the whole source. That is
// never acceptable, so the metric is blocked, not merely lossy.
func TestConvert_ConstKey_UnconvertedFilterBlocksMetric(t *testing.T) {
	raw := `{"type":"user_warehouse","name":"Pro Checkouts","id":"Pro Checkouts::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"count","metricSourceName":"Checkout Events",
	    "criteria":[{"type":"metadata","column":"plan","condition":"in","values":["pro"]},
	                {"type":"metadata","condition":"sql_filter","values":["plan_tier > 2"]}]}}`
	res := mustConvert(t, raw, constKeyOpts())
	if !res.IsBlocked() || !hasCode(res.BlockingCodes, WarnUnfilteredConstantKey) {
		t.Fatalf("want blocked by %s; blocking=%v warnings=%v", WarnUnfilteredConstantKey, res.BlockingReasons, res.Warnings)
	}
	if res.LDMetric.Filters != nil {
		t.Errorf("no partial filter may be emitted: %#v", res.LDMetric.Filters)
	}

	// The same metric on a legacy data source stays merely lossy: there its event
	// key, not the filter, is what keeps it from counting every row.
	legacy := constKeyOpts()
	legacy.ConstantEventKeys = nil
	res = mustConvert(t, raw, legacy)
	if res.IsBlocked() || !res.IsLossy() {
		t.Errorf("legacy source: blocked=%v lossy=%v, want lossy only", res.BlockingReasons, res.LossyReasons)
	}
}

func TestConvert_ConstKey_CriteriaInTwoLocationsBlocksMetric(t *testing.T) {
	raw := `{"type":"user_warehouse","name":"Pro Checkouts","id":"Pro Checkouts::user_warehouse","directionality":"increase",
	  "metricEvents":[{"name":"checkout","type":"count","criteria":[{"type":"metadata","column":"plan","condition":"in","values":["pro"]}]}],
	  "warehouseNative":{"aggregation":"count","metricSourceName":"Checkout Events"}}`
	res := mustConvert(t, raw, constKeyOpts())
	if !res.IsBlocked() {
		t.Errorf("criteria left on metricEvents are not applied, so the metric would count every row; blocking=%v warnings=%v", res.BlockingReasons, res.Warnings)
	}
}

func TestConvert_ConstKey_RatioDenominatorFilterBlocksMetric(t *testing.T) {
	raw := `{"type":"user_warehouse","name":"Orders per Pro View","id":"Orders per Pro View::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"ratio",
	    "metricSourceName":"Checkout Events","valueColumn":"order_total","numeratorAggregation":"sum",
	    "denominatorMetricSourceName":"Page Views","denominatorAggregation":"count",
	    "denominatorCriteria":[{"type":"user","column":"plan","condition":"in","values":["pro"]}]}}`
	res := mustConvert(t, raw, constKeyOpts())
	if !res.IsBlocked() {
		t.Fatalf("an unconverted denominator filter on a constant-key source must block; warnings=%v", res.Warnings)
	}
	if len(res.BlockingReasons) != 1 {
		t.Errorf("only the denominator term should block, got %v", res.BlockingReasons)
	}
}
