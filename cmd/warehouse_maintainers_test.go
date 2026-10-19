package cmd

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
)

const (
	memberA = "aaaaaaaaaaaaaaaaaaaaaaaa"
	memberB = "bbbbbbbbbbbbbbbbbbbbbbbb"
	memberC = "cccccccccccccccccccccccc"
	memberD = "dddddddddddddddddddddddd"
)

const testMembers = `[{"_id":"aaaaaaaaaaaaaaaaaaaaaaaa","email":"a@example.com"},{"_id":"bbbbbbbbbbbbbbbbbbbbbbbb","email":"b@example.com"},
	{"_id":"cccccccccccccccccccccccc","email":"c@example.com"},{"_id":"dddddddddddddddddddddddd","email":"d@example.com"}]`

// maintainedList holds unwrapped data sources: checkout-events maintained by a@, orders
// with no maintainer, and refunds maintained by b@.
const maintainedList = `{"items":[
	{"key":"checkout-events","environmentKey":"production","sqlQuery":"SELECT 1","maintainerId":"aaaaaaaaaaaaaaaaaaaaaaaa","maintainer":{"email":"a@example.com"},"columnMappings":{"keyColumn":"EVENT_NAME","timestampColumn":"TS","contexts":{"user":"USER_ID"}}},
	{"key":"orders","environmentKey":"production","sqlQuery":"SELECT 1","columnMappings":{"keyColumn":"EVENT_NAME","timestampColumn":"TS","contexts":{"user":"USER_ID"}}},
	{"key":"refunds","environmentKey":"production","sqlQuery":"SELECT 1","maintainerId":"bbbbbbbbbbbbbbbbbbbbbbbb","maintainer":{"email":"b@example.com"},"columnMappings":{"keyColumn":"EVENT_NAME","timestampColumn":"TS","contexts":{"user":"USER_ID"}}}]}`

func maintainedSources() []map[string]any {
	named := func(name string) map[string]any {
		s := checkoutSource()
		s["name"] = name
		return s
	}
	return []map[string]any{checkoutSource(), named("Orders"), named("Refunds"), named("Page Views")}
}

