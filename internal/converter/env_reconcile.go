// Env reconciler. Maps Statsig env names to LD env keys, auto-creating
// missing LD envs on the fly. Ported from goaltender
// flag_import_worker/env_reconcile.go (PR #829).
package converter

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"

	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/statsig"
)

// defaultAutoCreatedEnvColor is the LD env color assigned when the importer
// creates a new env. Mid-grey; can derive from name hash in a follow-up.
const defaultAutoCreatedEnvColor = "808080"

// EnvLister is the LD-client method surface the reconciler needs for reads.
// Concrete impl: *launchdarkly.Client.
type envLDClient interface {
	ListEnvironments(ctx context.Context) ([]launchdarkly.Environment, error)
	CreateEnvironment(ctx context.Context, env launchdarkly.Environment) (launchdarkly.Environment, error)
}

type envStatsigClient interface {
	ListEnvironments(ctx context.Context) ([]statsig.Environment, error)
}

// EnvReconciler builds the Statsig env name → LD env key mapping. Built once
// at the start of an import; consulted during rule/override transformation to
// fan out to the right per-LD-env set.
type EnvReconciler struct {
	ld          envLDClient
	statsig     envStatsigClient
	ldTag       string
	mapping     map[string]string   // statsig env name (lowercased) → LD env key
	reverseMap  map[string][]string // LD env key → original-cased statsig env names mapping to it
	unreachable map[string]bool     // statsig env names (lowercased) the importer could not resolve
	allLDKeys   []string            // union of matched + created LD env keys (reachable only)
	failures    []launchdarkly.FailedFlag
}

// NewEnvReconciler constructs a reconciler with the provided clients and the
// LD tag to apply to auto-created environments.
func NewEnvReconciler(ld *launchdarkly.Client, sg *statsig.Client, ldTag string) *EnvReconciler {
	return &EnvReconciler{
		ld:          ld,
		statsig:     sg,
		ldTag:       ldTag,
		mapping:     map[string]string{},
		reverseMap:  map[string][]string{},
		unreachable: map[string]bool{},
	}
}

