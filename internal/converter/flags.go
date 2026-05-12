// Statsig→LD flag conversion. Ported from goaltender flag_import_worker/flag.go
// (PRs #825, #828, #829). Lambda-specific scaffolding stripped; gate/DC
// variation building is pure and deterministic.
package converter

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/statsig"
)

// FlagResult holds one converted flag plus any per-flag notes.
type FlagResult struct {
	Flag  launchdarkly.Flag
	Notes []launchdarkly.FailedFlag
}

// ConvertGates converts Statsig feature gates into LD boolean flags. Variation
// names are "true"/"false"; Temporary is derived from gate.Type; key + tags
// sanitized to LD requirements.
func ConvertGates(gates []statsig.Gate, tag, maintainerID string) []FlagResult {
	out := make([]FlagResult, 0, len(gates))
	for _, g := range gates {
		out = append(out, FlagResult{
			Flag: launchdarkly.Flag{
				Defaults:     launchdarkly.Defaults{OnVariation: 0, OffVariation: 1},
				Description:  g.Description,
				Key:          SanitizeFlagKey(g.ID),
				MaintainerID: maintainerID,
				Name:         g.Name,
				Tags:         SanitizeFlagTags(appendNonEmpty(g.Tags, tag)),
				Temporary:    g.Type == statsig.GateTypeTemporary,
				Variations: []launchdarkly.Variation{
					{Name: "true", Value: true},
					{Name: "false", Value: false},
				},
			},
		})
	}
	return out
}

// ConvertDynamicConfigs converts Statsig dynamic configs into LD JSON
// multi-variate flags. Variations come from the first rule's Variants array
// (newer Statsig API) plus the dc-level DefaultValue, with scalar/null
// defaults wrapped as {"value": <raw>}, an "Empty" fallback when the config
// produces zero variations, and dedup by canonical JSON.
func ConvertDynamicConfigs(configs []statsig.DynamicConfig, tag, maintainerID string) []FlagResult {
	out := make([]FlagResult, 0, len(configs))
	for _, c := range configs {
		variations, defaults := variationsFromDynamicConfig(c)
		out = append(out, FlagResult{
			Flag: launchdarkly.Flag{
				Defaults:     defaults,
				Description:  c.Description,
				Key:          SanitizeFlagKey(c.ID),
				MaintainerID: maintainerID,
				Name:         c.Name,
				Tags:         SanitizeFlagTags(appendNonEmpty(c.Tags, tag)),
				Temporary:    false,
				Variations:   variations,
			},
		})
	}
	return out
}

// FilterNewFlags returns flags from `newFlags` whose Key is not already present
// in `existingFlags`. Used to dedup an import against the live LD project so
// the user can re-run safely. Matches goaltender's behavior.
func FilterNewFlags(newFlags []FlagResult, existingFlags []launchdarkly.Flag) []FlagResult {
	existing := make(map[string]struct{}, len(existingFlags))
	for _, f := range existingFlags {
		existing[f.Key] = struct{}{}
	}
	out := make([]FlagResult, 0, len(newFlags))
	for _, f := range newFlags {
		if _, ok := existing[f.Flag.Key]; !ok {
			out = append(out, f)
		}
	}
	return out
}

func appendNonEmpty(tags []string, extra string) []string {
	if extra == "" {
		return tags
	}
	return append(tags, extra)
}

// variationsFromDynamicConfig builds LD variations from a Dynamic Config
// (port of the statsig-migration CLI / goaltender):
//  1. If the first rule has a Variants array, each variant becomes a variation.
//     A variant value of the form {"value": x} is unwrapped to x.
//  2. Always append a "Default" variation from dc.DefaultValue. Wrap scalar /
//     null / non-object defaults as {"value": <raw>} so LD always sees an
//     object.
//  3. If no variations were produced, fall back to {Name:"Empty", Value:{}}.
//  4. Dedup by canonical JSON of value.
//  5. OnVariation = first non-Default index (or 0). OffVariation = index of
//     Default after dedup.
func variationsFromDynamicConfig(config statsig.DynamicConfig) ([]launchdarkly.Variation, launchdarkly.Defaults) {
	variations := make([]launchdarkly.Variation, 0, 4)

	if len(config.Rules) > 0 {
		for _, v := range config.Rules[0].Variants {
			variations = append(variations, launchdarkly.Variation{
				Name:  v.Name,
				Value: unwrapVariantValue(v.ReturnValue),
			})
		}
	}

	defaultIndex := len(variations)
	variations = append(variations, launchdarkly.Variation{
		Name:  defaultVariantName,
		Value: wrapScalarDefault(config.DefaultValue),
	})

	variations, defaultIndex = dedupVariationsByValue(variations, defaultIndex)

	if len(variations) == 0 {
		variations = []launchdarkly.Variation{{Name: defaultVariantName, Value: json.RawMessage(`{}`)}}
		defaultIndex = 0
	}

	// LD JSON multi-variate flags require ≥2 variations. DCs with a single
	// distinct value (default-only configs, or variants whose values all
	// collapsed onto the default in dedup) get a JSON-object filler so the
	// flag is creatable. If the sole variation already canonicalizes to {},
	// fall back to {"value":null} so the two stay distinct.
	if len(variations) == 1 {
		filler := json.RawMessage(`{}`)
		if canonicalJSON(variations[0].Value) == canonicalJSON(filler) {
			filler = json.RawMessage(`{"value":null}`)
		}
		variations = append(variations, launchdarkly.Variation{Name: "Empty", Value: filler})
	}

	on := 0
	if defaultIndex == 0 && len(variations) > 1 {
		on = 1
	}
	return variations, launchdarkly.Defaults{OnVariation: on, OffVariation: defaultIndex}
}

