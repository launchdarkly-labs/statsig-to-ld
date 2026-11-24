package warehouse

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// mappingsJSON decodes column mappings the way the LaunchDarkly client returns them.
func mappingsJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestEventPropertyColumns_AppliesLaunchDarklysColumnRules(t *testing.T) {
	long, longest := strings.Repeat("a", 256), strings.Repeat("b", 255)
	cm := mappingsJSON(t, `{"timestampColumn":"TS","contexts":{"user":"USER_ID"},"columns":[
		{"name":"TS","type":"TIMESTAMP_NTZ"},
		{"name":"USER_ID","type":"TEXT"},
		{"name":"DEVICE","type":"TEXT"},
		{"name":"PLAN","type":"VARCHAR(16777216)"},
		{"name":"IS_PRO","type":"BOOLEAN"},
		{"name":"SEATS","type":"FIXED","precision":38,"scale":0},
		{"name":"PRICE","type":"FIXED","precision":38,"scale":2},
		{"name":"LEGACY","type":"NUMBER"},
		{"name":"SHARD","type":"Nullable(Int32)"},
		{"name":"REGION","type":"LowCardinality(String)"},
		{"name":"TAGS","type":"ARRAY<STRING>"},
		{"name":"AMOUNT","type":"FLOAT"},
		{"name":"UNTYPED","type":""},
		{"name":"`+long+`","type":"TEXT"},
		{"name":"`+longest+`","type":"TEXT"}]}`)

	want := []EventPropertyColumn{
		{Name: "TS", Unsupported: "it is the timestamp column"},
		{Name: "USER_ID", Unsupported: "it is the user key column"},
		{Name: "DEVICE"},
		{Name: "PLAN"},
		{Name: "IS_PRO"},
		{Name: "SEATS"},
		{Name: "PRICE", Unsupported: `its type is "FIXED" with scale 2`},
		{Name: "LEGACY", Unsupported: `its type is "NUMBER" with no recorded scale`},
		{Name: "SHARD"},
		{Name: "REGION"},
		{Name: "TAGS", Unsupported: `its type is "ARRAY<STRING>"`},
		{Name: "AMOUNT", Unsupported: `its type is "FLOAT"`},
		{Name: "UNTYPED", Unsupported: "it has no recorded type"},
		{Name: long, Unsupported: "its name is longer than 255 characters"},
		{Name: longest},
	}
	if got := EventPropertyColumns(cm); !reflect.DeepEqual(got, want) {
		t.Errorf("EventPropertyColumns =\n%+v\nwant\n%+v", got, want)
	}
}

func TestEventPropertyColumns_NoColumnsIsNil(t *testing.T) {
	if got := EventPropertyColumns(mappingsJSON(t, `{"timestampColumn":"TS","contexts":{"user":"USER_ID"}}`)); got != nil {
		t.Errorf("EventPropertyColumns = %+v, want nil", got)
	}
	if got := EventPropertyColumns(nil); got != nil {
		t.Errorf("EventPropertyColumns(nil) = %+v, want nil", got)
	}
}

// LaunchDarkly rejects a name when any column by that name breaks a rule.
func TestEventPropertyColumns_RepeatedNameTakesTheRejection(t *testing.T) {
	cm := mappingsJSON(t, `{"columns":[
		{"name":"DEVICE","type":"TEXT"},{"name":"DEVICE","type":"ARRAY<STRING>"},{"name":"PLAN","type":"TEXT"},{"name":"PLAN","type":"TEXT"}]}`)
	want := []EventPropertyColumn{{Name: "DEVICE", Unsupported: `its type is "ARRAY<STRING>"`}, {Name: "PLAN"}}
	if got := EventPropertyColumns(cm); !reflect.DeepEqual(got, want) {
		t.Errorf("EventPropertyColumns = %+v, want %+v", got, want)
	}
}

// LaunchDarkly compares names exactly, so only the exact timestamp or key column is ruled out.
func TestEventPropertyColumns_KeyColumnsMatchExactly(t *testing.T) {
	cm := mappingsJSON(t, `{"timestampColumn":"ts","contexts":{"user":"user_id"},"columns":[
		{"name":"TS","type":"TEXT"},{"name":"USER_ID","type":"TEXT"}]}`)
	want := []EventPropertyColumn{{Name: "TS"}, {Name: "USER_ID"}}
	if got := EventPropertyColumns(cm); !reflect.DeepEqual(got, want) {
		t.Errorf("EventPropertyColumns = %+v, want %+v", got, want)
	}
}
