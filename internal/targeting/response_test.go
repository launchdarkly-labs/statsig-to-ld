package targeting

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/flag"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
)

// ============================================================================
// End-to-end conversion tests using realistic Statsig API payloads.
//
// Decode raw JSON shaped like real responses → run through flag construction
// + targeting transformation → assert the LD-side shape. These complement
// the synthetic struct-based tests by exercising the JSON-unmarshal path
// the production code actually takes.
//
// The fixture constants are duplicated from internal/statsig/response_test.go
// because Go test fixtures can't be shared across packages without
// promoting them to non-test exports.
//
// Ported from Eric Wang's PR #12 (parallel implementation).
// ============================================================================

const realStatsigGateResponse = `{
  "message": "Gates listed successfully.",
  "data": [
    {
      "id": "checkout_new_flow",
      "name": "Checkout: New Flow",
      "description": "Enables the redesigned checkout funnel",
      "idType": "userID",
      "lastModifierID": "4R5PV7mvYdW6NLCwK8ocoz",
      "tags": ["team-payments", "mobile"],
      "isEnabled": true,
      "rules": [
        {
          "id": "rule-1",
          "name": "Beta cohort",
          "passPercentage": 100,
          "conditions": [
            {"type":"email","operator":"str_contains_any","targetValue":["@example.com"]}
          ],
          "environments": ["production"]
        },
        {
          "id": "rule-2",
          "name": "Gradual rollout",
          "passPercentage": 25,
          "conditions": [
            {"type":"country","operator":"any","targetValue":["US","CA"]}
          ],
          "environments": null
        }
      ],
      "type": "PERMANENT"
    },
    {
      "id": "killswitch",
      "name": "Killswitch",
      "description": "Emergency disable",
      "tags": [],
      "isEnabled": true,
      "type": "TEMPORARY",
      "rules": []
    }
  ]
}`

const realStatsigDCResponse = `{
  "data": [
    {
      "id": "checkout_copy",
      "name": "Checkout Copy",
      "isEnabled": true,
      "tags": ["copy"],
      "defaultValue": {"headline":"Buy now","cta":"Add to cart"},
      "rules": [
        {
          "id": "ruleA",
          "name": "ab-test",
          "passPercentage": 50,
          "conditions": [{"type":"public"}],
          "returnValue": {},
          "variants": [
            {"name":"Friendly","returnValue":{"headline":"Pick yours","cta":"Add it"},"passPercentage":50},
            {"name":"Direct","returnValue":{"headline":"Add to cart","cta":"Buy"},"passPercentage":50}
          ]
        }
      ]
    },
    {
      "id": "feature_toggle_v2",
      "name": "Feature Toggle v2",
      "isEnabled": true,
      "tags": [],
      "defaultValue": "fallback-string",
      "rules": []
    }
  ]
}`

// gateListWrapper / dcListWrapper are local minimal envelopes for the test
// payloads. Production code uses the internal/statsig list-response types
// which include pagination; the relevant subset for these tests is just
// `data`.
type gateListWrapper struct {
	Data []statsig.Gate `json:"data"`
}

type dcListWrapper struct {
	Data []statsig.DynamicConfig `json:"data"`
}

// ----------------------------------------------------------------------------
// Gate listing → flag conversion end-to-end
// ----------------------------------------------------------------------------

