package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/jsonutil"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/output"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/warehouse"
)

// --ld-maintainer-mapping names a maintainer per Statsig source. --update-maintainers
// moves existing data sources to those, and gives unmaintained ones the run's maintainer.

const noteMaintainerUpdated = "maintainer_updated"

// loadMaintainerMapping reads a JSON object of Statsig source names to an email, a
// member ID, or "none".
func loadMaintainerMapping(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("--ld-maintainer-mapping: %w", err)
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("--ld-maintainer-mapping %s must be a JSON object mapping Statsig metric source names to an email, a 24-character member ID, or %q: %w", path, launchdarkly.MaintainerNone, err)
	}
	var bad []string
	for _, name := range slices.Sorted(maps.Keys(m)) {
		if v := strings.TrimSpace(m[name]); !launchdarkly.IsMaintainerValue(v) {
			bad = append(bad, fmt.Sprintf("%q: %q", name, m[name]))
		}
	}
	if len(bad) > 0 {
		return nil, fmt.Errorf("--ld-maintainer-mapping %s has values that are not an email, a 24-character member ID, or %q: %s", path, launchdarkly.MaintainerNone, strings.Join(bad, ", "))
	}
	return m, nil
}

// checkMaintainerMapping fails when the mapping names a source the export does not have.
func (e *migrationEngine) checkMaintainerMapping() error {
	names := map[string]bool{}
	for _, source := range e.metricSources {
		names[jsonutil.GetStr(source, "name")] = true
	}
	var unknown []string
	for _, name := range slices.Sorted(maps.Keys(e.maintainerMapping)) {
		if !names[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("--ld-maintainer-mapping names Statsig metric sources the export does not have: %s. Use the source names exactly as in source-mapping.json", quoteAll(unknown))
	}
	return nil
}

// resolveMappedMaintainers resolves each distinct mapping value once.
func (e *migrationEngine) resolveMappedMaintainers() error {
	byValue := map[string]launchdarkly.Maintainer{}
	e.maintainers = map[string]launchdarkly.Maintainer{}
	for _, name := range slices.Sorted(maps.Keys(e.maintainerMapping)) {
		v := strings.TrimSpace(e.maintainerMapping[name])
		m, ok := byValue[v]
		if !ok {
			var err error
			if m, err = e.ld.ResolveMappedMaintainer(e.ctx, v); err != nil {
				return fmt.Errorf("maintainer for Statsig source %q: %w", name, err)
			}
			byValue[v] = m
		}
		e.maintainers[name] = m
	}
	return nil
}

// resolveMaintainers resolves the mapping and, with withDefault, the run's maintainer.
func (e *migrationEngine) resolveMaintainers(withDefault bool) error {
	if withDefault {
		m, err := e.ld.ResolveMaintainer(e.ctx, whFlagLDMaintainer)
		if err != nil {
			return err
		}
		e.defaultMaintainer, e.maintainerID = m, m.MemberID
	}
	if err := e.resolveMappedMaintainers(); err != nil {
		return err
	}
	e.maintainersResolved = true
	return nil
}

// maintainerFor is the maintainer a Statsig source's data source is created with.
func (e *migrationEngine) maintainerFor(source map[string]any) string {
	if m, ok := e.maintainers[jsonutil.GetStr(source, "name")]; ok {
		return m.MemberID
	}
	return e.maintainerID
}

// maintainerChange is an existing data source whose maintainer --update-maintainers changes.
type maintainerChange struct {
	key    string
	ops    []launchdarkly.JSONPatchOp
	change string
}

// maintainerChanges lists the existing data sources in this environment whose
// maintainer differs from the one the mapping names, and, unless onlyMapped, the
// unmaintained ones the run's maintainer would get. A maintainer set in LaunchDarkly
// is never replaced by the run's.
func (e *migrationEngine) maintainerChanges(existing map[string]map[string]any, collisions map[string][]string, onlyMapped bool) []maintainerChange {
	var changes []maintainerChange
	for _, source := range e.metricSources {
		name := jsonutil.GetStr(source, "name")
		key := warehouse.SanitizeKey(name)
		ds := existing[key]
		if name == "" || ds == nil || collisions[key] != nil {
			continue
		}
		if env := jsonutil.GetStr(ds, "environmentKey"); env != "" && env != e.environmentKey {
			continue
		}
		current := jsonutil.GetStr(ds, "maintainerId")
		to, mapped := e.maintainers[name]
		if !mapped {
			if current != "" || onlyMapped {
				continue
			}
			to = e.defaultMaintainer
		}
		if current == to.MemberID {
			continue
		}
		var ops []launchdarkly.JSONPatchOp
		if current != "" {
			ops = append(ops, launchdarkly.JSONPatchOp{Op: "test", Path: "/maintainerId", Value: current})
		}
		if to.MemberID == "" {
			ops = append(ops, launchdarkly.JSONPatchOp{Op: "remove", Path: "/maintainerId"})
		} else {
			ops = append(ops, launchdarkly.JSONPatchOp{Op: "add", Path: "/maintainerId", Value: to.MemberID})
		}
		from := orEmail(current, jsonutil.GetStr(jsonutil.GetMap(ds, "maintainer"), "email"))
		changes = append(changes, maintainerChange{key: key, ops: ops, change: mappingChange("maintainer", from, orEmail(to.MemberID, to.Email))})
	}
	return changes
}

func orEmail(id, email string) string {
	if email != "" {
		return email
	}
	return id
}

// syncMaintainers applies --update-maintainers, or says how many mapped maintainers it would change.
func (e *migrationEngine) syncMaintainers(existing map[string]map[string]any, collisions map[string][]string) {
	if !e.updateMaintainers {
		if msg := maintainerMappingSummary(len(e.maintainerChanges(existing, collisions, true))); msg != "" {
			e.warn(msg)
		}
		return
	}
	changes := e.maintainerChanges(existing, collisions, false)
	if len(changes) == 0 {
		return
	}
	output.Info(fmt.Sprintf("Updating the maintainer of %d existing data source(s)...", len(changes)))
	for _, c := range changes {
		if _, err := e.ld.UpdateMetricDataSource(e.ctx, c.key, c.ops); err != nil {
			if errors.Is(err, launchdarkly.ErrPatchNotApplied) {
				err = fmt.Errorf("its maintainer changed in LaunchDarkly after this run read it; rerun to pick up the change")
			}
			output.ErrMsg(fmt.Sprintf("Data source %q: maintainer not updated: %v", c.key, err))
			e.state.AddError("data_source", c.key, err.Error())
			e.report.Errors = append(e.report.Errors, fmt.Sprintf("Data source %q: maintainer not updated: %v", c.key, err))
			continue
		}
		e.report.DataSources.MaintainersUpdated++
		e.report.Notes = append(e.report.Notes, reportNote{Code: noteMaintainerUpdated, DataSource: c.key, Changes: []string{c.change}})
	}
}

func maintainerMappingSummary(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d existing data source(s) have a maintainer other than --ld-maintainer-mapping names for them. Rerun with --update-maintainers to change them.", n)
}
