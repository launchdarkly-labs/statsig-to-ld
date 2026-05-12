package converter

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/statsig"
)

// ===========================================================================
// Sanitizers
//
// LD flag keys must contain only [a-zA-Z0-9\-_.] and must start with an
// alphanumeric. Tags use the same alphabet, capped at 64 chars, with a
// fallback for empty inputs. Tests cover the cases the goaltender team
// hit when porting the migration CLI: leading non-alnum, repeated
// underscores from `__`-bearing Statsig IDs, mixed valid/invalid chars.
// ===========================================================================

func TestSanitizeFlagKey(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"simple_gate", "simple_gate"},
		{"gate-with-dashes", "gate-with-dashes"},
		{"My Gate With Spaces", "My_Gate_With_Spaces"},
		{"gate.with.dots", "gate.with.dots"},
		{"gate/with/slashes", "gate_with_slashes"},
		{"gate@with#symbols", "gate_with_symbols"},
		{"_leading_underscore", "ld_leading_underscore"},
		{"123_leading_digit", "123_leading_digit"},
		// "_____" → invalid-char regex matches nothing (underscore is allowed),
		// so no leading alnum → prepend "ld_" → trim trailing _ → "ld".
		// Bizarre but matches goaltender exactly; the "ld_flag" fallback only
		// fires when the result is truly empty (which only happens for input
		// like " " whose chars all become underscores then get trimmed).
		{"_____", "ld"},
		{"", "ld"},
		{"trailing___", "trailing"},
		{"a___b", "a_b"},  // collapse repeated underscores
		{"a--b", "a--b"},  // dashes survive
		// "!@#abc" → "___abc" → no leading alnum → "ld____abc" → trim edges
		// (no leading/trailing _ now since "ld" + "abc" are alnum) → collapse
		// inner _s → "ld_abc". Leading non-alnum is NOT stripped; it's
		// prefixed with "ld_".
		{"!@#abc", "ld_abc"},
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			got := SanitizeFlagKey(c.input)
			if got != c.want {
				t.Errorf("SanitizeFlagKey(%q) = %q, want %q", c.input, got, c.want)
			}
		})
	}
}

func TestSanitizeFlagTags(t *testing.T) {
	cases := []struct {
		name  string
		input []string
		want  []string
	}{
		{"empty input", nil, []string{}},
		{"valid tags pass through", []string{"prod", "team-x"}, []string{"prod", "team-x"}},
		{"empty string becomes 'tag'", []string{""}, []string{"tag"}},
		{"dedup case-sensitive", []string{"a", "b", "a"}, []string{"a", "b"}},
		{"sanitize invalid chars", []string{"my tag!"}, []string{"my_tag"}},
		{"dedup after sanitize", []string{"my tag", "my_tag"}, []string{"my_tag"}},
		{"trim leading/trailing underscores", []string{"_x_"}, []string{"x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SanitizeFlagTags(c.input)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("SanitizeFlagTags(%v) = %v, want %v", c.input, got, c.want)
			}
		})
	}
}

func TestSanitizeFlagTags_CapAt64Chars(t *testing.T) {
	long := "a"
	for len(long) < 100 {
		long += "a"
	}
	got := SanitizeFlagTags([]string{long})
	if len(got) != 1 || len(got[0]) != 64 {
		t.Errorf("expected 64-char cap, got len(got)=%d len(got[0])=%d", len(got), len(got[0]))
	}
}

// ===========================================================================
// Gate conversion
//
// Statsig feature gates → LD boolean flags. Variations are always
// [{true,true}, {false,false}]; Temporary derives from gate.type ==
// "TEMPORARY" (per Statsig enum: TEMPORARY|PERMANENT|STALE|TEMPLATE);
// description carries through; key + tags are sanitized.
// ===========================================================================

func TestConvertGates_BasicProperties(t *testing.T) {
	gates := []statsig.Gate{
		{
			ID:          "checkout_new_flow",
			Name:        "Checkout: New Flow",
			Description: "Enables the redesigned checkout funnel",
			IsEnabled:   true,
			Tags:        []string{"team-payments", "mobile"},
			Type:        "PERMANENT",
			IDType:      "userID",
		},
	}
	results := ConvertGates(gates, "imported-from-statsig", "maintainer-123")
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	f := results[0].Flag

	if f.Key != "checkout_new_flow" {
		t.Errorf("Key = %q, want \"checkout_new_flow\"", f.Key)
	}
	if f.Name != "Checkout: New Flow" {
		t.Errorf("Name preserved verbatim, got %q", f.Name)
	}
	if f.Description != "Enables the redesigned checkout funnel" {
		t.Errorf("Description = %q", f.Description)
	}
	if f.MaintainerID != "maintainer-123" {
		t.Errorf("MaintainerID = %q", f.MaintainerID)
	}
	if f.Temporary {
		t.Errorf("Temporary=true for PERMANENT gate")
	}
	if len(f.Variations) != 2 {
		t.Fatalf("Variations len = %d, want 2", len(f.Variations))
	}
	if f.Variations[0].Value != true || f.Variations[0].Name != "true" {
		t.Errorf("Variations[0] = %+v, want {true, true}", f.Variations[0])
	}
	if f.Variations[1].Value != false || f.Variations[1].Name != "false" {
		t.Errorf("Variations[1] = %+v, want {false, false}", f.Variations[1])
	}
	if f.Defaults.OnVariation != 0 || f.Defaults.OffVariation != 1 {
		t.Errorf("Defaults = %+v, want {0, 1}", f.Defaults)
	}
	// Tags include the source tags + the extra import tag, all sanitized
	wantTags := []string{"team-payments", "mobile", "imported-from-statsig"}
	if !reflect.DeepEqual(f.Tags, wantTags) {
		t.Errorf("Tags = %v, want %v", f.Tags, wantTags)
	}
}

