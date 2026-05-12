package flag

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
)

// ============================================================================
// SanitizeKey
// ============================================================================

func TestSanitizeKey(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// Happy paths
		{"my_gate", "my_gate"},
		{"my-gate-2", "my-gate-2"},
		{"my.gate", "my.gate"},
		// Statsig ID format
		{"show_banner::feature_gate", "show_banner_feature_gate"},
		// Invalid chars → underscore
		{"my gate!", "my_gate"},
		{"foo/bar", "foo_bar"},
		// Leading non-alphanumeric → prepend ld_. Underscores trim;
		// hyphens (which are valid key chars) are preserved.
		{"_leading", "ld_leading"},
		{"-leading", "ld_-leading"},
		// Repeated underscores collapsed
		{"foo___bar", "foo_bar"},
		// Empty input → "ld" (the ld_flag fallback is unreachable with the
		// current algorithm; documenting actual behavior here).
		{"", "ld"},
		// All-invalid input → also "ld" via the same path
		{"!!!", "ld"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := SanitizeKey(tc.in); got != tc.want {
				t.Errorf("SanitizeKey(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ============================================================================
// SanitizeTags
// ============================================================================

func TestSanitizeTags(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty", nil, []string{}},
		{"happy path", []string{"p0", "ux"}, []string{"p0", "ux"}},
		{"invalid chars", []string{"my tag!"}, []string{"my_tag"}},
		{"dedup", []string{"p0", "p0"}, []string{"p0"}},
		{"empty becomes 'tag'", []string{""}, []string{"tag"}},
		{
			"long string truncated to 64",
			[]string{"abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJ"}, // 72 chars
			[]string{"abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz0123456789AB"},       // 64 chars
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeTags(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("SanitizeTags(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// ============================================================================
// NewFlagsFromGates
// ============================================================================

func TestNewFlagsFromGates_HappyPath(t *testing.T) {
	gates := []statsig.Gate{
		{ID: "show_banner", Name: "Show Banner", Description: "banner gate", Tags: []string{"p0"}, Type: "PERMANENT"},
		{ID: "alpha_test::feature_gate", Name: "Alpha Test", Type: "TEMPORARY", Tags: []string{"alpha"}},
	}

	flags, failed := NewFlagsFromGates(gates, "imported-from-statsig", "user-abc")
	if len(failed) != 0 {
		t.Errorf("expected no failed flags, got: %v", failed)
	}
	if len(flags) != 2 {
		t.Fatalf("got %d flags, want 2", len(flags))
	}

	first := flags[0]
	if first.Key != "show_banner" {
		t.Errorf("flags[0].Key = %q, want %q", first.Key, "show_banner")
	}
	if first.Temporary {
		t.Error("flags[0] PERMANENT gate should not be Temporary")
	}
	if first.MaintainerID != "user-abc" {
		t.Errorf("MaintainerID = %q, want user-abc", first.MaintainerID)
	}
	if !slices.Contains(first.Tags, "imported-from-statsig") {
		t.Errorf("Tags should include the import tag; got %v", first.Tags)
	}
	if !slices.Contains(first.Tags, "p0") {
		t.Errorf("Tags should include the original p0 tag; got %v", first.Tags)
	}
	wantVariations := []launchdarkly.Variation{{Name: "true", Value: true}, {Name: "false", Value: false}}
	if !reflect.DeepEqual(first.Variations, wantVariations) {
		t.Errorf("Variations = %v, want %v", first.Variations, wantVariations)
	}
	wantDefaults := launchdarkly.Defaults{OnVariation: 0, OffVariation: 1}
	if first.Defaults != wantDefaults {
		t.Errorf("Defaults = %+v, want %+v", first.Defaults, wantDefaults)
	}

	// Second gate: TEMPORARY type → Temporary=true; ID with :: → sanitized
	second := flags[1]
	if !second.Temporary {
		t.Error("flags[1] TEMPORARY gate should be Temporary=true")
	}
	if second.Key != "alpha_test_feature_gate" {
		t.Errorf("flags[1].Key = %q, want %q", second.Key, "alpha_test_feature_gate")
	}
}

func TestNewFlagsFromGates_EmptyTagNotAdded(t *testing.T) {
	gates := []statsig.Gate{{ID: "g1", Name: "G1", Tags: []string{"orig"}}}
	flags, _ := NewFlagsFromGates(gates, "", "")
	if slices.Contains(flags[0].Tags, "") {
		t.Errorf("empty tag should not be added; got %v", flags[0].Tags)
	}
	if !slices.Contains(flags[0].Tags, "orig") {
		t.Errorf("original tag should be preserved; got %v", flags[0].Tags)
	}
}

// ============================================================================
// NewFlagsFromDynamicConfigs
// ============================================================================

func TestNewFlagsFromDynamicConfigs_SingleVariantGetsFiller(t *testing.T) {
	// DC with no rules and a non-trivial default → one variation; expect a filler
	// to be appended so LD's ≥2 requirement is satisfied.
	dcs := []statsig.DynamicConfig{{
		ID:           "single-config",
		Name:         "Single Config",
		DefaultValue: json.RawMessage(`{"foo":"bar"}`),
	}}

	flags, _ := NewFlagsFromDynamicConfigs(dcs, "", "")
	if len(flags) != 1 {
		t.Fatalf("got %d flags, want 1", len(flags))
	}
	f := flags[0]
	if len(f.Variations) < 2 {
		t.Errorf("DC should produce ≥2 variations (LD requirement); got %d: %v", len(f.Variations), f.Variations)
	}
}

func TestNewFlagsFromDynamicConfigs_MultiVariantFromFirstRule(t *testing.T) {
	dcs := []statsig.DynamicConfig{{
		ID:           "ab-test",
		Name:         "AB Test",
		DefaultValue: json.RawMessage(`{"copy":"default"}`),
		Rules: []statsig.DCRule{{
			ID: "r1",
			Variants: []statsig.DCVariant{
				{Name: "A", ReturnValue: json.RawMessage(`{"copy":"hello"}`)},
				{Name: "B", ReturnValue: json.RawMessage(`{"copy":"hi"}`)},
			},
		}},
	}}

	flags, _ := NewFlagsFromDynamicConfigs(dcs, "", "")
	if len(flags[0].Variations) != 3 {
		t.Errorf("expected 3 variations (A, B, Default); got %d: %+v", len(flags[0].Variations), flags[0].Variations)
	}
	if flags[0].Variations[0].Name != "A" || flags[0].Variations[1].Name != "B" || flags[0].Variations[2].Name != "Default" {
		t.Errorf("variation names = %v %v %v, want A B Default",
			flags[0].Variations[0].Name, flags[0].Variations[1].Name, flags[0].Variations[2].Name)
	}
}

func TestNewFlagsFromDynamicConfigs_DedupCollapseOntoDefault(t *testing.T) {
	// A variant whose value matches the dc-level default should collapse onto
	// the default after dedup. The result has 2 variations (the unique one
	// plus the dedup'd default, which becomes the off variation).
	dcs := []statsig.DynamicConfig{{
		ID:           "collapsing",
		Name:         "Collapsing",
		DefaultValue: json.RawMessage(`{"copy":"hi"}`),
		Rules: []statsig.DCRule{{
			ID: "r1",
			Variants: []statsig.DCVariant{
				{Name: "A", ReturnValue: json.RawMessage(`{"copy":"hi"}`)}, // same as default
				{Name: "B", ReturnValue: json.RawMessage(`{"copy":"new"}`)},
			},
		}},
	}}

	flags, _ := NewFlagsFromDynamicConfigs(dcs, "", "")
	if len(flags[0].Variations) != 2 {
		t.Errorf("expected 2 unique variations after dedup; got %d: %+v", len(flags[0].Variations), flags[0].Variations)
	}
}

// ============================================================================
// FilterNewFlags — D6 key-based dedupe
// ============================================================================

func TestFilterNewFlags_DedupByKeyNotName(t *testing.T) {
	// D6: behavior change from the goaltender worker. The Statsig source
	// renamed a gate (display name changed), but its sanitized key is stable.
	// A re-run should NOT re-create the flag.
	existing := []launchdarkly.Flag{
		{Key: "show_banner", Name: "Show Banner (legacy)"},
	}
	newFlags := []launchdarkly.Flag{
		{Key: "show_banner", Name: "Show Banner (renamed)"}, // SAME key, different name
		{Key: "alpha_test", Name: "Alpha Test"},
	}

	got := FilterNewFlags(newFlags, existing)
	if len(got) != 1 {
		t.Fatalf("got %d flags, want 1 (renamed flag should be deduped by key)", len(got))
	}
	if got[0].Key != "alpha_test" {
		t.Errorf("kept flag key = %q, want alpha_test", got[0].Key)
	}
}

func TestFilterNewFlags_NameCollisionIsNotEnoughToDedupe(t *testing.T) {
	// Inverse case: same name, different key. With name-based dedupe (the
	// old worker behavior), this would be filtered. With D6's key-based
	// dedupe, it's a distinct flag and should be kept.
	existing := []launchdarkly.Flag{{Key: "foo", Name: "Same Name"}}
	newFlags := []launchdarkly.Flag{{Key: "bar", Name: "Same Name"}}

	got := FilterNewFlags(newFlags, existing)
	if len(got) != 1 || got[0].Key != "bar" {
		t.Errorf("flag with different key but same name should NOT be deduped; got: %+v", got)
	}
}

// ============================================================================
// Helpers
// ============================================================================

func TestUnwrapVariantValue(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", "null"},
		{"scalar string", `"hello"`, `"hello"`},
		{"scalar number", `42`, `42`},
		{"wrapped object", `{"value":"hello"}`, `"hello"`},
		{"non-wrapped object", `{"foo":"bar"}`, `{"foo":"bar"}`},
		{"multi-key object", `{"value":"x","other":"y"}`, `{"value":"x","other":"y"}`},
		{"malformed JSON", `{not json`, `{not json`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(unwrapVariantValue(json.RawMessage(tc.in)))
			if got != tc.want {
				t.Errorf("unwrapVariantValue(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestWrapScalarDefault(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", `{"value":null}`},
		{"null", `null`, `{"value":null}`},
		{"object passes through", `{"foo":"bar"}`, `{"foo":"bar"}`},
		{"scalar string wrapped", `"hello"`, `{"value":"hello"}`},
		{"scalar number wrapped", `42`, `{"value":42}`},
		{"array wrapped", `[1,2]`, `{"value":[1,2]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(wrapScalarDefault(json.RawMessage(tc.in)))
			if got != tc.want {
				t.Errorf("wrapScalarDefault(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCanonicalJSON_KeyOrderNormalized(t *testing.T) {
	a := canonicalJSON(json.RawMessage(`{"a":1,"b":2}`))
	b := canonicalJSON(json.RawMessage(`{"b":2,"a":1}`))
	if a != b {
		t.Errorf("canonicalJSON should normalize key order: %q vs %q", a, b)
	}
}
