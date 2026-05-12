package converter

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/statsig"
)

// ===========================================================================
// Representative-payload tests
//
// These tests decode raw JSON shaped like real Statsig Console API responses
// (per https://api.statsig.com/openapi/20240601.json and the goaltender
// production fixtures) into our types and then run the full conversion. They
// catch bugs at any point in the pipeline — JSON unmarshaling, field mapping,
// targeting translation — with one assertion per case.
//
// The payloads include fields we don't care about (lastModifierID, owner,
// checksPerHour, etc.) so encoding/json's silent field skipping is also
// covered.
// ===========================================================================

// realStatsigGateResponse is the literal shape returned by GET
// /console/v1/gates, copied from the OpenAPI example and extended with the
// rule shapes the targeting importer cares about (passPercentage,
// conditions, environments).
const realStatsigGateResponse = `{
  "message": "Gates listed successfully.",
  "data": [
    {
      "id": "checkout_new_flow",
      "name": "Checkout: New Flow",
      "description": "Enables the redesigned checkout funnel",
      "idType": "userID",
      "lastModifierID": "4R5PV7mvYdW6NLCwK8ocoz",
      "lastModifiedTime": 1705439406750,
      "lastModifierName": "CONSOLE API",
      "lastModifierEmail": null,
      "creatorID": "4R5PV7mvYdW6NLCwK8ocoz",
      "createdTime": 1705439406615,
      "creatorName": "CONSOLE API",
      "targetApps": [],
      "holdoutIDs": [],
      "tags": ["team-payments", "mobile"],
      "isEnabled": true,
      "status": "In Progress",
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
      "checksPerHour": 0,
      "type": "PERMANENT",
      "typeReason": "NONE",
      "version": 1
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
  ],
  "pagination": {
    "all": "",
    "itemsPerPage": 100,
    "nextPage": null,
    "pageNumber": 1,
    "previousPage": null,
    "totalItems": 2
  }
}`

// realStatsigDCResponse mirrors the GET /console/v1/dynamic_configs shape.
// One config has multi-variant rules + a scalar default; the other is a
// default-only config (the common case from prototyping projects).
const realStatsigDCResponse = `{
  "message": "Dynamic Configs listed successfully.",
  "data": [
    {
      "id": "checkout_copy",
      "name": "Checkout Copy",
      "description": "Headline + button label for the checkout page",
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
  ],
  "pagination": {"itemsPerPage":100,"pageNumber":1,"totalItems":2,"nextPage":null}
}`

// realStatsigEnvironmentsResponse is the GET /console/v1/environments shape
// (with `data` wrapping the environments list, not the bare array).
const realStatsigEnvironmentsResponse = `{
  "data": {
    "environments": [
      {"name":"production","id":"prod","isProduction":true,"requiresReview":true,"requiresReleasePipeline":false},
      {"name":"staging","id":"stg","isProduction":false,"requiresReview":false,"requiresReleasePipeline":false},
      {"name":"development","id":"dev","isProduction":false,"requiresReview":false,"requiresReleasePipeline":false}
    ]
  }
}`

// realStatsigGateOverridesResponse is the GET /console/v1/gates/{id}/overrides
// shape. The body is wrapped in `data` and the `environmentOverrides` field is
// the part we actually care about (legacy passingUserIDs/etc. fields are
// also returned by the API but we don't read them).
const realStatsigGateOverridesResponse = `{
  "message": "Gate Overrides read successfully.",
  "data": {
    "passingUserIDs": ["legacy-user"],
    "failingUserIDs": [],
    "passingCustomIDs": [],
    "failingCustomIDs": [],
    "environmentOverrides": [
      {"environment":"production","unitID":"userID","passingIDs":["alice","bob"],"failingIDs":["carol"]},
      {"environment":null,"unitID":"orgID","passingIDs":["acme"],"failingIDs":[]}
    ]
  }
}`

// ---------------------------------------------------------------------------
// Gate listing → flag conversion end-to-end
// ---------------------------------------------------------------------------

type gateListWrapper struct {
	Data []statsig.Gate `json:"data"`
}

