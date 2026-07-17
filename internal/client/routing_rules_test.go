package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func routingRuleJSON(projectID string) map[string]any {
	return map[string]any{
		"_id":           "rrl_1",
		"display_name":  "route",
		"description":   "primary",
		"enabled":       true,
		"project_id":    projectID,
		"priority":      5,
		"expression":    map[string]any{"cel": `model == "gpt-4"`, "config": map[string]any{"x": 1}},
		"models_config": map[string]any{"mode": "fallback", "models": []any{map[string]any{"id": "m1"}}},
		"created_at":    "2020-01-01T00:00:00Z",
		"updated_at":    "2020-01-02T00:00:00Z",
		"created_by_id": "u",
		"updated_by_id": "u",
	}
}

func newRoutingServer(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := New(Config{URL: srv.URL, Token: sentinelToken})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestRoutingRules_CreateRoundTrip(t *testing.T) {
	var gotBody map[string]any
	var gotPath, gotMethod string
	c := newRoutingServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(routingRuleJSON("proj_7"))
	})

	pid := "proj_7"
	cel := `model == "gpt-4"`
	mc := json.RawMessage(`{"mode":"fallback","models":[{"id":"m1"}]}`)
	rule, err := c.RoutingRules().Create(context.Background(), RoutingRuleCreateInput{
		DisplayName:   "route",
		ProjectID:     &pid,
		ExpressionCEL: &cel,
		ModelsConfig:  mc,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v2/routing-rules" {
		t.Errorf("wrong request: %s %s", gotMethod, gotPath)
	}
	if gotBody["project_id"] != "proj_7" {
		t.Errorf("project_id not sent: %v", gotBody["project_id"])
	}
	if exp, ok := gotBody["expression"].(map[string]any); !ok || exp["cel"] != cel {
		t.Errorf("expression.cel not sent: %v", gotBody["expression"])
	}
	if _, ok := gotBody["models_config"].(map[string]any); !ok {
		t.Errorf("models_config not sent: %v", gotBody["models_config"])
	}
	if rule.ID != "rrl_1" || rule.ProjectID != "proj_7" || !rule.Enabled || rule.Priority != 5 {
		t.Errorf("read-back wrong: %+v", rule)
	}
	// Only cel is surfaced from expression; config is dropped.
	if rule.ExpressionCEL != cel {
		t.Errorf("expression cel read-back wrong: %q", rule.ExpressionCEL)
	}
	if len(rule.ModelsConfig) == 0 {
		t.Errorf("models_config not read back: %s", rule.ModelsConfig)
	}
}

func TestRoutingRules_UpdateOmitsProjectID(t *testing.T) {
	var gotBody map[string]any
	var gotMethod string
	c := newRoutingServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(routingRuleJSON("proj_7"))
	})
	name := "route2"
	if _, err := c.RoutingRules().Update(context.Background(), RoutingRuleUpdateInput{ID: "rrl_1", DisplayName: &name}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("expected PATCH, got %s", gotMethod)
	}
	// The routing-rule update wire contract has no project_id field.
	if _, ok := gotBody["project_id"]; ok {
		t.Errorf("update body must not carry project_id: %v", gotBody)
	}
}

func TestRoutingRules_NotFoundNormalized(t *testing.T) {
	c := newRoutingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"gone"}`))
	})
	_, err := c.RoutingRules().Get(context.Background(), "missing")
	if err == nil || CodeOf(err) != CodeNotFound {
		t.Fatalf("expected not_found, got %v", err)
	}
	if strings.Contains(err.Error(), "/v2/routing-rules") {
		t.Errorf("error leaks REST route: %q", err.Error())
	}
}
