package launchdarkly

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListRegisteredAnalysisUnits(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"_projectKey":"p","randomizationUnits":[
			{"randomizationUnit":"user","default":true},
			{"randomizationUnit":"company","default":false},
			{"randomizationUnit":""}
		]}`))
	}))
	t.Cleanup(server.Close)

	client := NewClient("api-test", "my-project", server.URL)
	units, err := client.ListRegisteredAnalysisUnits(context.Background())
	if err != nil {
		t.Fatalf("ListRegisteredAnalysisUnits: %v", err)
	}
	if want := "/api/v2/projects/my-project/experimentation-settings"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if len(units) != 2 || units[0] != "user" || units[1] != "company" {
		t.Errorf("units = %v, want [user company] (blank entries dropped)", units)
	}
}

func TestListRegisteredAnalysisUnits_ErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"nope"}`))
	}))
	t.Cleanup(server.Close)

	client := NewClient("api-test", "my-project", server.URL)
	_, err := client.ListRegisteredAnalysisUnits(context.Background())
	if err == nil {
		t.Fatal("expected an error on a non-200 response")
	}
	if !strings.Contains(err.Error(), "experimentation settings") {
		t.Errorf("error should name the operation, got: %v", err)
	}
}
