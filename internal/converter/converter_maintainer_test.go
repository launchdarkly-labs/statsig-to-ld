package converter

import "testing"

func TestConvert_MaintainerSetOnMetric(t *testing.T) {
	sg := baseMetric("event_count_custom")
	res, err := Convert(sg, Options{MaintainerID: "6917a463c1b64809c1124c34"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.LDMetric.MaintainerID != "6917a463c1b64809c1124c34" {
		t.Errorf("MaintainerID = %q, want the configured member ID", res.LDMetric.MaintainerID)
	}
}

func TestConvert_MaintainerOmittedWhenUnset(t *testing.T) {
	sg := baseMetric("event_count_custom")
	res, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.LDMetric.MaintainerID != "" {
		t.Errorf("MaintainerID = %q, want empty", res.LDMetric.MaintainerID)
	}
}

func TestConvertRatio_MaintainerSet(t *testing.T) {
	sg := ratioMetric("purchase", "count", "page_view", "count")
	res, err := Convert(sg, Options{LDDataSource: "ds", MaintainerID: "6917a463c1b64809c1124c34"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.LDMetric.MaintainerID != "6917a463c1b64809c1124c34" {
		t.Errorf("ratio MaintainerID = %q, want the configured member ID", res.LDMetric.MaintainerID)
	}
}
