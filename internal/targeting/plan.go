package targeting

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/flag"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
)

// overrideFetchConcurrency caps parallel override GETs against the Statsig
// Console API. 10 workers matches the goaltender lambda's setting.
const overrideFetchConcurrency = 10

// Plan holds the data needed to apply per-environment targeting to a set of
// flag shells already created by `flags import`. Built once via BuildPlan;
// consumed by Apply.
type Plan struct {
	Reconciler *EnvReconciler

	// Source data keyed by sanitized LD flag key. Only one is populated per
	// import (gates XOR dynamic configs), determined by the cmd's
	// --import-type flag.
	rawGates      map[string]statsig.Gate
	rawConfigs    map[string]statsig.DynamicConfig
	gateOverrides map[string][]statsig.Override
	dcOverrides   map[string][]statsig.Override

	// Notes accumulated during plan construction (env reconciliation, override
	// fetch failures). Surfaced alongside Apply's per-flag notes.
	BuildNotes []Note
}

// PlanInputs is the bundle of data BuildPlan needs. Caller is responsible for
// any --include-tag / --accept-data-loss filtering before passing the slices.
type PlanInputs struct {
	Gates          []statsig.Gate
	DynamicConfigs []statsig.DynamicConfig
	LDTag          string // applied to auto-created envs
	NoCreateEnvs   bool   // when true, don't auto-create missing LD envs
}

// statsigClient is the subset of statsig.Client the planner needs. Interface
// for test mockability.
type statsigClient interface {
	envReconcilerStatsigLister
	GetGateOverrides(ctx context.Context, gateID string) ([]statsig.Override, error)
	GetDynamicConfigOverrides(ctx context.Context, configID string) ([]statsig.Override, error)
}

// BuildPlan reconciles environments and fetches per-source overrides in
// parallel. Returns a Plan ready for Apply.
func BuildPlan(
	ctx context.Context,
	sgClient statsigClient,
	ldClient envReconcilerLDClient,
	inputs PlanInputs,
) (*Plan, error) {
	reconciler := NewEnvReconciler(ldClient, sgClient, inputs.LDTag, !inputs.NoCreateEnvs)
	if err := reconciler.Reconcile(ctx); err != nil {
		return nil, fmt.Errorf("env reconciliation: %w", err)
	}

	plan := &Plan{
		Reconciler:    reconciler,
		rawGates:      map[string]statsig.Gate{},
		rawConfigs:    map[string]statsig.DynamicConfig{},
		gateOverrides: map[string][]statsig.Override{},
		dcOverrides:   map[string][]statsig.Override{},
		BuildNotes:    reconciler.Notes(),
	}

	if len(inputs.Gates) > 0 {
		for _, g := range inputs.Gates {
			plan.rawGates[flag.SanitizeKey(g.ID)] = g
		}
		ids := make([]string, len(inputs.Gates))
		for i, g := range inputs.Gates {
			ids[i] = g.ID
		}
		results, notes := fetchOverridesParallel(ctx, ids, overrideFetchConcurrency, sgClient.GetGateOverrides)
		for id, overrides := range results {
			plan.gateOverrides[flag.SanitizeKey(id)] = overrides
		}
		plan.BuildNotes = append(plan.BuildNotes, notes...)
	}

	if len(inputs.DynamicConfigs) > 0 {
		for _, c := range inputs.DynamicConfigs {
			plan.rawConfigs[flag.SanitizeKey(c.ID)] = c
		}
		ids := make([]string, len(inputs.DynamicConfigs))
		for i, c := range inputs.DynamicConfigs {
			ids[i] = c.ID
		}
		results, notes := fetchOverridesParallel(ctx, ids, overrideFetchConcurrency, sgClient.GetDynamicConfigOverrides)
		for id, overrides := range results {
			plan.dcOverrides[flag.SanitizeKey(id)] = overrides
		}
		plan.BuildNotes = append(plan.BuildNotes, notes...)
	}

	return plan, nil
}

// ldPatchClient is the LD client surface Apply needs.
type ldPatchClient interface {
	PatchFlag(ctx context.Context, flagKey string, ops []launchdarkly.JSONPatchOp) error
}

// ApplyResult is the per-flag outcome of Apply. Status is "applied",
// "skipped_no_source" (flag tagged but no corresponding Statsig source),
// "skipped_dry_run", or "failed".
type ApplyResult struct {
	FlagKey string
	Status  string
	Notes   []Note
	Error   string
}

