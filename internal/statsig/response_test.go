package statsig

import (
	"encoding/json"
	"strings"
	"testing"
)

// ============================================================================
// Realistic-payload JSON tests
//
// These tests decode raw JSON shaped like real Statsig Console API responses
// (per https://api.statsig.com/openapi/20240601.json) into our types. They
// catch field-mapping and json-tag regressions that synthetic Go-struct tests
// miss — in particular, fields the API returns that we don't read (forward-
// compat) and the nullable Environments pointer distinguishing null from [].
//
// Ported from Eric Wang's PR #12 (parallel implementation) to fill a coverage
// gap our original PRs had.
// ============================================================================

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

// ----------------------------------------------------------------------------
// Gate list unmarshal
// ----------------------------------------------------------------------------

func TestRealResponse_GateListUnmarshals(t *testing.T) {
	var wrap gatesListResponse
	if err := json.Unmarshal([]byte(realStatsigGateResponse), &wrap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(wrap.Data) != 2 {
		t.Fatalf("expected 2 gates, got %d", len(wrap.Data))
	}

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
	// rule[1].environments = null → nil pointer (NOT an empty slice — distinguishes "all envs" from "no envs")
	if g0.Rules[1].Environments != nil {
		t.Errorf("rule[1].Environments should be nil (null in JSON), got %v", g0.Rules[1].Environments)
	}
	// rule[0].passPercentage = 100 → non-nil
	if g0.Rules[0].PassPercentage == nil || *g0.Rules[0].PassPercentage != 100 {
		t.Errorf("rule[0].PassPercentage = %v, want 100", g0.Rules[0].PassPercentage)
	}
	// Tags slice
	if len(g0.Tags) != 2 || g0.Tags[0] != "team-payments" || g0.Tags[1] != "mobile" {
		t.Errorf("gate tags = %v, want [team-payments mobile]", g0.Tags)
	}
	// Condition fields
	if cond := g0.Rules[0].Conditions[0]; cond.Type != "email" || cond.Operator != "str_contains_any" {
		t.Errorf("condition[0] fields wrong: %+v", cond)
	}

	// Pagination — verify our nullable nextPage handling
	if wrap.Pagination.PageNumber != 1 || wrap.Pagination.TotalItems != 2 {
		t.Errorf("pagination = %+v", wrap.Pagination)
	}
}

// ----------------------------------------------------------------------------
// Dynamic config list unmarshal
// ----------------------------------------------------------------------------

func TestRealResponse_DynamicConfigListUnmarshals(t *testing.T) {
	var wrap dynamicConfigsListResponse
	if err := json.Unmarshal([]byte(realStatsigDCResponse), &wrap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(wrap.Data) != 2 {
		t.Fatalf("got %d configs", len(wrap.Data))
	}

	checkout := wrap.Data[0]
	featureToggle := wrap.Data[1]

	// Scalar default round-trips as raw JSON via json.RawMessage.
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
	if checkout.Rules[0].Variants[0].Name != "Friendly" || checkout.Rules[0].Variants[1].Name != "Direct" {
		t.Errorf("variant names wrong: %+v", checkout.Rules[0].Variants)
	}
	if checkout.Rules[0].Variants[0].PassPercentage != 50 {
		t.Errorf("variant passPercentage = %v, want 50", checkout.Rules[0].Variants[0].PassPercentage)
	}
}

// ----------------------------------------------------------------------------
// Environments response unmarshal — the data-wrapper shape
// ----------------------------------------------------------------------------

func TestRealResponse_EnvironmentsResponseUnmarshals(t *testing.T) {
	var resp environmentsResponse
	if err := json.Unmarshal([]byte(realStatsigEnvironmentsResponse), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Data.Environments) != 3 {
		t.Fatalf("got %d envs", len(resp.Data.Environments))
	}
	if !resp.Data.Environments[0].IsProduction {
		t.Error("production env not flagged as production")
	}
	if !resp.Data.Environments[0].RequiresReview {
		t.Error("production env should require review per fixture")
	}
}

// ----------------------------------------------------------------------------
// Overrides response unmarshal — also a data-wrapper shape
// ----------------------------------------------------------------------------

func TestRealResponse_OverridesResponseUnmarshals(t *testing.T) {
	var resp overridesResponse
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
	if len(overrides[0].PassingIDs) != 2 || overrides[0].PassingIDs[0] != "alice" {
		t.Errorf("overrides[0].PassingIDs = %v", overrides[0].PassingIDs)
	}
	// Second: environment=null → nil pointer
	if overrides[1].Environment != nil {
		t.Errorf("overrides[1].Environment should be nil, got %v", overrides[1].Environment)
	}
	if overrides[1].UnitID != "orgID" {
		t.Errorf("overrides[1].UnitID = %q", overrides[1].UnitID)
	}
}
