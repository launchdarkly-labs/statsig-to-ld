package cmd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/converter"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/report"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/warehouse"
)

func TestFetchDataSourceKeys_ReadsEventPropertyColumns(t *testing.T) {
	list, _ := json.Marshal(map[string]any{"items": []any{
		map[string]any{"key": "file-events", "sqlQuery": "SELECT * FROM file_events", "integrationKey": "snowflake-experimentation",
			"columnMappings": map[string]any{"timestampColumn": "TS", "contexts": map[string]any{"user": "USER_ID"}, "columns": []any{
				map[string]any{"name": "TS", "type": "TIMESTAMP_NTZ"},
				map[string]any{"name": "USER_ID", "type": "TEXT"},
				map[string]any{"name": "DEVICE", "type": "TEXT"},
				map[string]any{"name": "SEATS", "type": "FIXED", "precision": 38, "scale": 0},
			}}},
		map[string]any{"key": "no-columns", "sqlQuery": "SELECT * FROM t", "integrationKey": "snowflake-experimentation",
			"columnMappings": map[string]any{"timestampColumn": "TS"}},
	}})
	srv := dataSourceListServer(t, http.StatusOK, string(list))
	defer srv.Close()
	ld := launchdarkly.NewClient("api-x", "proj", srv.URL)
	info, err := fetchDataSourceKeys(context.Background(), ld, map[string]string{"A": "file-events", "B": "no-columns"}, "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []warehouse.EventPropertyColumn{
		{Name: "TS", Unsupported: "it is the timestamp column"},
		{Name: "USER_ID", Unsupported: "it is the user key column"},
		{Name: "DEVICE"},
		{Name: "SEATS"},
	}
	if got := info.eventProps["file-events"]; !reflect.DeepEqual(got, want) {
		t.Errorf("eventProps[file-events] = %+v, want %+v", got, want)
	}
	if cols, listed := info.eventProps["no-columns"]; !listed || cols != nil {
		t.Errorf("eventProps[no-columns] = %v, %v; want listed with none", cols, listed)
	}
	var opts converter.Options
	info.apply(&opts)
	if !reflect.DeepEqual(opts.DataSourceEventPropertyColumns, info.eventProps) {
		t.Errorf("apply set DataSourceEventPropertyColumns = %+v", opts.DataSourceEventPropertyColumns)
	}
}

func dimensionWHNMetric(t *testing.T, name string) statsig.Metric {
	t.Helper()
	var m statsig.Metric
	raw := `{"type":"user_warehouse","name":"` + name + `","id":"` + name + `::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"sum","metricSourceName":"File Events","valueColumn":"edits",
	    "metricDimensionColumns":["device","plan_tier"]}}`
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func eventPropertyConvOpts() converter.Options {
	return converter.Options{
		LDDataSource: "file-events",
		DataSourceEventPropertyColumns: map[string][]warehouse.EventPropertyColumn{
			"file-events": {{Name: "DEVICE"}, {Name: "PLAN_TIER"}},
		},
	}
}

const notEnabledBody = `{"code":"invalid_input","message":"adding ` + "`slicedEventPropertyColumns`" + ` is not enabled"}`

// metricCreateServer records each metric create's event properties and answers with respond.
func metricCreateServer(t *testing.T, respond func(cols []string) (int, string)) (*httptest.Server, func() [][]string) {
	var mu sync.Mutex
	var posts [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/metrics/proj" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body launchdarkly.MetricPost
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("bad body %s: %v", raw, err)
		}
		mu.Lock()
		posts = append(posts, body.SlicedEventPropertyColumns)
		mu.Unlock()
		status, resp := respond(body.SlicedEventPropertyColumns)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, resp)
	}))
	t.Cleanup(srv.Close)
	return srv, func() [][]string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(posts)
	}
}

func TestProcessMetric_SendsEventProperties(t *testing.T) {
	srv, posts := metricCreateServer(t, func([]string) (int, string) { return http.StatusCreated, `{"key":"edits-user-warehouse"}` })
	rpt := report.New()
	ld := launchdarkly.NewClient("api-x", "proj", srv.URL)
	processMetric(context.Background(), dimensionWHNMetric(t, "Edits"), eventPropertyConvOpts(), ld, rpt, "proj", false, 1, 1, new(convertTally))

	if got := posts(); !reflect.DeepEqual(got, [][]string{{"DEVICE", "PLAN_TIER"}}) {
		t.Errorf("creates sent event properties %v", got)
	}
	if len(rpt.Metrics) != 1 || rpt.Metrics[0].Status != report.StatusConverted {
		t.Fatalf("report = %+v, want one converted entry", rpt.Metrics)
	}
	if got := rpt.Metrics[0].EventPropertyColumns; !slices.Equal(got, []string{"DEVICE", "PLAN_TIER"}) {
		t.Errorf("report event_property_columns = %v", got)
	}
}

