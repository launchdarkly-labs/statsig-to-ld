// Statsig targeting orchestration: reconcile environments, fetch overrides in
// parallel, apply per-flag targeting via JSON Patch. Ported from goaltender
// flag_import_worker/statsig_targeting.go (PR #829), adapted for CLI use
// (concrete clients instead of interfaces).
package converter

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"

	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/statsig"
)

// ImportKind identifies whether the import targets feature gates or dynamic
// configs. Gates and DCs use different targeting builders, override endpoints,
// and variation conventions.
type ImportKind string

const (
	ImportKindGates          ImportKind = "feature-gates"
	ImportKindDynamicConfigs ImportKind = "dynamic-configs"
)

// overrideFetchConcurrency caps parallel override GETs against the Statsig
// API. 10 workers is conservative against Statsig's published rate limits.
const overrideFetchConcurrency = 10

// TargetingPlan holds the data needed to apply per-environment targeting
// after the flag shells have been created. Built once per import.
type TargetingPlan struct {
	Kind          ImportKind
	Reconciler    *EnvReconciler
	rawGates      map[string]statsig.Gate          // keyed by sanitized LD flag key
	rawConfigs    map[string]statsig.DynamicConfig // keyed by sanitized LD flag key
	gateOverrides map[string][]statsig.Override    // keyed by sanitized LD flag key
	dcOverrides   map[string][]statsig.Override    // keyed by sanitized LD flag key
}

// BuildTargetingPlan reconciles environments and fetches per-flag overrides
// for the targeting-import step. Takes the already-filtered gates/configs
// from the calling command so we don't double-fetch.
func BuildTargetingPlan(
	ctx context.Context,
	sgClient *statsig.Client,
	ldClient *launchdarkly.Client,
	kind ImportKind,
	tag string,
	gates []statsig.Gate,
	configs []statsig.DynamicConfig,
) (*TargetingPlan, error) {
	reconciler := NewEnvReconciler(ldClient, sgClient, tag)
	if err := reconciler.Reconcile(ctx); err != nil {
		return nil, fmt.Errorf("env reconciliation: %w", err)
	}

	plan := &TargetingPlan{
		Kind:          kind,
		Reconciler:    reconciler,
		rawGates:      map[string]statsig.Gate{},
		rawConfigs:    map[string]statsig.DynamicConfig{},
		gateOverrides: map[string][]statsig.Override{},
		dcOverrides:   map[string][]statsig.Override{},
	}

	switch kind {
	case ImportKindGates:
		for _, g := range gates {
			plan.rawGates[SanitizeFlagKey(g.ID)] = g
		}
		ids := make([]string, len(gates))
		for i, g := range gates {
			ids[i] = g.ID
		}
		results := fetchOverridesParallel(ctx, ids, overrideFetchConcurrency, sgClient.GetGateOverrides)
		for id, overrides := range results {
			plan.gateOverrides[SanitizeFlagKey(id)] = overrides
		}
	case ImportKindDynamicConfigs:
		for _, c := range configs {
			plan.rawConfigs[SanitizeFlagKey(c.ID)] = c
		}
		ids := make([]string, len(configs))
		for i, c := range configs {
			ids[i] = c.ID
		}
		results := fetchOverridesParallel(ctx, ids, overrideFetchConcurrency, sgClient.GetDynamicConfigOverrides)
		for id, overrides := range results {
			plan.dcOverrides[SanitizeFlagKey(id)] = overrides
		}
	}

	return plan, nil
}

// fetchOverridesParallel fans out override fetches across `concurrency`
// workers. Failures are logged and the entry is omitted from the result map
// (non-fatal: targeting for that flag proceeds without overrides).
func fetchOverridesParallel(
	ctx context.Context,
	ids []string,
	concurrency int,
	fetch func(ctx context.Context, id string) ([]statsig.Override, error),
) map[string][]statsig.Override {
	if len(ids) == 0 {
		return map[string][]statsig.Override{}
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
	workers := min(concurrency, len(ids))
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
	for r := range results {
		if r.err != nil {
			log.Printf("failed to fetch overrides for %s: %v", r.id, r.err)
			continue
		}
		out[r.id] = r.overrides
	}
	return out
}

// ApplyTargeting runs after flag creation. For each created flag, builds
// per-env settings from the plan and issues one PATCH per flag covering all
// envs in a single JSON Patch op array. JSON Patch is atomic, so an all-envs-
// or-nothing failure mode is consistent with LD's contract.
//
// Returns per-flag notes and patch failures for the migration report.
func ApplyTargeting(
	ctx context.Context,
	ldClient *launchdarkly.Client,
	plan *TargetingPlan,
	createdFlags []launchdarkly.Flag,
) []launchdarkly.FailedFlag {
	var failures []launchdarkly.FailedFlag

	for _, flag := range createdFlags {
		var settingsByEnv map[string]LDEnvSettings
		var notes []launchdarkly.FailedFlag

		switch plan.Kind {
		case ImportKindGates:
			gate, ok := plan.rawGates[flag.Key]
			if !ok {
				continue
			}
			settingsByEnv, notes = BuildGateEnvSettings(gate, plan.gateOverrides[flag.Key], plan.Reconciler)
		case ImportKindDynamicConfigs:
			dc, ok := plan.rawConfigs[flag.Key]
			if !ok {
				continue
			}
			settingsByEnv, notes = BuildDCEnvSettings(dc, flag.Variations, plan.dcOverrides[flag.Key], plan.Reconciler)
		default:
			continue
		}
		failures = append(failures, notes...)

		// Iterate envs in sorted order so the emitted op array is stable
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
			continue
		}
		if err := ldClient.PatchFlag(ctx, flag.Key, allOps); err != nil {
			log.Printf("failed to patch flag %s: %v", flag.Key, err)
			failures = append(failures, launchdarkly.FailedFlag{
				Name:  flag.Key,
				Error: fmt.Sprintf("[warning] Could not apply targeting for flag %q: %v", flag.Key, err),
			})
			continue
		}
	}

	return failures
}
