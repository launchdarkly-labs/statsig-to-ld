package targeting

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/flag"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
)

// defaultAutoCreatedEnvColor is the LD env color assigned when the importer
// creates a new env. Mid-grey.
const defaultAutoCreatedEnvColor = "808080"

// envReconcilerStatsigLister is the StatsigClient method surface the
// reconciler needs. Defined as an interface for test mockability.
type envReconcilerStatsigLister interface {
	ListEnvironments(ctx context.Context) ([]statsig.Environment, error)
}

// envReconcilerLDClient is the LD client method surface the reconciler needs.
type envReconcilerLDClient interface {
	ListEnvironments(ctx context.Context) ([]launchdarkly.Environment, error)
	CreateEnvironment(ctx context.Context, env launchdarkly.Environment) (*launchdarkly.Environment, error)
}

// EnvReconciler maps Statsig env names to LD env keys, auto-creating missing
// LD envs on the fly. Built once at the start of an import; consulted during
// rule/override transformation to fan out to the right per-LD-env set.
type EnvReconciler struct {
	ld          envReconcilerLDClient
	statsig     envReconcilerStatsigLister
	ldTag       string
	autoCreate  bool
	mapping     map[string]string   // statsig env name (lowercased) → LD env key
	reverseMap  map[string][]string // LD env key → original-cased statsig env names
	unreachable map[string]bool     // statsig env names (lowercased) we couldn't resolve
	allLDKeys   []string            // matched + created LD env keys (reachable only)
	notes       []Note
}

// NewEnvReconciler constructs a reconciler. When autoCreate is false, missing
// LD envs are recorded as unreachable instead of being auto-created (the
// --no-create-envs path).
func NewEnvReconciler(ld envReconcilerLDClient, sg envReconcilerStatsigLister, ldTag string, autoCreate bool) *EnvReconciler {
	return &EnvReconciler{
		ld:          ld,
		statsig:     sg,
		ldTag:       ldTag,
		autoCreate:  autoCreate,
		mapping:     map[string]string{},
		reverseMap:  map[string][]string{},
		unreachable: map[string]bool{},
	}
}

// Reconcile fetches both Statsig and LD environment lists (concurrently) and
// builds the mapping, creating any missing LD environments when autoCreate is
// true. Informational notes are accumulated for callers to surface.
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
		return fmt.Errorf("fetch Statsig environments: %w", statsigErr)
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

	// Deterministic auto-create order across re-runs.
	sort.SliceStable(statsigEnvs, func(i, j int) bool {
		return strings.ToLower(statsigEnvs[i].Name) < strings.ToLower(statsigEnvs[j].Name)
	})

	reachable := map[string]struct{}{}
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

		if !r.autoCreate {
			r.unreachable[lower] = true
			r.notes = append(r.notes, newInfo("(environment) "+se.Name,
				"Statsig environment %q has no LD equivalent and --no-create-envs is set; rules and overrides for it will not be imported.",
				se.Name))
			continue
		}

		newKey := flag.SanitizeKey(lower)
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
			r.notes = append(r.notes, newInfo("(environment) "+se.Name,
				"LaunchDarkly environment %q auto-created for Statsig environment %q.",
				created.Key, se.Name))
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
				r.notes = append(r.notes, newWarning("(environment) "+se.Name,
					"LaunchDarkly returned 409 conflict creating environment %q but it was not visible on refetch.",
					newKey))
			}
		case errors.Is(createErr, launchdarkly.ErrEnvironmentForbidden):
			r.unreachable[lower] = true
			r.notes = append(r.notes, newWarning("(environment) "+se.Name,
				"Could not create LaunchDarkly environment %q (insufficient permissions). Rules and overrides for Statsig environment %q will not be imported.",
				newKey, se.Name))
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

// StatsigEnvNamesForLDKey returns the original-cased Statsig env names that
// map to the given LD env key.
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

// Notes returns the accumulated reconciler notes (env-creation outcomes,
// permission errors, refetch surprises).
func (r *EnvReconciler) Notes() []Note {
	out := make([]Note, len(r.notes))
	copy(out, r.notes)
	return out
}