func TestConvertGates_TemporaryFlag(t *testing.T) {
	cases := []struct {
		statsigType string
		wantTemp    bool
	}{
		{"TEMPORARY", true},
		{"PERMANENT", false},
		{"STALE", false},   // only TEMPORARY → temporary; STALE doesn't apply
		{"TEMPLATE", false},
		{"", false}, // unknown / unset → permanent
	}
	for _, c := range cases {
		t.Run(c.statsigType, func(t *testing.T) {
			results := ConvertGates([]statsig.Gate{{ID: "g", Name: "G", Type: c.statsigType}}, "", "")
			if results[0].Flag.Temporary != c.wantTemp {
				t.Errorf("Temporary = %v for type %q, want %v", results[0].Flag.Temporary, c.statsigType, c.wantTemp)
			}
		})
	}
}

func TestConvertGates_EmptyTagsNoTagFlag(t *testing.T) {
	results := ConvertGates([]statsig.Gate{{ID: "g", Name: "G"}}, "", "")
	if len(results[0].Flag.Tags) != 0 {
		t.Errorf("Tags = %v, want empty", results[0].Flag.Tags)
	}
}

func TestConvertGates_SanitizesKeyFromMessyID(t *testing.T) {
	// Real Statsig gate IDs are reasonably clean but can contain spaces and
	// punctuation when the gate has been renamed. Verify the sanitizer kicks in.
	results := ConvertGates([]statsig.Gate{{ID: "Gate With Spaces!", Name: "x"}}, "", "")
	if results[0].Flag.Key != "Gate_With_Spaces" {
		t.Errorf("Key = %q, want \"Gate_With_Spaces\"", results[0].Flag.Key)
	}
}

// ===========================================================================
// DynamicConfig conversion
//
// Statsig dynamic configs are richer than gates: they can have variants on
// rules (newer API), nested return values, and a top-level defaultValue. LD
// represents the result as a JSON multi-variate flag. The conversion is
// driven by:
//   1. rules[0].Variants → one variation per variant (unwrap {"value": x} shells)
//   2. dc.defaultValue → a "Default" variation (wrap scalars in {"value": x})
//   3. Dedup by canonical JSON of value (a variant whose value matches the
//      default collapses onto the same variation index).
//   4. If only one distinct variation survives, append an "Empty" filler so
//      LD's ≥2-variations requirement is satisfied.
// ===========================================================================

func TestConvertDynamicConfigs_DefaultOnly(t *testing.T) {
	// No rules → just defaultValue → 1 distinct → "Empty" filler appended.
	configs := []statsig.DynamicConfig{
		{
			ID:           "homepage_layout",
			Name:         "Homepage Layout",
			DefaultValue: json.RawMessage(`{"hero":"a","showBanner":true}`),
		},
	}
	results := ConvertDynamicConfigs(configs, "", "")
	if len(results) != 1 {
		t.Fatalf("got %d results", len(results))
	}
	f := results[0].Flag
	if len(f.Variations) != 2 {
		t.Fatalf("expected 2 variations (Default + filler), got %d", len(f.Variations))
	}
	if f.Variations[0].Name != "Default" {
		t.Errorf("Variations[0].Name = %q, want \"Default\"", f.Variations[0].Name)
	}
	if f.Variations[1].Name != "Empty" {
		t.Errorf("Variations[1].Name = %q, want \"Empty\"", f.Variations[1].Name)
	}
	// OnVariation should point at the non-Default (filler) so the flag has
	// somewhere meaningful to send "on" traffic.
	if f.Defaults.OnVariation != 1 || f.Defaults.OffVariation != 0 {
		t.Errorf("Defaults = %+v, want {On:1, Off:0}", f.Defaults)
	}
}