// unwrapVariantValue returns the inner value when raw is a JSON object with
// exactly one key "value" (the migration CLI's RETURN_VALUE_WRAP_ATTRIBUTE
// convention used to box scalars Statsig can't store directly). Anything else
// — non-objects, multi-key objects, malformed JSON — is returned unchanged.
// Empty input becomes JSON null.
func unwrapVariantValue(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	if raw[0] != '{' {
		return raw
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return raw
	}
	if inner, ok := probe["value"]; ok && len(probe) == 1 {
		return inner
	}
	return raw
}

// wrapScalarDefault returns the dc-level defaultValue in object form. Scalar /
// non-object / null defaults are wrapped as {"value": x} so LD JSON-variation
// flags always see an object.
func wrapScalarDefault(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{"value":null}`)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return json.RawMessage(fmt.Sprintf(`{"value":%s}`, string(raw)))
	}
	if decoded == nil {
		return json.RawMessage(`{"value":null}`)
	}
	if _, isMap := decoded.(map[string]any); isMap {
		return raw
	}
	return json.RawMessage(fmt.Sprintf(`{"value":%s}`, string(raw)))
}

// dedupVariationsByValue removes variations whose canonical JSON value matches
// an earlier variation. Returns the deduped slice and the new index of the
// variation originally at defaultIndex (collapsed onto its first-seen
// equivalent if it duplicated an earlier value).
func dedupVariationsByValue(in []launchdarkly.Variation, defaultIndex int) ([]launchdarkly.Variation, int) {
	out := make([]launchdarkly.Variation, 0, len(in))
	canonicalIndex := make(map[string]int, len(in))
	newDefault := defaultIndex
	for i, v := range in {
		canon := canonicalJSON(v.Value)
		if existing, dup := canonicalIndex[canon]; dup {
			if i == defaultIndex {
				newDefault = existing
			}
			continue
		}
		if i == defaultIndex {
			newDefault = len(out)
		}
		canonicalIndex[canon] = len(out)
		out = append(out, v)
	}
	return out, newDefault
}

// canonicalJSON returns a canonical form of v's JSON suitable for byte-equal
// dedup. Round-tripping through `any` decodes JSON objects to map[string]any;
// json.Marshal then emits keys in alphabetical order, so {"a":1,"b":2} and
// {"b":2,"a":1} produce the same output.
func canonicalJSON(v any) string {
	raw, ok := v.(json.RawMessage)
	if !ok {
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		raw = b
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return string(raw)
	}
	out, err := json.Marshal(decoded)
	if err != nil {
		return string(raw)
	}
	return string(out)
}

// buildVariantNameToIndex builds a name→index map from a Flag's variations
// slice. Used by DC rule conversion to resolve variant references that may
// have shifted after dedup.
//
// On ties (multiple variants collapsed onto one post-dedup variation), the
// surviving variation's Name wins. Callers reference variants by their
// original Statsig name; if that name no longer maps, the rule is dropped.
func buildVariantNameToIndex(variations []launchdarkly.Variation) map[string]int {
	out := make(map[string]int, len(variations))
	for i, v := range variations {
		if _, exists := out[v.Name]; !exists {
			out[v.Name] = i
		}
	}
	return out
}

var (
	// flagKeyInvalidChars matches any char outside the LD-allowed alphabet for
	// keys and tags ([a-zA-Z0-9\-_.]). Used to sanitize both keys and tags.
	flagKeyInvalidChars      = regexp.MustCompile(`[^a-zA-Z0-9\-_.]`)
	flagKeyLeadingPattern    = regexp.MustCompile(`^[a-zA-Z0-9]`)
	flagKeyRepeatedUnderscore = regexp.MustCompile(`_+`)
)

// SanitizeFlagKey ports the migration CLI's sanitizeLaunchDarklyKey. LD keys
// must contain only letters, numbers, '-', '_', or '.', and must start with
// an alphanumeric. Algorithm: replace invalid chars with '_', prepend "ld_"
// if leading non-alnum, trim leading/trailing '_', collapse repeated '_',
// fall back to "ld_flag" if empty.
func SanitizeFlagKey(key string) string {
	s := flagKeyInvalidChars.ReplaceAllString(key, "_")
	if !flagKeyLeadingPattern.MatchString(s) {
		s = "ld_" + s
	}
	s = strings.Trim(s, "_")
	s = flagKeyRepeatedUnderscore.ReplaceAllString(s, "_")
	if s == "" {
		return "ld_flag"
	}
	return s
}

// SanitizeFlagTags ports the migration CLI's sanitizeLaunchDarklyTags. Each
// tag: replace invalid chars with '_', trim leading/trailing '_', collapse
// repeated '_', cap at 64 chars, fall back to "tag" if empty. Output is
// deduped while preserving order.
func SanitizeFlagTags(tags []string) []string {
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		s := flagKeyInvalidChars.ReplaceAllString(t, "_")
		s = strings.Trim(s, "_")
		s = flagKeyRepeatedUnderscore.ReplaceAllString(s, "_")
		if len(s) > 64 {
			s = s[:64]
		}
		if s == "" {
			s = "tag"
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