func TestEndToEnd_GateListUnmarshalsAndConverts(t *testing.T) {
	var wrap gateListWrapper
	if err := json.Unmarshal([]byte(realStatsigGateResponse), &wrap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(wrap.Data) != 2 {
		t.Fatalf("expected 2 gates, got %d", len(wrap.Data))
	}

	// Verify gate-level fields survived (these are the ones the converter reads).
	g0 := wrap.Data[0]
	if g0.ID != "checkout_new_flow" || g0.Name != "Checkout: New Flow" {
		t.Errorf("gate 0 fields wrong: id=%q name=%q", g0.ID, g0.Name)
	}
	if g0.Type != "PERMANENT" || wrap.Data[1].Type != "TEMPORARY" {
		t.Errorf("gate types wrong: %q %q", g0.Type, wrap.Data[1].Type)
	}
	if len(g0.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(g0.Rules))
	}
	// rule[0].environments = ["production"] → non-nil pointer to ["production"]
	if g0.Rules[0].Environments == nil || len(*g0.Rules[0].Environments) != 1 || (*g0.Rules[0].Environments)[0] != "production" {
		t.Errorf("rule[0].Environments = %v, want [production]", g0.Rules[0].Environments)
	}
	// rule[1].environments = null → nil pointer (NOT an empty slice)
	if g0.Rules[1].Environments != nil {
		t.Errorf("rule[1].Environments should be nil (null in JSON), got %v", g0.Rules[1].Environments)
	}
	// rule[0].passPercentage = 100 → non-nil
	if g0.Rules[0].PassPercentage == nil || *g0.Rules[0].PassPercentage != 100 {
		t.Errorf("rule[0].PassPercentage = %v, want 100", g0.Rules[0].PassPercentage)
	}
	// Condition targetValue should decode as []any
	if cond := g0.Rules[0].Conditions[0]; cond.Type != "email" || cond.Operator != "str_contains_any" {
		t.Errorf("condition[0] fields wrong: %+v", cond)
	}

	// Run the converter and check the result is sensible.
	results := ConvertGates(wrap.Data, "imported-from-statsig", "")
	if len(results) != 2 {
		t.Fatalf("got %d flag results", len(results))
	}
	checkout := results[0].Flag
	killswitch := results[1].Flag

	if checkout.Key != "checkout_new_flow" {
		t.Errorf("checkout key = %q", checkout.Key)
	}
	if checkout.Temporary {
		t.Error("checkout (PERMANENT) should not be Temporary")
	}
	if !killswitch.Temporary {
		t.Error("killswitch (TEMPORARY) should be Temporary")
	}
	// Tag applied as the trailing import tag
	wantTags := []string{"team-payments", "mobile", "imported-from-statsig"}
	if len(checkout.Tags) != len(wantTags) {
		t.Errorf("checkout.Tags = %v, want %v", checkout.Tags, wantTags)
	}
	for i, want := range wantTags {
		if checkout.Tags[i] != want {
			t.Errorf("checkout.Tags[%d] = %q, want %q", i, checkout.Tags[i], want)
		}
	}
}

// ---------------------------------------------------------------------------
// DC listing → flag conversion end-to-end
// ---------------------------------------------------------------------------

type dcListWrapper struct {
	Data []statsig.DynamicConfig `json:"data"`
}