const (
	StatusApplied         = "applied"
	StatusSkippedNoSource = "skipped_no_source"
	StatusSkippedDryRun   = "skipped_dry_run"
	StatusFailed          = "failed"
)

// Apply iterates the provided LD flags and PATCHes per-env targeting for each
// one whose key matches a source in the plan. dryRun=true skips the HTTP
// PATCH but still records the build outcome.
func (p *Plan) Apply(ctx context.Context, ld ldPatchClient, flags []launchdarkly.Flag, dryRun bool) []ApplyResult {
	results := make([]ApplyResult, 0, len(flags))

	for _, f := range flags {
		settingsByEnv, notes, ok := p.buildFlagSettings(f)
		if !ok {
			results = append(results, ApplyResult{
				FlagKey: f.Key,
				Status:  StatusSkippedNoSource,
				Notes:   notes,
			})
			continue
		}

		// Iterate envs in sorted order so emitted op arrays are stable
		// across runs (helps debugging, tests, audit logs).
		envKeys := make([]string, 0, len(settingsByEnv))
		for k := range settingsByEnv {
			envKeys = append(envKeys, k)
		}
		sort.Strings(envKeys)

		var allOps []launchdarkly.JSONPatchOp
		for _, k := range envKeys {
			allOps = append(allOps, BuildEnvPatchOps(k, settingsByEnv[k])...)
		}
		if len(allOps) == 0 {
			results = append(results, ApplyResult{
				FlagKey: f.Key,
				Status:  StatusApplied,
				Notes:   notes,
			})
			continue
		}

		if dryRun {
			results = append(results, ApplyResult{
				FlagKey: f.Key,
				Status:  StatusSkippedDryRun,
				Notes:   notes,
			})
			continue
		}

		if err := ld.PatchFlag(ctx, f.Key, allOps); err != nil {
			results = append(results, ApplyResult{
				FlagKey: f.Key,
				Status:  StatusFailed,
				Notes:   notes,
				Error:   err.Error(),
			})
			continue
		}
		results = append(results, ApplyResult{
			FlagKey: f.Key,
			Status:  StatusApplied,
			Notes:   notes,
		})
	}

	return results
}

// buildFlagSettings dispatches to BuildGateEnvSettings or BuildDCEnvSettings
// based on which source map the flag's key appears in. Returns ok=false when
// neither matches (flag was tagged but its source is no longer in Statsig).
func (p *Plan) buildFlagSettings(f launchdarkly.Flag) (map[string]EnvSettings, []Note, bool) {
	if gate, ok := p.rawGates[f.Key]; ok {
		settings, notes := BuildGateEnvSettings(gate, p.gateOverrides[f.Key], p.Reconciler)
		return settings, notes, true
	}
	if dc, ok := p.rawConfigs[f.Key]; ok {
		settings, notes := BuildDCEnvSettings(dc, f, p.dcOverrides[f.Key], p.Reconciler)
		return settings, notes, true
	}
	return nil, nil, false
}

// ============================================================================
// Parallel override fetch
// ============================================================================

func fetchOverridesParallel(
	ctx context.Context,
	ids []string,
	concurrency int,
	fetch func(ctx context.Context, id string) ([]statsig.Override, error),
) (map[string][]statsig.Override, []Note) {
	if len(ids) == 0 {
		return map[string][]statsig.Override{}, nil
	}
	if concurrency <= 0 {
		concurrency = 1
	}

	type result struct {
		id        string
		overrides []statsig.Override
		err       error
	}
	jobs := make(chan string, len(ids))
	results := make(chan result, len(ids))
	var wg sync.WaitGroup
	workers := concurrency
	if workers > len(ids) {
		workers = len(ids)
	}
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				o, err := fetch(ctx, id)
				results <- result{id: id, overrides: o, err: err}
			}
		}()
	}
	for _, id := range ids {
		jobs <- id
	}
	close(jobs)
	wg.Wait()
	close(results)

	out := make(map[string][]statsig.Override, len(ids))
	var notes []Note
	for r := range results {
		if r.err != nil {
			notes = append(notes, newWarning(r.id,
				"failed to fetch overrides for %s: %v (targeting for this flag will proceed without overrides)", r.id, r.err))
			continue
		}
		out[r.id] = r.overrides
	}
	return out, notes
}
