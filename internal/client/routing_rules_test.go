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
		"models_config": map[string]any{"mode": "fallback", "models": []any{map[string]any{"model": "m1", "weight": 0.5}}},
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
	rule, err := c.RoutingRules().Create(context.Background(), RoutingRuleCreateInput{
		DisplayName:   "route",
		ProjectID:     &pid,
		ExpressionCEL: &cel,
		ModelsConfig: &RoutingRuleModelsConfig{
			Mode:   "fallback",
			Models: []RoutingRuleModelRef{{Model: "m1"}},
		},
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
	sentConfig, ok := gotBody["models_config"].(map[string]any)
	if !ok {
		t.Fatalf("models_config not sent: %v", gotBody["models_config"])
	}
	// A model carrying only `model` must not drag empty defaults along: the
	// server would store "" / reject them rather than apply its own.
	sentModel := sentConfig["models"].([]any)[0].(map[string]any)
	for _, key := range []string{"display_name", "integration_id", "weight"} {
		if _, present := sentModel[key]; present {
			t.Errorf("an unset %q must be omitted from the payload, got %v", key, sentModel)
		}
	}
	if rule.ID != "rrl_1" || rule.ProjectID != "proj_7" || !rule.Enabled || rule.Priority != 5 {
		t.Errorf("read-back wrong: %+v", rule)
	}
	// Only cel is surfaced from expression; config is dropped.
	if rule.ExpressionCEL != cel {
		t.Errorf("expression cel read-back wrong: %q", rule.ExpressionCEL)
	}
	if rule.ModelsConfig == nil || rule.ModelsConfig.Mode != "fallback" || len(rule.ModelsConfig.Models) != 1 {
		t.Fatalf("models_config not read back: %+v", rule.ModelsConfig)
	}
	if got := rule.ModelsConfig.Models[0]; got.Model != "m1" || got.Weight == nil || *got.Weight != 0.5 {
		t.Errorf("model read-back wrong: %+v", got)
	}
}

// The create body must OMIT models_config when there is none: the server
// rejects an explicit null ("models_config must be a JSON object, got null").
func TestRoutingRules_CreateOmitsAbsentModelsConfig(t *testing.T) {
	var gotBody map[string]any
	c := newRoutingServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(routingRuleJSON(""))
	})

	if _, err := c.RoutingRules().Create(context.Background(), RoutingRuleCreateInput{DisplayName: "route"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, present := gotBody["models_config"]; present {
		t.Errorf("models_config must be absent, got %v", gotBody["models_config"])
	}
}

// Removing the block from config must actually clear the stored one, which only
// the (spec-hidden) clear_models_config flag can do.
func TestRoutingRules_UpdateClearsModelsConfig(t *testing.T) {
	var gotBody map[string]any
	c := newRoutingServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody = nil // decoding into a live map would merge the two requests
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		rule := routingRuleJSON("")
		rule["models_config"] = nil
		_ = json.NewEncoder(w).Encode(rule)
	})

	rule, err := c.RoutingRules().Update(context.Background(), RoutingRuleUpdateInput{
		ID:                "rrl_1",
		ClearModelsConfig: true,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if gotBody["clear_models_config"] != true {
		t.Errorf("clear_models_config not sent: %v", gotBody)
	}
	if _, present := gotBody["models_config"]; present {
		t.Errorf("a clearing update must not also send models_config: %v", gotBody["models_config"])
	}
	// A server-emitted `"models_config": null` must read back as absent, not as
	// an empty config the next write would echo.
	if rule.ModelsConfig != nil {
		t.Errorf("null models_config must read back as nil, got %+v", rule.ModelsConfig)
	}

	// A write that carries a config never asks for a clear.
	if _, err := c.RoutingRules().Update(context.Background(), RoutingRuleUpdateInput{
		ID:                "rrl_1",
		ModelsConfig:      &RoutingRuleModelsConfig{Mode: "fallback", Models: []RoutingRuleModelRef{{Model: "m1"}}},
		ClearModelsConfig: true,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, present := gotBody["clear_models_config"]; present {
		t.Errorf("clear_models_config must not accompany a models_config write: %v", gotBody)
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