func TestConvertDynamicConfigs_WithVariants(t *testing.T) {
	// Rule[0].Variants drives the variation set. dc.defaultValue is appended
	// as "Default" and OnVariation/OffVariation index into the result.
	configs := []statsig.DynamicConfig{
		{
			ID:           "checkout_copy",
			Name:         "Checkout Copy",
			DefaultValue: json.RawMessage(`{"headline":"Buy now"}`),
			Rules: []statsig.DCRule{
				{
					Name:           "variants",
					PassPercentage: 100,
					Variants: []statsig.DCVariant{
						{Name: "Friendly", ReturnValue: json.RawMessage(`{"headline":"Pick yours"}`)},
						{Name: "Direct", ReturnValue: json.RawMessage(`{"headline":"Add to cart"}`)},
					},
				},
			},
		},
	}
	f := ConvertDynamicConfigs(configs, "", "")[0].Flag

	if len(f.Variations) != 3 {
		t.Fatalf("expected 3 variations (Friendly + Direct + Default), got %d: %+v", len(f.Variations), f.Variations)
	}
	if f.Variations[0].Name != "Friendly" || f.Variations[1].Name != "Direct" || f.Variations[2].Name != "Default" {
		t.Errorf("variation order/names wrong: %q %q %q", f.Variations[0].Name, f.Variations[1].Name, f.Variations[2].Name)
	}
	// OffVariation = Default index (2). OnVariation = first non-Default (0).
	if f.Defaults.OffVariation != 2 {
		t.Errorf("OffVariation = %d, want 2 (Default index)", f.Defaults.OffVariation)
	}
	if f.Defaults.OnVariation != 0 {
		t.Errorf("OnVariation = %d, want 0", f.Defaults.OnVariation)
	}
}

func TestConvertDynamicConfigs_VariantCollapsesOntoDefault(t *testing.T) {
	// Variant value == default value → dedup collapses the variant onto the
	// Default index. Test that OffVariation tracks Default correctly post-dedup.
	configs := []statsig.DynamicConfig{
		{
			ID:           "dc",
			Name:         "DC",
			DefaultValue: json.RawMessage(`{"x":1}`),
			Rules: []statsig.DCRule{
				{
					Name:           "v",
					PassPercentage: 100,
					Variants: []statsig.DCVariant{
						{Name: "Identical", ReturnValue: json.RawMessage(`{"x":1}`)},
					},
				},
			},
		},
	}
	f := ConvertDynamicConfigs(configs, "", "")[0].Flag

	// Both variations have the same canonical value → dedup keeps just one,
	// but LD requires ≥2 so an "Empty" filler is appended.
	if len(f.Variations) != 2 {
		t.Fatalf("expected 2 variations after dedup+filler, got %d: %+v", len(f.Variations), f.Variations)
	}
	// The surviving first-named variation should be the variant (it wins on
	// dedup by virtue of appearing first in the in-slice).
	if f.Variations[0].Name != "Identical" {
		t.Errorf("expected first variation \"Identical\" (variant beats default by ordering); got %q", f.Variations[0].Name)
	}
}

func TestConvertDynamicConfigs_NullDefault(t *testing.T) {
	// dc.defaultValue absent or null → wrapped as {"value":null}.
	configs := []statsig.DynamicConfig{{ID: "dc", Name: "DC", DefaultValue: json.RawMessage(`null`)}}
	f := ConvertDynamicConfigs(configs, "", "")[0].Flag

	if len(f.Variations) < 1 {
		t.Fatal("expected at least one variation")
	}
	// First variation should be "Default" with {"value":null}
	gotJSON, _ := json.Marshal(f.Variations[0].Value)
	if string(gotJSON) != `{"value":null}` {
		t.Errorf("Default value = %s, want %s", gotJSON, `{"value":null}`)
	}
}

func TestConvertDynamicConfigs_ScalarDefaultWrapped(t *testing.T) {
	// dc.defaultValue is a scalar (string) → wrapped as {"value":"x"}.
	configs := []statsig.DynamicConfig{{ID: "dc", Name: "DC", DefaultValue: json.RawMessage(`"hello"`)}}
	f := ConvertDynamicConfigs(configs, "", "")[0].Flag

	gotJSON, _ := json.Marshal(f.Variations[0].Value)
	if string(gotJSON) != `{"value":"hello"}` {
		t.Errorf("Default value = %s, want %s", gotJSON, `{"value":"hello"}`)
	}
}

// ===========================================================================
// Variation building helpers
// ===========================================================================

