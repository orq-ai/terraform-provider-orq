package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	connect "connectrpc.com/connect"

	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
)

type fakeRoutingRulesHandler struct {
	platformv1connect.UnimplementedRoutingRulesServiceHandler
	lastCreate *platformv1.CreateRoutingRuleRequest
	lastUpdate *platformv1.UpdateRoutingRuleRequest
	rule       *platformv1.RoutingRule
}

func (f *fakeRoutingRulesHandler) CreateRoutingRule(_ context.Context, req *platformv1.CreateRoutingRuleRequest) (*platformv1.CreateRoutingRuleResponse, error) {
	f.lastCreate = req
	return &platformv1.CreateRoutingRuleResponse{RoutingRule: f.rule}, nil
}

func (f *fakeRoutingRulesHandler) UpdateRoutingRule(_ context.Context, req *platformv1.UpdateRoutingRuleRequest) (*platformv1.UpdateRoutingRuleResponse, error) {
	f.lastUpdate = req
	return &platformv1.UpdateRoutingRuleResponse{RoutingRule: f.rule}, nil
}

func (f *fakeRoutingRulesHandler) GetRoutingRule(_ context.Context, _ *platformv1.GetRoutingRuleRequest) (*platformv1.GetRoutingRuleResponse, error) {
	if f.rule == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("gone"))
	}
	return &platformv1.GetRoutingRuleResponse{RoutingRule: f.rule}, nil
}

func newRoutingRulesClient(t *testing.T, h *fakeRoutingRulesHandler) *Client {
	t.Helper()
	path, handler := platformv1connect.NewRoutingRulesServiceHandler(h)
	mux := http.NewServeMux()
	mux.Handle("/v3/rpc/platform"+path, http.StripPrefix("/v3/rpc/platform", handler))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := New(Config{URL: srv.URL, Token: sentinelToken})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func routingRuleProto(projectID string) *platformv1.RoutingRule {
	weight := 0.5
	return &platformv1.RoutingRule{
		Id:          "rrl_1",
		DisplayName: "route",
		Description: "primary",
		Enabled:     true,
		ProjectId:   projectID,
		Priority:    5,
		Expression:  &platformv1.RoutingRuleExpression{Cel: `model == "gpt-4"`},
		ModelsConfig: &platformv1.RoutingRuleModelsConfig{
			Mode:   "fallback",
			Models: []*platformv1.RoutingRuleModel{{Model: "m1", Weight: &weight}},
		},
	}
}

func TestRoutingRules_CreateRoundTrip(t *testing.T) {
	h := &fakeRoutingRulesHandler{rule: routingRuleProto("proj_7")}
	c := newRoutingRulesClient(t, h)

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
	if h.lastCreate.GetProjectId() != "proj_7" {
		t.Errorf("project_id not sent: %v", h.lastCreate.GetProjectId())
	}
	if h.lastCreate.GetExpression().GetCel() != cel {
		t.Errorf("expression.cel not sent: %v", h.lastCreate.GetExpression())
	}
	sentModels := h.lastCreate.GetModelsConfig().GetModels()
	if len(sentModels) != 1 {
		t.Fatalf("models_config not sent: %v", h.lastCreate.GetModelsConfig())
	}
	// A model carrying only `model` must not drag a weight along: the server
	// applies its own default to an absent one.
	if sentModels[0].Weight != nil {
		t.Errorf("an unset weight must be omitted, got %v", sentModels[0].GetWeight())
	}
	if rule.ID != "rrl_1" || rule.ProjectID != "proj_7" || !rule.Enabled || rule.Priority != 5 {
		t.Errorf("read-back wrong: %+v", rule)
	}
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

func TestRoutingRules_CreateOmitsAbsentModelsConfig(t *testing.T) {
	h := &fakeRoutingRulesHandler{rule: routingRuleProto("")}
	c := newRoutingRulesClient(t, h)

	if _, err := c.RoutingRules().Create(context.Background(), RoutingRuleCreateInput{DisplayName: "route"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if h.lastCreate.GetModelsConfig() != nil {
		t.Errorf("models_config must be absent, got %v", h.lastCreate.GetModelsConfig())
	}
}

// Removing the block from config must actually clear the stored one, which only
// the clear_models_config flag can do.
func TestRoutingRules_UpdateClearsModelsConfig(t *testing.T) {
	cleared := routingRuleProto("")
	cleared.ModelsConfig = nil
	h := &fakeRoutingRulesHandler{rule: cleared}
	c := newRoutingRulesClient(t, h)

	rule, err := c.RoutingRules().Update(context.Background(), RoutingRuleUpdateInput{
		ID:                "rrl_1",
		ClearModelsConfig: true,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !h.lastUpdate.GetClearModelsConfig() {
		t.Errorf("clear_models_config not sent: %v", h.lastUpdate)
	}
	if h.lastUpdate.GetModelsConfig() != nil {
		t.Errorf("a clearing update must not also send models_config: %v", h.lastUpdate.GetModelsConfig())
	}
	if rule.ModelsConfig != nil {
		t.Errorf("an absent models_config must read back as nil, got %+v", rule.ModelsConfig)
	}

	// A write that carries a config never asks for a clear.
	if _, err := c.RoutingRules().Update(context.Background(), RoutingRuleUpdateInput{
		ID:                "rrl_1",
		ModelsConfig:      &RoutingRuleModelsConfig{Mode: "fallback", Models: []RoutingRuleModelRef{{Model: "m1"}}},
		ClearModelsConfig: true,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if h.lastUpdate.ClearModelsConfig != nil {
		t.Errorf("clear_models_config must not accompany a models_config write: %v", h.lastUpdate)
	}
}

func TestRoutingRules_NotFoundNormalized(t *testing.T) {
	c := newRoutingRulesClient(t, &fakeRoutingRulesHandler{})
	_, err := c.RoutingRules().Get(context.Background(), "missing")
	if err == nil || CodeOf(err) != CodeNotFound {
		t.Fatalf("expected not_found, got %v", err)
	}
}