// An account without event properties gets its metrics anyway, and only the first
// create spends a request finding out.
func TestProcessMetric_EventPropertiesNotEnabledCreatesWithoutThem(t *testing.T) {
	srv, posts := metricCreateServer(t, func(cols []string) (int, string) {
		if len(cols) > 0 {
			return http.StatusBadRequest, notEnabledBody
		}
		return http.StatusCreated, `{"key":"k"}`
	})
	rpt := report.New()
	ld := launchdarkly.NewClient("api-x", "proj", srv.URL)
	var tally convertTally
	processMetric(context.Background(), dimensionWHNMetric(t, "Edits"), eventPropertyConvOpts(), ld, rpt, "proj", false, 1, 2, &tally)
	processMetric(context.Background(), dimensionWHNMetric(t, "Saves"), eventPropertyConvOpts(), ld, rpt, "proj", false, 2, 2, &tally)

	if got, want := posts(), [][]string{{"DEVICE", "PLAN_TIER"}, nil, nil}; !reflect.DeepEqual(got, want) {
		t.Errorf("creates sent event properties %v, want %v", got, want)
	}
	if n := tally.withoutEventProperties.Load(); n != 2 {
		t.Errorf("withoutEventProperties = %d, want 2", n)
	}
	if len(rpt.Metrics) != 2 {
		t.Fatalf("report = %+v", rpt.Metrics)
	}
	for _, m := range rpt.Metrics {
		if m.Status != report.StatusConverted || len(m.EventPropertyColumns) != 0 ||
			!slices.Contains(m.WarningCodes, converter.WarnEventPropertiesNotEnabled) || len(m.Warnings) != len(m.WarningCodes) {
			t.Errorf("entry = %+v, want converted without event properties and coded %s", m, converter.WarnEventPropertiesNotEnabled)
		}
	}
	if !strings.Contains(rpt.Metrics[0].Warnings[slices.Index(rpt.Metrics[0].WarningCodes, converter.WarnEventPropertiesNotEnabled)], `"DEVICE", "PLAN_TIER"`) {
		t.Errorf("warnings = %v, want the dropped columns named", rpt.Metrics[0].Warnings)
	}
}

// Any other refusal of the columns is about that metric, so each such metric is retried
// without them, and later metrics still send theirs.
func TestProcessMetric_RejectedEventPropertiesCreateWithoutThem(t *testing.T) {
	srv, posts := metricCreateServer(t, func(cols []string) (int, string) {
		if len(cols) > 1 {
			return http.StatusBadRequest, `{"code":"invalid_input","message":"` + "`slicedEventPropertyColumns`" + ` can have at most 1 columns"}`
		}
		return http.StatusCreated, `{"key":"k"}`
	})
	rpt := report.New()
	ld := launchdarkly.NewClient("api-x", "proj", srv.URL)
	var tally convertTally
	processMetric(context.Background(), dimensionWHNMetric(t, "Edits"), eventPropertyConvOpts(), ld, rpt, "proj", false, 1, 2, &tally)
	processMetric(context.Background(), dimensionWHNMetric(t, "Saves"), eventPropertyConvOpts(), ld, rpt, "proj", false, 2, 2, &tally)

	if got, want := posts(), [][]string{{"DEVICE", "PLAN_TIER"}, nil, {"DEVICE", "PLAN_TIER"}, nil}; !reflect.DeepEqual(got, want) {
		t.Errorf("creates sent event properties %v, want %v", got, want)
	}
	if tally.eventPropertiesRefused.Load() || tally.withoutEventProperties.Load() != 0 || tally.eventPropertiesRejected.Load() != 2 {
		t.Errorf("tally = refused %v, without %d, rejected %d; want false, 0, 2",
			tally.eventPropertiesRefused.Load(), tally.withoutEventProperties.Load(), tally.eventPropertiesRejected.Load())
	}
	if len(rpt.Metrics) != 2 {
		t.Fatalf("report = %+v", rpt.Metrics)
	}
	for _, m := range rpt.Metrics {
		if m.Status != report.StatusConverted || len(m.EventPropertyColumns) != 0 ||
			!slices.Contains(m.WarningCodes, converter.WarnEventPropertiesRejected) || len(m.Warnings) != len(m.WarningCodes) {
			t.Errorf("entry = %+v, want converted without event properties and coded %s", m, converter.WarnEventPropertiesRejected)
		}
	}
	if w := rpt.Metrics[0].Warnings[slices.Index(rpt.Metrics[0].WarningCodes, converter.WarnEventPropertiesRejected)]; !strings.Contains(w, "can have at most 1 columns") {
		t.Errorf("warning = %q, want LaunchDarkly's reason", w)
	}
}

