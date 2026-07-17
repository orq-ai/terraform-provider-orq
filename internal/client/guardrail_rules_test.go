package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// guardrailRuleJSON is the canned single-rule body the fake server returns.
func guardrailRuleJSON(projectID string) map[string]any {
	return map[string]any{
		"_id":           "gr_1",
		"display_name":  "pii",
		"description":   "block pii",
		"enabled":       true,
		"project_id":    projectID,
		"timeout":       1000,
		"created_at":    "2020-01-01T00:00:00Z",
		"updated_at":    "2020-01-02T00:00:00Z",
		"created_by_id": "u",
		"updated_by_id": "u",
		"guardrails": []map[string]any{
			{"id": "guard_1", "execute_on": "input", "sample_rate": 0.5, "is_guardrail": true},
		},
	}
}

func newGuardrailServer(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := New(Config{URL: srv.URL, Token: sentinelToken})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestGuardrailRules_CreateRoundTrip(t *testing.T) {
	var gotBody map[string]any
	var gotPath, gotMethod string
	c := newGuardrailServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(guardrailRuleJSON("proj_7"))
	})

	pid := "proj_7"
	rule, err := c.GuardrailRules().Create(context.Background(), GuardrailRuleCreateInput{
		DisplayName: "pii",
		ProjectID:   &pid,
		Guardrails:  []GuardrailRef{{ID: "guard_1", ExecuteOn: "input"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v2/guardrail-rules" {
		t.Errorf("wrong request: %s %s", gotMethod, gotPath)
	}
	if gotBody["project_id"] != "proj_7" {
		t.Errorf("project_id not sent: %v", gotBody["project_id"])
	}
	if rule.ID != "gr_1" || rule.ProjectID != "proj_7" || !rule.Enabled {
		t.Errorf("read-back wrong: %+v", rule)
	}
	if len(rule.Guardrails) != 1 || rule.Guardrails[0].ID != "guard_1" || rule.Guardrails[0].SampleRate == nil {
		t.Errorf("guardrails read-back wrong: %+v", rule.Guardrails)
	}
}

func TestGuardrailRules_UpdateOmitsProjectID(t *testing.T) {
	var gotBody map[string]any
	var gotMethod string
	c := newGuardrailServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(guardrailRuleJSON("proj_7"))
	})
	name := "pii2"
	if _, err := c.GuardrailRules().Update(context.Background(), GuardrailRuleUpdateInput{ID: "gr_1", DisplayName: &name}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("expected PATCH, got %s", gotMethod)
	}
	// The update wire contract has no project_id field.
	if _, ok := gotBody["project_id"]; ok {
		t.Errorf("update body must not carry project_id: %v", gotBody)
	}
}

func TestGuardrailRules_NotFoundNormalized(t *testing.T) {
	c := newGuardrailServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"gone"}`))
	})
	_, err := c.GuardrailRules().Get(context.Background(), "missing")
	if err == nil || CodeOf(err) != CodeNotFound {
		t.Fatalf("expected not_found, got %v", err)
	}
	if strings.Contains(err.Error(), "/v2/guardrail-rules") {
		t.Errorf("error leaks REST route: %q", err.Error())
	}
}