// Reconcile fetches both Statsig and LD environment lists (concurrently) and
// builds the mapping, creating any missing LD environments. Returns
// informational FailedFlag entries for envs that could not be resolved.
func (r *EnvReconciler) Reconcile(ctx context.Context) error {
	var (
		statsigEnvs []statsig.Environment
		ldEnvs      []launchdarkly.Environment
		statsigErr  error
		ldErr       error
		wg          sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		statsigEnvs, statsigErr = r.statsig.ListEnvironments(ctx)
	}()
	go func() {
		defer wg.Done()
		ldEnvs, ldErr = r.ld.ListEnvironments(ctx)
	}()
	wg.Wait()
	if statsigErr != nil {
		return fmt.Errorf("fetch statsig environments: %w", statsigErr)
	}
	if ldErr != nil {
		return fmt.Errorf("fetch LD environments: %w", ldErr)
	}

	ldByLowerKey := make(map[string]launchdarkly.Environment, len(ldEnvs))
	ldByLowerName := make(map[string]launchdarkly.Environment, len(ldEnvs))
	for _, e := range ldEnvs {
		ldByLowerKey[strings.ToLower(e.Key)] = e
		ldByLowerName[strings.ToLower(e.Name)] = e
	}

	// Sort for deterministic auto-create order across re-runs.
	sort.SliceStable(statsigEnvs, func(i, j int) bool {
		return strings.ToLower(statsigEnvs[i].Name) < strings.ToLower(statsigEnvs[j].Name)
	})

	reachable := map[string]struct{}{}
	// refetchedLDByLowerKey is populated lazily on the first 409 conflict so
	// subsequent 409s reuse the refetched list instead of issuing another
	// full GET per conflict.
	var refetchedLDByLowerKey map[string]launchdarkly.Environment

	for _, se := range statsigEnvs {
		lower := strings.ToLower(se.Name)
		if ldEnv, ok := ldByLowerKey[lower]; ok {
			r.assignMapping(lower, se.Name, ldEnv.Key, reachable)
			continue
		}
		if ldEnv, ok := ldByLowerName[lower]; ok {
			r.assignMapping(lower, se.Name, ldEnv.Key, reachable)
			continue
		}

		// Missing LD env — auto-create. Key uses the Statsig env name run
		// through the flag-key sanitizer (lowercased input).
		newKey := SanitizeFlagKey(lower)
		var newTags []string
		if r.ldTag != "" {
			newTags = []string{r.ldTag}
		}

		created, createErr := r.ld.CreateEnvironment(ctx, launchdarkly.Environment{
			Key:   newKey,
			Name:  se.Name,
			Color: defaultAutoCreatedEnvColor,
			Tags:  newTags,
		})
		switch {
		case createErr == nil:
			r.assignMapping(lower, se.Name, created.Key, reachable)
			log.Printf("env-reconcile: LD environment %q auto-created for Statsig environment %q", created.Key, se.Name)
		case errors.Is(createErr, launchdarkly.ErrEnvironmentExists):
			if refetchedLDByLowerKey == nil {
				refetched, refetchErr := r.ld.ListEnvironments(ctx)
				if refetchErr != nil {
					return fmt.Errorf("refetch LD environments after 409 on %s: %w", newKey, refetchErr)
				}
				refetchedLDByLowerKey = make(map[string]launchdarkly.Environment, len(refetched))
				for _, e := range refetched {
					refetchedLDByLowerKey[strings.ToLower(e.Key)] = e
				}
			}
			if ldEnv, ok := refetchedLDByLowerKey[strings.ToLower(newKey)]; ok {
				r.assignMapping(lower, se.Name, ldEnv.Key, reachable)
			} else {
				r.unreachable[lower] = true
				r.failures = append(r.failures, launchdarkly.FailedFlag{
					Name:  "(environment) " + se.Name,
					Error: fmt.Sprintf("[warning] LaunchDarkly returned 409 conflict creating environment %q but it was not visible on refetch.", newKey),
				})
			}
		case errors.Is(createErr, launchdarkly.ErrEnvironmentForbidden):
			r.unreachable[lower] = true
			r.failures = append(r.failures, launchdarkly.FailedFlag{
				Name:  "(environment) " + se.Name,
				Error: fmt.Sprintf("[warning] Could not create LaunchDarkly environment %q (insufficient permissions). Rules and overrides for Statsig environment %q will not be imported.", newKey, se.Name),
			})
		default:
			return fmt.Errorf("create LD environment %s: %w", newKey, createErr)
		}
	}

	r.allLDKeys = make([]string, 0, len(reachable))
	for k := range reachable {
		r.allLDKeys = append(r.allLDKeys, k)
	}
	sort.Strings(r.allLDKeys)
	return nil
}

// assignMapping records that a Statsig env name maps to an LD env key,
// updating both the forward and reverse indices and the reachable set.
func (r *EnvReconciler) assignMapping(lowerStatsigName, originalStatsigName, ldKey string, reachable map[string]struct{}) {
	r.mapping[lowerStatsigName] = ldKey
	r.reverseMap[ldKey] = append(r.reverseMap[ldKey], originalStatsigName)
	reachable[ldKey] = struct{}{}
}

// LookupLDEnv returns the LD env key mapped from a Statsig env name (case-
// insensitive). Returns ok=false when the env is unreachable or unknown.
func (r *EnvReconciler) LookupLDEnv(statsigEnvName string) (string, bool) {
	lower := strings.ToLower(statsigEnvName)
	if r.unreachable[lower] {
		return "", false
	}
	key, ok := r.mapping[lower]
	return key, ok
}

// StatsigEnvNamesForLDKey returns the original-cased Statsig environment names
// that map to the given LD env key. O(1) lookup via the reverse index.
func (r *EnvReconciler) StatsigEnvNamesForLDKey(ldKey string) []string {
	return r.reverseMap[ldKey]
}

// AllReachableLDEnvKeys returns the union of matched + auto-created LD env
// keys, sorted.
func (r *EnvReconciler) AllReachableLDEnvKeys() []string {
	out := make([]string, len(r.allLDKeys))
	copy(out, r.allLDKeys)
	return out
}

// Failures returns the accumulated env-level failure/info entries to surface
// alongside flag-level failures.
func (r *EnvReconciler) Failures() []launchdarkly.FailedFlag {
	out := make([]launchdarkly.FailedFlag, len(r.failures))
	copy(out, r.failures)
	return out
}