func TestUnwrapVariantValue(t *testing.T) {
	cases := []struct {
		name  string
		input json.RawMessage
		want  json.RawMessage
	}{
		{"empty input becomes null", json.RawMessage{}, json.RawMessage("null")},
		{"non-object passthrough", json.RawMessage(`"hello"`), json.RawMessage(`"hello"`)},
		{"single-key {value:x} unwraps", json.RawMessage(`{"value":"hello"}`), json.RawMessage(`"hello"`)},
		{"single-key {value:42} unwraps", json.RawMessage(`{"value":42}`), json.RawMessage(`42`)},
		{"multi-key object passthrough", json.RawMessage(`{"value":"x","other":1}`), json.RawMessage(`{"value":"x","other":1}`)},
		{"non-value-keyed object passthrough", json.RawMessage(`{"x":1}`), json.RawMessage(`{"x":1}`)},
		{"malformed json passthrough", json.RawMessage(`{notjson`), json.RawMessage(`{notjson`)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := unwrapVariantValue(c.input)
			if string(got) != string(c.want) {
				t.Errorf("unwrapVariantValue(%s) = %s, want %s", c.input, got, c.want)
			}
		})
	}
}

func TestWrapScalarDefault(t *testing.T) {
	cases := []struct {
		name  string
		input json.RawMessage
		want  string
	}{
		{"empty → {value:null}", json.RawMessage{}, `{"value":null}`},
		{"null → {value:null}", json.RawMessage(`null`), `{"value":null}`},
		{"object passes through", json.RawMessage(`{"x":1}`), `{"x":1}`},
		{"string wrapped", json.RawMessage(`"hello"`), `{"value":"hello"}`},
		{"number wrapped", json.RawMessage(`42`), `{"value":42}`},
		{"array wrapped", json.RawMessage(`[1,2]`), `{"value":[1,2]}`},
		{"bool wrapped", json.RawMessage(`true`), `{"value":true}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := wrapScalarDefault(c.input)
			if string(got) != c.want {
				t.Errorf("wrapScalarDefault(%s) = %s, want %s", c.input, got, c.want)
			}
		})
	}
}

func TestCanonicalJSON_KeyOrderInvariant(t *testing.T) {
	// {"a":1,"b":2} and {"b":2,"a":1} must canonicalize identically.
	a := canonicalJSON(json.RawMessage(`{"a":1,"b":2}`))
	b := canonicalJSON(json.RawMessage(`{"b":2,"a":1}`))
	if a != b {
		t.Errorf("canonicalJSON not key-order-invariant: %q vs %q", a, b)
	}
}

func TestCanonicalJSON_NestedObjects(t *testing.T) {
	a := canonicalJSON(json.RawMessage(`{"x":{"a":1,"b":2}}`))
	b := canonicalJSON(json.RawMessage(`{"x":{"b":2,"a":1}}`))
	if a != b {
		t.Errorf("canonicalJSON not invariant for nested objects: %q vs %q", a, b)
	}
}

func TestDedupVariationsByValue_CollapsesDuplicates(t *testing.T) {
	in := []launchdarkly.Variation{
		{Name: "A", Value: json.RawMessage(`{"x":1}`)},
		{Name: "B", Value: json.RawMessage(`{"x":1}`)}, // duplicate of A
		{Name: "C", Value: json.RawMessage(`{"x":2}`)},
	}
	out, newDefault := dedupVariationsByValue(in, 2) // C was the "default"
	if len(out) != 2 {
		t.Errorf("expected 2 distinct variations, got %d", len(out))
	}
	if newDefault != 1 {
		t.Errorf("default index after dedup = %d, want 1", newDefault)
	}
}

func TestDedupVariationsByValue_DefaultCollapsesOntoVariant(t *testing.T) {
	// Default (index 2) duplicates an earlier variant; newDefault should
	// point at the earlier variant's surviving index, not be dropped.
	in := []launchdarkly.Variation{
		{Name: "A", Value: json.RawMessage(`{"x":1}`)},
		{Name: "B", Value: json.RawMessage(`{"x":2}`)},
		{Name: "Default", Value: json.RawMessage(`{"x":1}`)}, // dup of A
	}
	out, newDefault := dedupVariationsByValue(in, 2)
	if len(out) != 2 {
		t.Errorf("expected 2 distinct variations, got %d", len(out))
	}
	if newDefault != 0 {
		t.Errorf("default collapsed onto index %d, want 0 (the earlier dup)", newDefault)
	}
}

// ===========================================================================
// FilterNewFlags — dedup against existing LD flags
// ===========================================================================

func TestFilterNewFlags(t *testing.T) {
	newFlags := []FlagResult{
		{Flag: launchdarkly.Flag{Key: "a"}},
		{Flag: launchdarkly.Flag{Key: "b"}},
		{Flag: launchdarkly.Flag{Key: "c"}},
	}
	existing := []launchdarkly.Flag{{Key: "b"}}
	got := FilterNewFlags(newFlags, existing)
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
	if got[0].Flag.Key != "a" || got[1].Flag.Key != "c" {
		t.Errorf("got %+v, want [a, c]", []string{got[0].Flag.Key, got[1].Flag.Key})
	}
}
