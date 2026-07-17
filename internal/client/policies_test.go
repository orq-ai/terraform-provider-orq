package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func policyJSON(projectID string) map[string]any {
	return map[string]any{
		"_id":           "pol_1",
		"display_name":  "prod policy",
		"description":   "primary",
		"enabled":       true,
		"project_id":    projectID,
		"slug":          "prod-policy",
		"timeout":       300000,
		"evaluators":    []map[string]any{{"id": "ev_1", "execute_on": "input", "sample_rate": 0.5, "options": map[string]any{"k": "v"}}},
		"limits":        map[string]any{"requests": map[string]any{"amount": 10, "period": "day"}},
		"models_config": map[string]any{"mode": "fallback", "models": []any{map[string]any{"model": "m1", "weight": 0.5}}},
		"retry_config":  map[string]any{"count": 2, "on_codes": []any{429, 503}},
		"created_at":    "2020-01-01T00:00:00Z",
		"updated_at":    "2020-01-02T00:00:00Z",
		"created_by_id": "u",
		"updated_by_id": "u",
	}
}

func newPolicyServer(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := New(Config{URL: srv.URL, Token: sentinelToken})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestPolicies_CreateRoundTrip(t *testing.T) {
	var gotBody map[string]any
	var gotPath, gotMethod string
	c := newPolicyServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(policyJSON("proj_7"))
	})

	pid := "proj_7"
	p, err := c.Policies().Create(context.Background(), PolicyCreateInput{
		DisplayName:  "prod policy",
		ProjectID:    &pid,
		Evaluators:   []EvaluatorRef{{ID: "ev_1", ExecuteOn: "input", Options: map[string]any{"k": "v"}}},
		Limits:       json.RawMessage(`{"requests":{"amount":10,"period":"day"}}`),
		ModelsConfig: json.RawMessage(`{"mode":"fallback","models":[{"model":"m1"}]}`),
		RetryConfig:  json.RawMessage(`{"count":2,"on_codes":[429,503]}`),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v2/policies" {
		t.Errorf("wrong request: %s %s", gotMethod, gotPath)
	}
	if gotBody["project_id"] != "proj_7" {
		t.Errorf("project_id not sent: %v", gotBody["project_id"])
	}
	if p.ID != "pol_1" || p.ProjectID != "proj_7" || p.Slug != "prod-policy" || p.Timeout != 300000 {
		t.Errorf("read-back wrong: %+v", p)
	}
	if len(p.Evaluators) != 1 || p.Evaluators[0].ID != "ev_1" || p.Evaluators[0].Options["k"] != "v" {
		t.Errorf("evaluators read-back wrong: %+v", p.Evaluators)
	}
	if len(p.Limits) == 0 || len(p.ModelsConfig) == 0 || len(p.RetryConfig) == 0 {
		t.Errorf("json blobs not read back: limits=%s models=%s retry=%s", p.Limits, p.ModelsConfig, p.RetryConfig)
	}
}

// TestPolicies_UpdateCarriesProjectID is the mutability proof: unlike routing /
// guardrail rules, the policy update body DOES carry project_id (mutable scope).
func TestPolicies_UpdateCarriesProjectID(t *testing.T) {
	var gotBody map[string]any
	var gotMethod string
	c := newPolicyServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(policyJSON("proj_new"))
	})
	name := "renamed"
	pid := "proj_new"
	if _, err := c.Policies().Update(context.Background(), PolicyUpdateInput{ID: "pol_1", DisplayName: &name, ProjectID: &pid}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("expected PATCH, got %s", gotMethod)
	}
	if gotBody["project_id"] != "proj_new" {
		t.Errorf("update body must carry the mutable project_id: %v", gotBody["project_id"])
	}
}

func TestPolicies_NotFoundNormalized(t *testing.T) {
	c := newPolicyServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"gone"}`))
	})
	_, err := c.Policies().Get(context.Background(), "missing")
	if err == nil || CodeOf(err) != CodeNotFound {
		t.Fatalf("expected not_found, got %v", err)
	}
	if strings.Contains(err.Error(), "/v2/policies") {
		t.Errorf("error leaks REST route: %q", err.Error())
	}
}