func TestRealResponse_GateListConvertsToFlags(t *testing.T) {
	var wrap gateListWrapper
	if err := json.Unmarshal([]byte(realStatsigGateResponse), &wrap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	flags, failed := flag.NewFlagsFromGates(wrap.Data, "imported-from-statsig", "")
	if len(failed) != 0 {
		t.Errorf("unexpected failed flags: %v", failed)
	}
	if len(flags) != 2 {
		t.Fatalf("got %d flags", len(flags))
	}

	checkout := flags[0]
	killswitch := flags[1]

	if checkout.Key != "checkout_new_flow" {
		t.Errorf("checkout key = %q", checkout.Key)
	}
	if checkout.Temporary {
		t.Error("checkout (PERMANENT) should not be Temporary")
	}
	if !killswitch.Temporary {
		t.Error("killswitch (TEMPORARY) should be Temporary")
	}

	// Tag applied as the trailing import tag, alongside the original gate tags.
	wantTags := []string{"team-payments", "mobile", "imported-from-statsig"}
	if !slices.Equal(checkout.Tags, wantTags) {
		t.Errorf("checkout.Tags = %v, want %v", checkout.Tags, wantTags)
	}
}

// ----------------------------------------------------------------------------
// DC listing → flag conversion end-to-end
// ----------------------------------------------------------------------------

func TestRealResponse_DynamicConfigListConvertsToFlags(t *testing.T) {
	var wrap dcListWrapper
	if err := json.Unmarshal([]byte(realStatsigDCResponse), &wrap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	flags, _ := flag.NewFlagsFromDynamicConfigs(wrap.Data, "imported-from-statsig", "")
	if len(flags) != 2 {
		t.Fatalf("got %d flags", len(flags))
	}

	checkoutFlag := flags[0]
	toggleFlag := flags[1]

	// checkout_copy has 2 variants + 1 default = 3 variations.
	if len(checkoutFlag.Variations) != 3 {
		t.Errorf("checkout: expected 3 variations (Friendly + Direct + Default), got %d: %+v",
			len(checkoutFlag.Variations), checkoutFlag.Variations)
	}
	wantNames := []string{"Friendly", "Direct", "Default"}
	for i, name := range wantNames {
		if checkoutFlag.Variations[i].Name != name {
			t.Errorf("checkout variation[%d].Name = %q, want %q", i, checkoutFlag.Variations[i].Name, name)
		}
	}

	// feature_toggle_v2 has scalar-default only → wrapped + filler appended (≥2 variations required by LD).
	if len(toggleFlag.Variations) != 2 {
		t.Errorf("toggle: expected 2 variations (Default + filler), got %d: %+v",
			len(toggleFlag.Variations), toggleFlag.Variations)
	}
	// Wrapped scalar default serializes as {"value":"fallback-string"}.
	defaultJSON, _ := json.Marshal(toggleFlag.Variations[0].Value)
	if string(defaultJSON) != `{"value":"fallback-string"}` {
		t.Errorf("toggle wrapped default = %s, want %s", defaultJSON, `{"value":"fallback-string"}`)
	}
}

// ----------------------------------------------------------------------------
// Variation values round-trip as raw JSON so LD sees the original bytes.
// ----------------------------------------------------------------------------

func TestRealResponse_VariationValueRoundTripsAsRawJSON(t *testing.T) {
	configs := []statsig.DynamicConfig{
		{ID: "dc", Name: "DC", DefaultValue: json.RawMessage(`{"a":1,"b":2}`)},
	}
	flags, _ := flag.NewFlagsFromDynamicConfigs(configs, "", "")
	if len(flags) == 0 {
		t.Fatal("no flag produced")
	}

	b, err := json.Marshal(flags[0].Variations[0].Value)
	if err != nil {
		t.Fatal(err)
	}
	// canonicalJSON in dedup normalizes key order internally, but the raw value
	// surface preserves whatever JSON bytes came in.
	if !strings.Contains(string(b), `"a":1`) || !strings.Contains(string(b), `"b":2`) {
		t.Errorf("round-tripped value missing keys: %s", b)
	}
}

// ----------------------------------------------------------------------------
// Pipeline test — gate JSON through to per-env LD targeting settings.
// ----------------------------------------------------------------------------

func TestRealResponse_GateWithTargeting(t *testing.T) {
	var wrap gateListWrapper
	_ = json.Unmarshal([]byte(realStatsigGateResponse), &wrap)
	gate := wrap.Data[0] // checkout_new_flow

	r := newFakeReconciler(map[string]string{
		"production": "production",
		"staging":    "staging",
	})

	settings, _ := BuildGateEnvSettings(gate, nil, r)
	if len(settings) != 2 {
		t.Fatalf("expected 2 env settings, got %d", len(settings))
	}

	prod := settings["production"]
	staging := settings["staging"]

	// production:
	//   rule[0] env=["production"] applies; pass=100 → variation 0
	//   rule[1] env=null applies everywhere; pass=25 → rollout 25/75
	// → 2 rules in prod.
	if len(prod.Rules) != 2 {
		t.Errorf("prod: expected 2 rules, got %d", len(prod.Rules))
	}
	if prod.Rules[0].Description != "Beta cohort" {
		t.Errorf("prod first rule = %q", prod.Rules[0].Description)
	}
	if prod.Rules[0].Variation == nil || *prod.Rules[0].Variation != 0 {
		t.Errorf("Beta cohort variation = %v, want 0 (pass=100)", prod.Rules[0].Variation)
	}
	if prod.Rules[1].Rollout == nil {
		t.Errorf("Gradual rollout (pass=25) should produce a rollout, got %+v", prod.Rules[1])
	}

	// staging:
	//   rule[0] env=["production"] does NOT apply
	//   rule[1] env=null applies everywhere
	// → 1 rule in staging.
	if len(staging.Rules) != 1 {
		t.Errorf("staging: expected 1 rule (env-scoped rule filtered), got %d", len(staging.Rules))
	}

	if prod.OffVariation != 1 {
		t.Errorf("OffVariation = %d, want 1 (gate false variation)", prod.OffVariation)
	}
}

// ----------------------------------------------------------------------------
// Patch-body JSON shape — LD rejects null arrays where empty arrays belong.
// ----------------------------------------------------------------------------

func TestRealResponse_PatchBodySerializesWithoutNulls(t *testing.T) {
	r := newFakeReconciler(map[string]string{"production": "production"})
	pp100 := 100.0
	gate := statsig.Gate{
		ID:   "g",
		Name: "G",
		Rules: []statsig.GateRule{{
			Name:           "us-only",
			PassPercentage: &pp100,
			Conditions:     []statsig.Condition{{Type: "country", Operator: "any", TargetValue: []any{"US"}}},
		}},
	}
	settings, _ := BuildGateEnvSettings(gate, nil, r)
	ops := BuildEnvPatchOps("production", settings["production"])

	body, err := json.Marshal(ops)
	if err != nil {
		t.Fatal(err)
	}
	for _, badPattern := range []string{`"targets":null`, `"contextTargets":null`, `"rules":null`} {
		if strings.Contains(string(body), badPattern) {
			t.Errorf("patch body has %q (LD rejects null arrays): %s", badPattern, body)
		}
	}
}

// ----------------------------------------------------------------------------
// LD Rule JSON shape — required fields present, optional fields omitted.
// ----------------------------------------------------------------------------

func TestRealResponse_RuleJSONShape(t *testing.T) {
	r := Rule{
		Description: "test",
		Clauses: []Clause{
			{ContextKind: "user", Attribute: "country", Op: "in", Values: []any{"US"}},
		},
		Variation: intPtr(0),
	}
	b, _ := json.Marshal(r)
	str := string(b)

	// Required: description, clauses array, variation, trackEvents
	for _, want := range []string{`"description":"test"`, `"clauses":[`, `"variation":0`, `"trackEvents":false`} {
		if !strings.Contains(str, want) {
			t.Errorf("missing %s in: %s", want, str)
		}
	}
	// Rollout omitted when nil (omitempty)
	if strings.Contains(str, `"rollout":`) {
		t.Errorf("rollout should be omitted when nil: %s", str)
	}
}

// ----------------------------------------------------------------------------
// LD Flag JSON round-trip — sanity check the launchdarkly.Flag tags hold up
// under encode/decode cycle. Catches missing json tags.
// ----------------------------------------------------------------------------

func TestRealResponse_LDFlagJSONRoundTrip(t *testing.T) {
	f := launchdarkly.Flag{
		Key:         "test_flag",
		Name:        "Test Flag",
		Description: "desc",
		Tags:        []string{"a", "b"},
		Temporary:   true,
		Defaults:    launchdarkly.Defaults{OnVariation: 0, OffVariation: 1},
		Variations: []launchdarkly.Variation{
			{Name: "true", Value: true},
			{Name: "false", Value: false},
		},
	}
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	var decoded launchdarkly.Flag
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Key != f.Key {
		t.Errorf("Key round-trip: %q → %q", f.Key, decoded.Key)
	}
	if decoded.Temporary != f.Temporary {
		t.Errorf("Temporary round-trip: %v → %v", f.Temporary, decoded.Temporary)
	}
	if len(decoded.Variations) != len(f.Variations) {
		t.Errorf("Variations count: %d → %d", len(f.Variations), len(decoded.Variations))
	}
	if !slices.Equal(decoded.Tags, f.Tags) {
		t.Errorf("Tags round-trip: %v → %v", f.Tags, decoded.Tags)
	}
}
