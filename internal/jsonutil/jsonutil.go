// Package jsonutil provides typed accessors for map[string]any values,
// commonly used when working with dynamic JSON API responses.
package jsonutil

import "encoding/json"

// GetStr returns the string value for key, or "" if missing/wrong type.
func GetStr(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if ok {
		return s
	}
	return ""
}

// GetMap returns the nested map for key, or nil if missing/wrong type.
func GetMap(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	mm, ok := v.(map[string]any)
	if ok {
		return mm
	}
	return nil
}

// GetSlice returns the slice for key, or nil if missing/wrong type.
func GetSlice(m map[string]any, key string) []any {
	if m == nil {
		return nil
	}
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	s, ok := v.([]any)
	if ok {
		return s
	}
	return nil
}

// GetBool returns the bool value for key, or false if missing/wrong type.
func GetBool(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	v, ok := m[key]
	if !ok || v == nil {
		return false
	}
	b, ok := v.(bool)
	if ok {
		return b
	}
	return false
}

// GetFloat returns the float64 value for key, or 0 if missing/wrong type.
func GetFloat(m map[string]any, key string) float64 {
	if m == nil {
		return 0
	}
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	f, ok := v.(float64)
	if ok {
		return f
	}
	return 0
}

// GetStrSlice returns a string slice for key, filtering non-string elements.
func GetStrSlice(m map[string]any, key string) []string {
	raw := GetSlice(m, key)
	if raw == nil {
		return nil
	}
	result := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// Truncate returns s truncated to n bytes.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ToJSON marshals v to a JSON string, returning the fmt representation on error.
func ToJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// ExtractItemsList extracts the "items" array from a paginated API response.
func ExtractItemsList(body map[string]any) []map[string]any {
	raw := GetSlice(body, "items")
	if raw == nil {
		return nil
	}
	items := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		if m, ok := v.(map[string]any); ok {
			items = append(items, m)
		}
	}
	return items
}

// ExtractMapSlice extracts a key containing a slice of maps.
func ExtractMapSlice(data map[string]any, key string) []map[string]any {
	raw := GetSlice(data, key)
	if raw == nil {
		return nil
	}
	result := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		if m, ok := v.(map[string]any); ok {
			result = append(result, m)
		}
	}
	return result
}
