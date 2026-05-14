package launchdarkly

// JSONPatchOp is a single RFC 6902 JSON Patch operation. Used to PATCH a
// flag's per-environment configuration without sending the whole flag body.
type JSONPatchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}

// EscapeJSONPointer escapes a string for use as a single JSON Pointer
// (RFC 6901) reference token. Per the spec '~' must be encoded as '~0' and
// '/' as '~1', applied in that order so '~1' in the input doesn't become '/'
// after escaping '~' first. Used when constructing patch paths like
// /environments/<envKey>/rules where the env key may contain those characters.
func EscapeJSONPointer(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '~':
			out = append(out, '~', '0')
		case '/':
			out = append(out, '~', '1')
		default:
			out = append(out, s[i])
		}
	}
	return string(out)
}