func TestProcessMetric_OtherCreateFailuresAreNotRetried(t *testing.T) {
	srv, posts := metricCreateServer(t, func([]string) (int, string) {
		return http.StatusBadRequest, `{"code":"invalid_input","message":"Randomization unit \"stableid\" not found in project settings [user]"}`
	})
	rpt := report.New()
	ld := launchdarkly.NewClient("api-x", "proj", srv.URL)
	var tally convertTally
	processMetric(context.Background(), dimensionWHNMetric(t, "Edits"), eventPropertyConvOpts(), ld, rpt, "proj", false, 1, 1, &tally)

	if got := len(posts()); got != 1 {
		t.Errorf("%d creates, want 1", got)
	}
	if len(rpt.Metrics) != 1 || rpt.Metrics[0].Status != report.StatusFailed {
		t.Errorf("report = %+v, want one failed entry", rpt.Metrics)
	}
	if tally.eventPropertiesRefused.Load() || tally.withoutEventProperties.Load() != 0 || tally.eventPropertiesRejected.Load() != 0 {
		t.Errorf("tally = %+v, want untouched", &tally)
	}
}

// LaunchDarkly checks the columns before the key, so a metric that already exists is
// refused first and found to exist on the retry.
func TestProcessMetric_RefusedThenExistingIsSkipped(t *testing.T) {
	srv, posts := metricCreateServer(t, func(cols []string) (int, string) {
		if len(cols) > 0 {
			return http.StatusBadRequest, notEnabledBody
		}
		return http.StatusConflict, `{"code":"conflict","message":"Duplicate metric key"}`
	})
	rpt := report.New()
	ld := launchdarkly.NewClient("api-x", "proj", srv.URL)
	var tally convertTally
	processMetric(context.Background(), dimensionWHNMetric(t, "Edits"), eventPropertyConvOpts(), ld, rpt, "proj", false, 1, 1, &tally)

	if got := len(posts()); got != 2 {
		t.Errorf("%d creates, want 2", got)
	}
	if len(rpt.Metrics) != 1 || rpt.Metrics[0].Status != report.StatusSkippedExisting {
		t.Errorf("report = %+v, want one skipped_existing entry", rpt.Metrics)
	}
	if !tally.eventPropertiesRefused.Load() || tally.withoutEventProperties.Load() != 0 {
		t.Errorf("tally = refused %v, without %d; want true, 0", tally.eventPropertiesRefused.Load(), tally.withoutEventProperties.Load())
	}
}

func TestConvertCmd_MaxEventPropertiesDefaultsToLaunchDarklysLimit(t *testing.T) {
	if got := convertCmd.Flags().Lookup("max-event-properties").DefValue; got != "10" {
		t.Errorf("--max-event-properties defaults to %s, want 10, LaunchDarkly's default limit", got)
	}
}

func TestProcessMetric_DryRunReportsEventProperties(t *testing.T) {
	rpt := report.New()
	processMetric(context.Background(), dimensionWHNMetric(t, "Edits"), eventPropertyConvOpts(), nil, rpt, "proj", true, 1, 1, new(convertTally))
	if len(rpt.Metrics) != 1 || !slices.Equal(rpt.Metrics[0].EventPropertyColumns, []string{"DEVICE", "PLAN_TIER"}) {
		t.Errorf("report = %+v, want event_property_columns DEVICE, PLAN_TIER", rpt.Metrics)
	}
}

func TestRunConvert_MaxEventPropertiesMustBePositive(t *testing.T) {
	t.Setenv("STATSIG_CONSOLE_KEY", "")
	t.Setenv("LD_API_KEY", "")
	t.Setenv("LD_PROJECT", "")
	oldAll, oldMax := flagAll, flagMaxEventProps
	t.Cleanup(func() { flagAll, flagMaxEventProps = oldAll, oldMax })
	flagAll, flagMaxEventProps = true, 0

	err := runConvert(convertCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--max-event-properties") {
		t.Errorf("runConvert = %v, want a --max-event-properties error", err)
	}
}