func TestEndToEnd_DynamicConfigListUnmarshalsAndConverts(t *testing.T) {
	var wrap dcListWrapper
	if err := json.Unmarshal([]byte(realStatsigDCResponse), &wrap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(wrap.Data) != 2 {
		t.Fatalf("got %d configs", len(wrap.Data))
	}

	checkout := wrap.Data[0]
	featureToggle := wrap.Data[1]

	// Scalar default should round-trip as raw JSON (handled by json.RawMessage).
	if string(featureToggle.DefaultValue) != `"fallback-string"` {
		t.Errorf("scalar default = %q, want %q", featureToggle.DefaultValue, `"fallback-string"`)
	}
	// Object default
	if !strings.Contains(string(checkout.DefaultValue), `"headline":"Buy now"`) {
		t.Errorf("checkout default missing headline: %s", checkout.DefaultValue)
	}
	// Variants survived
	if len(checkout.Rules[0].Variants) != 2 {
		t.Fatalf("expected 2 variants, got %d", len(checkout.Rules[0].Variants))
	}

	// Run conversion.
	results := ConvertDynamicConfigs(wrap.Data, "imported-from-statsig", "")
	if len(results) != 2 {
		t.Fatalf("got %d flag results", len(results))
	}

	checkoutFlag := results[0].Flag
	toggleFlag := results[1].Flag

	// checkout_copy has 2 variants + 1 default = 3 variations
	if len(checkoutFlag.Variations) != 3 {
		t.Errorf("checkout: expected 3 variations (Friendly + Direct + Default), got %d: %+v", len(checkoutFlag.Variations), checkoutFlag.Variations)
	}
	if checkoutFlag.Variations[0].Name != "Friendly" || checkoutFlag.Variations[1].Name != "Direct" || checkoutFlag.Variations[2].Name != "Default" {
		t.Errorf("checkout variation names wrong: %q %q %q",
			checkoutFlag.Variations[0].Name, checkoutFlag.Variations[1].Name, checkoutFlag.Variations[2].Name)
	}

	// feature_toggle_v2 has default-only (scalar) → wrapped + filler appended
	if len(toggleFlag.Variations) != 2 {
		t.Errorf("toggle: expected 2 variations (Default + filler), got %d: %+v", len(toggleFlag.Variations), toggleFlag.Variations)
	}
	defaultJSON, _ := json.Marshal(toggleFlag.Variations[0].Value)
	if string(defaultJSON) != `{"value":"fallback-string"}` {
		t.Errorf("toggle wrapped default = %s, want %s", defaultJSON, `{"value":"fallback-string"}`)
	}
}

// ---------------------------------------------------------------------------
// Overrides response → unmarshal check
// ---------------------------------------------------------------------------

func TestEndToEnd_OverridesResponseUnmarshals(t *testing.T) {
	// Match the shape the client parses in fetchOverrides.
	var resp struct {
		Data struct {
			EnvironmentOverrides []statsig.Override `json:"environmentOverrides"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(realStatsigGateOverridesResponse), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	overrides := resp.Data.EnvironmentOverrides
	if len(overrides) != 2 {
		t.Fatalf("got %d overrides", len(overrides))
	}
	// First: environment="production"
	if overrides[0].Environment == nil || *overrides[0].Environment != "production" {
		t.Errorf("overrides[0].Environment = %v, want production", overrides[0].Environment)
	}
	if overrides[0].UnitID != "userID" {
		t.Errorf("overrides[0].UnitID = %q", overrides[0].UnitID)
	}
	// Second: environment=null → nil pointer
	if overrides[1].Environment != nil {
		t.Errorf("overrides[1].Environment should be nil, got %v", overrides[1].Environment)
	}
	if overrides[1].UnitID != "orgID" {
		t.Errorf("overrides[1].UnitID = %q", overrides[1].UnitID)
	}
}

// ---------------------------------------------------------------------------
// Environments response → unmarshal check
// ---------------------------------------------------------------------------

func TestEndToEnd_EnvironmentsResponseUnmarshals(t *testing.T) {
	var resp struct {
		Data struct {
			Environments []statsig.Environment `json:"environments"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(realStatsigEnvironmentsResponse), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Data.Environments) != 3 {
		t.Fatalf("got %d envs", len(resp.Data.Environments))
	}
	if !resp.Data.Environments[0].IsProduction {
		t.Error("production env not flagged as production")
	}
}

// ---------------------------------------------------------------------------
// Pipeline test — gate with targeting through the full conversion path,
// using a hand-built env reconciler. This is the most representative test
// for "did the import work end-to-end?".
// ---------------------------------------------------------------------------

func TestEndToEnd_GateWithTargeting(t *testing.T) {
	// Decode one gate from the representative response.
	var wrap gateListWrapper
	_ = json.Unmarshal([]byte(realStatsigGateResponse), &wrap)
	gate := wrap.Data[0] // checkout_new_flow with 2 rules

	// Reconciler: prod + staging mapped 1:1.
	reconciler := newTestReconciler(map[string]string{
		"production": "production",
		"staging":    "staging",
	})

	// No overrides for this test.
	settings, _ := BuildGateEnvSettings(gate, nil, reconciler)
	if len(settings) != 2 {
		t.Fatalf("expected 2 env settings, got %d", len(settings))
	}

	prodSettings, ok := settings["production"]
	if !ok {
		t.Fatal("missing production settings")
	}
	stagingSettings := settings["staging"]

	// production:
	//   rule[0] env=["production"] → applies to prod, pass=100 → variation 0
	//   rule[1] env=null → applies everywhere, pass=25 → rollout 25/75
	// → 2 rules in prod.
	if len(prodSettings.Rules) != 2 {
		t.Errorf("prod: expected 2 rules, got %d", len(prodSettings.Rules))
	}
	// staging:
	//   rule[0] env=["production"] → does NOT apply to staging
	//   rule[1] env=null → applies everywhere
	// → 1 rule in staging.
	if len(stagingSettings.Rules) != 1 {
		t.Errorf("staging: expected 1 rule (env-scoped rule filtered out), got %d", len(stagingSettings.Rules))
	}

	// Sanity: prod's first rule is the email "Beta cohort" rule with variation 0
	if prodSettings.Rules[0].Description != "Beta cohort" {
		t.Errorf("prod first rule = %q, want \"Beta cohort\"", prodSettings.Rules[0].Description)
	}
	if prodSettings.Rules[0].Variation == nil || *prodSettings.Rules[0].Variation != 0 {
		t.Errorf("Beta cohort variation = %v, want 0 (pass=100)", prodSettings.Rules[0].Variation)
	}
	// And the second rule is the 25% rollout
	if prodSettings.Rules[1].Description != "Gradual rollout" || prodSettings.Rules[1].Rollout == nil {
		t.Errorf("prod second rule wrong: %+v", prodSettings.Rules[1])
	}

	// Off variation = 1 (false) for gates always
	if prodSettings.OffVariation != 1 {
		t.Errorf("OffVariation = %d, want 1", prodSettings.OffVariation)
	}
}

// ---------------------------------------------------------------------------
// JSON-shape integrity for the patched body
//
// After conversion + patch building, the final body LD sees needs to
// serialize correctly. This catches regressions where a field is missing
// `omitempty` and ends up as null where LD wants empty arrays.
// ---------------------------------------------------------------------------

func TestEndToEnd_PatchBodySerializesWithoutNulls(t *testing.T) {
	reconciler := newTestReconciler(map[string]string{"production": "production"})
	gate := statsig.Gate{
		ID:   "g",
		Name: "G",
		Rules: []statsig.GateRule{
			{
				Name:           "us-only",
				PassPercentage: floatPtr(100),
				Conditions:     []statsig.Condition{{Type: "country", Operator: "any", TargetValue: []any{"US"}}},
			},
		},
	}
	settings, _ := BuildGateEnvSettings(gate, nil, reconciler)
	ops := BuildEnvPatchOps("production", settings["production"])
	body, err := json.Marshal(ops)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"targets":null`) || strings.Contains(string(body), `"contextTargets":null`) || strings.Contains(string(body), `"rules":null`) {
		t.Errorf("patch body has null array fields (LD rejects this): %s", body)
	}
}

// Ensure the LDRule emitted serializes the way LD expects (clauses array,
// trackEvents present, variation OR rollout never both nil).
func TestEndToEnd_LDRuleJSONShape(t *testing.T) {
	r := LDRule{
		Description: "test",
		Clauses: []LDClause{
			{ContextKind: "user", Attribute: "country", Op: "in", Values: []any{"US"}},
		},
		Variation: intPtr(0),
	}
	b, _ := json.Marshal(r)
	str := string(b)
	// Required fields
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

// Variation values that come from a Statsig dynamic config arrive as
// json.RawMessage and must marshal back without re-encoding (so the
// downstream LD API sees the original JSON, including key order).
func TestEndToEnd_VariationValueRoundTripsAsRawJSON(t *testing.T) {
	configs := []statsig.DynamicConfig{
		{
			ID:           "dc",
			Name:         "DC",
			DefaultValue: json.RawMessage(`{"a":1,"b":2}`),
		},
	}
	f := ConvertDynamicConfigs(configs, "", "")[0].Flag
	b, err := json.Marshal(f.Variations[0].Value)
	if err != nil {
		t.Fatal(err)
	}
	// canonicalJSON normalizes key order, but the raw round-trip preserves
	// input order. Just check the keys are present.
	if !strings.Contains(string(b), `"a":1`) || !strings.Contains(string(b), `"b":2`) {
		t.Errorf("round-tripped value missing keys: %s", b)
	}
}

// Sanity: launchdarkly.Flag round-trips through JSON. Catches missing
// json tags in our new types.
func TestEndToEnd_LDFlagJSONRoundTrip(t *testing.T) {
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
	if decoded.Key != f.Key || decoded.Temporary != f.Temporary || len(decoded.Variations) != 2 {
		t.Errorf("round-trip mismatch: %+v", decoded)
	}
}
