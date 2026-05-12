// Package analyze produces a pre-migration sizing report for a Statsig
// project. It is read-only — no writes to Statsig or LaunchDarkly — and
// classifies gates, dynamic configs, environments, and metrics by how the
// statsig-to-ld importer will treat them.
//
// Per decision D8 the importer fails closed by default on a handful of
// structurally lossy Statsig features. analyze surfaces those features in
// advance so the user can decide whether to opt in via `--accept-data-loss`
// before running an import.
package analyze

import "time"

// Report is the full pre-migration sizing result.
type Report struct {
	Timestamp     time.Time         `json:"timestamp"`
	StatsigProject string           `json:"statsig_project,omitempty"`
	LDProject     string            `json:"ld_project,omitempty"`

	Gates          GateSummary          `json:"gates"`
	DynamicConfigs DynamicConfigSummary `json:"dynamic_configs"`
	Environments   EnvironmentSummary   `json:"environments"`
	Metrics        MetricSummary        `json:"metrics"`

	// EstimatedManualWork is the count of items that will fail-closed under
	// D8 unless the user opts in via --accept-data-loss. It's a rough sum of
	// the GateSummary fail-closed counters and the multi-variant DC count.
	EstimatedManualWork int `json:"estimated_manual_work"`
}

// GateSummary counts gates by how the importer will treat each one.
type GateSummary struct {
	Total int `json:"total"`

	// BooleanSimple counts gates whose rules use only operator/condition
	// types we can faithfully reproduce in LD. These are safe to import
	// with default flags.
	BooleanSimple int `json:"boolean_simple"`

	// WithSegments counts gates with any passes_segment / fails_segment
	// condition. Fail-closed under D8.
	WithSegments int `json:"with_segments"`

	// WithPrerequisites counts gates with any passes_gate / fails_gate
	// condition. Fail-closed under D8.
	WithPrerequisites int `json:"with_prerequisites"`

	// WithCustomUnitID counts gates with any unit_id condition whose
	// CustomID is non-empty and not "userID". Fail-closed under D8.
	WithCustomUnitID int `json:"with_custom_unit_id"`

	// WithUnreachableRules counts gates with rules after a "public"
	// (match-everyone) rule. Those trailing rules silently drop on import.
	WithUnreachableRules int `json:"with_unreachable_rules"`

	// WithApproximatedOperators counts gates that use operators LD
	// approximates rather than implements exactly (currently version_gte
	// and version_lte → semVerGreaterThan / semVerLessThan). Imports
	// succeed; results are approximate.
	WithApproximatedOperators int `json:"with_approximated_operators"`
}

// DynamicConfigSummary counts dynamic configs by variant shape. Overrides are
// not fetched (would require an O(N) per-DC HTTP request); see the README.
type DynamicConfigSummary struct {
	Total         int `json:"total"`
	SingleVariant int `json:"single_variant"`
	MultiVariant  int `json:"multi_variant"`
}

// EnvironmentSummary reports the Statsig-side env list and (optionally) the
// LD-side env list with a mapping preview. LD-side fields are populated only
// when the user provides --ld-key + --ld-project.
type EnvironmentSummary struct {
	StatsigEnvs []string `json:"statsig_envs"`

	// LDEnvsKnown indicates whether the LD-side preview was attempted.
	// When false, the LD fields below are empty.
	LDEnvsKnown bool     `json:"ld_envs_known"`
	LDEnvs      []string `json:"ld_envs,omitempty"`

	// AutoCreateRequired is the list of Statsig env names that would be
	// auto-created in LD on import (case-insensitive match miss).
	AutoCreateRequired []string `json:"auto_create_required,omitempty"`
}

// MetricSummary classifies Statsig metrics by whether the existing converter
// can produce an LD metric for each one.
type MetricSummary struct {
	Total        int `json:"total"`
	Convertible  int `json:"convertible"`
	Incompatible int `json:"incompatible"`
}