func runMaintainers(t *testing.T, f *fakeLD, mapping map[string]string, update bool) *migrationEngine {
	t.Helper()
	t.Chdir(t.TempDir())
	f.members, f.callerMemberID = testMembers, memberD
	srv := f.server()
	t.Cleanup(srv.Close)
	e := newPhase3Engine(t, srv.URL, false)
	e.metricSources = maintainedSources()
	e.maintainerMapping, e.updateMaintainers = mapping, update
	if err := e.resolveMaintainers(true); err != nil {
		t.Fatal(err)
	}
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestUpdateMaintainers_SetsMappedAndUnmaintainedDataSources(t *testing.T) {
	f := &fakeLD{t: t, list: maintainedList}
	e := runMaintainers(t, f, map[string]string{"Checkout Events": "c@example.com", "Refunds": "none", "Page Views": "none"}, true)

	for key, want := range map[string]string{
		"checkout-events": "test /maintainerId\nadd /maintainerId \"" + memberC + "\"",
		"orders":          "test /maintainerId\nadd /maintainerId \"" + memberD + "\"",
		"refunds":         "test /maintainerId\nremove /maintainerId",
	} {
		if got := opLines(f.patches[key]); got != want {
			t.Errorf("%s ops:\n%s\nwant:\n%s", key, got, want)
		}
	}
	// orders tests that it still has no maintainer.
	if f.patches["checkout-events"][0].Value != memberA || f.patches["refunds"][0].Value != memberB || f.patches["orders"][0].Value != nil {
		t.Errorf("test values = %v, %v, %v, want the listed maintainers", f.patches["checkout-events"][0].Value, f.patches["refunds"][0].Value, f.patches["orders"][0].Value)
	}
	if len(f.created) != 1 || f.created[0]["key"] != "page-views" || f.created[0]["maintainerId"] != nil {
		t.Errorf("created = %v, want page-views with no maintainer", f.created)
	}
	var notes []reportNote
	for _, n := range e.report.Notes {
		if n.Code == noteMaintainerUpdated {
			notes = append(notes, n)
		}
	}
	want := []reportNote{
		{Code: noteMaintainerUpdated, DataSource: "checkout-events", Changes: []string{"maintainer: a@example.com → c@example.com"}},
		{Code: noteMaintainerUpdated, DataSource: "orders", Changes: []string{"maintainer: (none) → d@example.com"}},
		{Code: noteMaintainerUpdated, DataSource: "refunds", Changes: []string{"maintainer: b@example.com → (none)"}},
	}
	if !reflect.DeepEqual(notes, want) || e.report.DataSources.MaintainersUpdated != 3 {
		t.Errorf("notes = %+v\nwant %+v\nmaintainers updated = %d", notes, want, e.report.DataSources.MaintainersUpdated)
	}
	// The run's maintainer by ID, and c@ by email; "none" needs no lookup.
	if f.memberReads != 2 {
		t.Errorf("member reads = %d, want 2", f.memberReads)
	}
}

// A maintainer set in LaunchDarkly is replaced only by a mapping entry.
func TestUpdateMaintainers_KeepsMaintainersTheMappingDoesNotName(t *testing.T) {
	f := &fakeLD{t: t, list: maintainedList}
	runMaintainers(t, f, nil, true)
	if len(f.patches) != 1 || opLines(f.patches["orders"]) != "test /maintainerId\nadd /maintainerId \""+memberD+"\"" {
		t.Errorf("patches = %v, want only orders given the run's maintainer", f.patches)
	}
}

func TestUpdateMaintainers_WithoutTheFlagSaysHowManyMappedDiffer(t *testing.T) {
	f := &fakeLD{t: t, list: maintainedList}
	e := runMaintainers(t, f, map[string]string{"Checkout Events": "c@example.com", "Refunds": "b@example.com", "Page Views": "c@example.com"}, false)
	if f.patchCount != 0 || len(f.created) != 1 || f.created[0]["maintainerId"] != memberC {
		t.Errorf("patches=%d created=%v, want no patch and page-views created for c@", f.patchCount, f.created)
	}
	if !strings.Contains(strings.Join(e.report.Warnings, "\n"), maintainerMappingSummary(1)) {
		t.Errorf("warnings = %q, want %q", e.report.Warnings, maintainerMappingSummary(1))
	}
	var differs []reportNote
	for _, n := range e.report.Notes {
		if n.Code == noteMaintainerDiffers {
			differs = append(differs, n)
		}
	}
	if want := []reportNote{{Code: noteMaintainerDiffers, DataSource: "checkout-events", Changes: []string{"maintainer: a@example.com → c@example.com"}}}; !reflect.DeepEqual(differs, want) {
		t.Errorf("notes = %+v, want %+v", differs, want)
	}

	// No mapping, no line: the run's maintainer goes only on creates.
	f = &fakeLD{t: t, list: maintainedList}
	e = runMaintainers(t, f, nil, false)
	if f.patchCount != 0 || strings.Contains(strings.Join(e.report.Warnings, "\n"), "--update-maintainers") {
		t.Errorf("patches=%d warnings=%q", f.patchCount, e.report.Warnings)
	}
}

func TestUpdateMaintainers_PatchErrorsSayWhatToDo(t *testing.T) {
	f := &fakeLD{t: t, list: maintainedList, patchStatus: http.StatusBadRequest, patchBody: `{"code":"invalid_request","message":"Error applying json-patch document"}`}
	e := runMaintainers(t, f, map[string]string{"Checkout Events": "c@example.com"}, true)
	want := `Data source "Checkout Events": maintainer not updated: its maintainer changed in LaunchDarkly after this run read it; rerun to pick up the change`
	ds := e.report.DataSources
	if len(e.report.Errors) != 2 || e.report.Errors[0] != want || ds.MaintainersUpdated != 0 || ds.MaintainersFailed != 2 || ds.Failed != 0 {
		t.Errorf("errors=%q report %+v", e.report.Errors, ds)
	}
}

func TestUpdateMaintainers_DryRunListsTheChanges(t *testing.T) {
	t.Chdir(t.TempDir())
	f := &fakeLD{t: t, list: maintainedList, members: testMembers, callerMemberID: memberD}
	srv := f.server()
	defer srv.Close()
	e := newPhase3Engine(t, srv.URL, false)
	e.dryRun, e.ldReadable, e.updateMaintainers = true, true, true
	e.metricSources = maintainedSources()
	e.maintainerMapping = map[string]string{"Checkout Events": "c@example.com"}
	if err := e.resolveMaintainers(true); err != nil {
		t.Fatal(err)
	}
	l := e.dryRunDataSources("snowflake")
	want := []string{"checkout-events: maintainer: a@example.com → c@example.com", "orders: maintainer: (none) → d@example.com"}
	if !l.maintainersChecked || !reflect.DeepEqual(l.maintainers, want) || f.patchCount != 0 {
		t.Errorf("checked=%v maintainers=%q\nwant %q\npatches=%d", l.maintainersChecked, l.maintainers, want, f.patchCount)
	}

	e.ldReadable, e.maintainersResolved = false, false
	l = e.dryRunDataSources("snowflake")
	if l.maintainersChecked || !strings.Contains(strings.Join(l.notes, " "), "maintainers were not compared. Pass --ld-key") {
		t.Errorf("without credentials: checked=%v notes=%q", l.maintainersChecked, l.notes)
	}

	f.listStatus = http.StatusForbidden
	e.ldReadable, e.maintainersResolved = true, true
	l = e.dryRunDataSources("snowflake")
	if !strings.Contains(strings.Join(l.notes, " "), "maintainers were not compared, because the data sources could not be read") {
		t.Errorf("unreadable list: notes=%q", l.notes)
	}
}

func TestUpdateMaintainers_DryRunWithoutTheFlagCountsMappedChanges(t *testing.T) {
	t.Chdir(t.TempDir())
	f := &fakeLD{t: t, list: maintainedList, members: testMembers, callerMemberID: memberD}
	srv := f.server()
	defer srv.Close()
	e := newPhase3Engine(t, srv.URL, false)
	e.dryRun, e.ldReadable = true, true
	e.metricSources = maintainedSources()
	e.maintainerMapping = map[string]string{"Checkout Events": "c@example.com", "Orders": "c@example.com"}
	if err := e.resolveMaintainers(false); err != nil {
		t.Fatal(err)
	}
	l := e.dryRunDataSources("snowflake")
	if l.maintainersChecked || !slices.Contains(l.warnings, maintainerMappingSummary(2)) {
		t.Errorf("checked=%v warnings=%q", l.maintainersChecked, l.warnings)
	}
}

func TestMaintainerChanges_SkipOtherEnvironmentsAndKeyCollisions(t *testing.T) {
	t.Chdir(t.TempDir())
	list := strings.Replace(maintainedList, `"key":"orders","environmentKey":"production"`, `"key":"orders","environmentKey":"staging"`, 1)
	f := &fakeLD{t: t, list: list}
	srv := f.server()
	defer srv.Close()
	e := newPhase3Engine(t, srv.URL, false)
	sources := maintainedSources()
	twin := checkoutSource()
	twin["name"] = "Checkout  Events"
	e.metricSources = append(sources, twin)
	e.defaultMaintainer = launchdarkly.Maintainer{MemberID: memberD}
	e.maintainers = map[string]launchdarkly.Maintainer{"Checkout Events": {MemberID: memberC}, "Orders": {MemberID: memberC}}
	dsList, err := e.ld.ListMetricDataSources(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if changes := e.maintainerChanges(indexDataSources(dsList), keyCollisions(e.metricSources), false); len(changes) != 0 {
		t.Errorf("changes = %+v, want none: orders is in staging, and checkout-events collides", changes)
	}
}

// run() checks the mapping before it reads or writes anything else in LaunchDarkly.
func TestRun_ChecksTheMaintainerMappingFirst(t *testing.T) {
	export := func(t *testing.T) string {
		raw, _ := json.Marshal(map[string]any{"metric_sources": maintainedSources()})
		p := filepath.Join(t.TempDir(), "export.json")
		if err := os.WriteFile(p, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, tc := range []struct {
		name    string
		mapping map[string]string
		dryRun  bool
		want    string
	}{
		{"unknown source", map[string]string{"Gone": "none"}, false, `--ld-maintainer-mapping names Statsig metric sources the export does not have: "Gone"`},
		{"unknown member on a dry run", map[string]string{"Orders": "nobody@example.com"}, true, `maintainer for Statsig source "Orders": no LaunchDarkly member has the email "nobody@example.com"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			f := &fakeLD{t: t, list: maintainedList, members: testMembers, callerMemberID: memberD}
			srv := f.server()
			defer srv.Close()
			e := newPhase3Engine(t, srv.URL, false)
			e.exportFile, e.maintainerMapping, e.dryRun, e.ldReadable = export(t), tc.mapping, tc.dryRun, true
			err := e.run()
			if err == nil || !strings.HasPrefix(err.Error(), tc.want) || f.patchCount != 0 || len(f.created) != 0 || len(f.previews) != 0 {
				t.Errorf("err=%v patches=%d created=%d previews=%d", err, f.patchCount, len(f.created), len(f.previews))
			}
		})
	}
}

func TestResolveMaintainers_NamesTheSourceOfABadEntry(t *testing.T) {
	t.Chdir(t.TempDir())
	f := &fakeLD{t: t, members: testMembers, callerMemberID: memberD}
	srv := f.server()
	defer srv.Close()
	e := newPhase3Engine(t, srv.URL, false)
	e.maintainerMapping = map[string]string{"Checkout Events": "nobody@example.com"}
	err := e.resolveMaintainers(false)
	if err == nil || !strings.HasPrefix(err.Error(), `maintainer for Statsig source "Checkout Events": no LaunchDarkly member has the email "nobody@example.com"`) {
		t.Errorf("err = %v", err)
	}
}

func TestLoadMaintainerMapping(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "m.json")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	m, err := loadMaintainerMapping(write(`{"Checkout Events":"a@example.com","Orders":"` + memberB + `","Refunds":"none"}`))
	if err != nil || len(m) != 3 {
		t.Errorf("m=%v err=%v", m, err)
	}
	if _, err := loadMaintainerMapping(write(`["a@example.com"]`)); err == nil || !strings.Contains(err.Error(), "must be a JSON object") {
		t.Errorf("array: err = %v", err)
	}
	_, err = loadMaintainerMapping(write(`{"":"none","Orders":"bob","Pages":"a@example.com|b@example.com","Refunds":""}`))
	if err == nil || !strings.Contains(err.Error(), `"": "none", "Orders": "bob", "Pages": "a@example.com|b@example.com", "Refunds": ""`) {
		t.Errorf("bad entries: err = %v", err)
	}
	if _, err := loadMaintainerMapping(write(`null`)); err == nil || !strings.Contains(err.Error(), "must be a JSON object") {
		t.Errorf("null: err = %v", err)
	}
}

func TestCheckMaintainerMapping_NamesUnknownSources(t *testing.T) {
	e := &migrationEngine{metricSources: maintainedSources(), maintainerMapping: map[string]string{"Orders": "none", "orders": "none", "Gone": "none"}}
	err := e.checkMaintainerMapping()
	if err == nil || !strings.HasPrefix(err.Error(), `--ld-maintainer-mapping names Statsig metric sources the export does not have: "Gone", "orders"`) {
		t.Errorf("err = %v", err)
	}
}
