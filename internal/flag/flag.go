// Package flag converts Statsig feature gates and dynamic configs into
// LaunchDarkly flag-shell payloads. Targeting transformations (rules,
// rollouts, overrides) are produced separately in internal/targeting; this
// package only builds the flag definition itself.
package flag

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
)

// gateTypeTemporary is the Statsig gate type that maps to LD's `temporary` flag attribute.
const gateTypeTemporary = "TEMPORARY"

// ============================================================================
// Gate → Flag
// ============================================================================

// NewFlagsFromGates converts Statsig feature gates into boolean LD flags.
// Variations are always [{true}, {false}]. OnVariation = 0, OffVariation = 1.
// The provided tag (if non-empty) is appended to each flag's Tags after
// sanitization; maintainerID is set on every flag.
func NewFlagsFromGates(gates []statsig.Gate, tag, maintainerID string) ([]launchdarkly.Flag, []launchdarkly.FailedFlag) {
	flags := make([]launchdarkly.Flag, 0, len(gates))
	for _, g := range gates {
		flags = append(flags, launchdarkly.Flag{
			Defaults:     launchdarkly.Defaults{OnVariation: 0, OffVariation: 1},
			Description:  g.Description,
			Key:          SanitizeKey(g.ID),
			MaintainerID: maintainerID,
			Name:         g.Name,
			Tags:         SanitizeTags(appendNonEmpty(g.Tags, tag)),
			Temporary:    g.Type == gateTypeTemporary,
			Variations: []launchdarkly.Variation{
				{Name: "true", Value: true},
				{Name: "false", Value: false},
			},
		})
	}
	return flags, []launchdarkly.FailedFlag{}
}

// ============================================================================
// DynamicConfig → Flag
// ============================================================================

// NewFlagsFromDynamicConfigs converts Statsig Dynamic Configs into multi-variate
// JSON flags. Variations are derived from the first rule's Variants array (newer
// Statsig API) plus the dc-level DefaultValue, with scalar/null defaults wrapped
// as {"value": <raw>}. Duplicates are removed by canonical JSON form. An
// "Empty" filler is appended when the config produces only a single variation,
// since LD requires ≥2.
func NewFlagsFromDynamicConfigs(configs []statsig.DynamicConfig, tag, maintainerID string) ([]launchdarkly.Flag, []launchdarkly.FailedFlag) {
	flags := make([]launchdarkly.Flag, 0, len(configs))
	for _, c := range configs {
		variations, defaults := newVariationsFromDynamicConfig(c)
		flags = append(flags, launchdarkly.Flag{
			Defaults:     defaults,
			Description:  c.Description,
			Key:          SanitizeKey(c.ID),
			MaintainerID: maintainerID,
			Name:         c.Name,
			Tags:         SanitizeTags(appendNonEmpty(c.Tags, tag)),
			Temporary:    false,
			Variations:   variations,
		})
	}
	return flags, []launchdarkly.FailedFlag{}
}

// newVariationsFromDynamicConfig builds the LD variations + defaults pair from
// a Statsig Dynamic Config. See the package doc for the algorithm.
func newVariationsFromDynamicConfig(config statsig.DynamicConfig) ([]launchdarkly.Variation, launchdarkly.Defaults) {
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
		Name:  "Default",
		Value: wrapScalarDefault(config.DefaultValue),
	})

	variations, defaultIndex = dedupVariationsByValue(variations, defaultIndex)

	if len(variations) == 0 {
		variations = []launchdarkly.Variation{{Name: "Default", Value: json.RawMessage(`{}`)}}
		defaultIndex = 0
	}

	// LD JSON multi-variate flags require ≥2 variations. Statsig DCs with a
	// single distinct value (default-only configs, or variants whose values
	// all collapsed onto the default in dedup) get a JSON-object filler so
	// the flag is creatable. If the sole variation already canonicalizes to
	// {}, fall back to {"value":null} so the two stay distinct.
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
// flags always see an object at the top level.
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
// json.Marshal then emits keys in alphabetical order.
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

// ============================================================================
// Dedupe (D6: by sanitized key, not by name)
// ============================================================================

// FilterNewFlags returns the subset of newFlags whose Key does not already
// appear in existingFlags. Dedupes by sanitized LD key, not by display name —
// this is a behavioral change from the goaltender worker (which dedupes by
// Name) and ensures re-runs are safe even if the Statsig source's display
// name changes between runs. See decision D6.
func FilterNewFlags(newFlags, existingFlags []launchdarkly.Flag) []launchdarkly.Flag {
	existing := make(map[string]struct{}, len(existingFlags))
	for _, f := range existingFlags {
		existing[f.Key] = struct{}{}
	}

	out := make([]launchdarkly.Flag, 0, len(newFlags))
	for _, f := range newFlags {
		if _, ok := existing[f.Key]; ok {
			continue
		}
		out = append(out, f)
	}
	return out
}

// ============================================================================
// Sanitization helpers
// ============================================================================

var (
	// invalidKeyChars matches any char outside the LD-allowed alphabet for
	// keys and tags ([a-zA-Z0-9\-_.]). Used to sanitize both.
	invalidKeyChars      = regexp.MustCompile(`[^a-zA-Z0-9\-_.]`)
	keyLeadingAlphanum   = regexp.MustCompile(`^[a-zA-Z0-9]`)
	keyRepeatedUnderbars = regexp.MustCompile(`_+`)
)

// SanitizeKey ports the migration CLI's sanitizeLaunchDarklyKey. LD keys must
// contain only letters, numbers, '-', '_', or '.', and must start with an
// alphanumeric. Algorithm: replace invalid chars with '_', prepend "ld_" if
// leading non-alnum, trim leading/trailing '_', collapse repeated '_', fall
// back to "ld_flag" if empty.
func SanitizeKey(key string) string {
	s := invalidKeyChars.ReplaceAllString(key, "_")
	if !keyLeadingAlphanum.MatchString(s) {
		s = "ld_" + s
	}
	s = strings.Trim(s, "_")
	s = keyRepeatedUnderbars.ReplaceAllString(s, "_")
	if s == "" {
		return "ld_flag"
	}
	return s
}

// SanitizeTags ports the migration CLI's sanitizeLaunchDarklyTags. Each tag:
// replace invalid chars with '_', trim leading/trailing '_', collapse repeated
// '_', cap at 64 chars, fall back to "tag" if empty. Output is deduped while
// preserving order.
func SanitizeTags(tags []string) []string {
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		s := invalidKeyChars.ReplaceAllString(t, "_")
		s = strings.Trim(s, "_")
		s = keyRepeatedUnderbars.ReplaceAllString(s, "_")
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

func appendNonEmpty(tags []string, extra string) []string {
	if extra == "" {
		return tags
	}
	return append(tags, extra)
}
