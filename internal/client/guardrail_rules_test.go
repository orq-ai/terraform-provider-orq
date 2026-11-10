package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	connect "connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
)

type fakeGuardrailRulesHandler struct {
	platformv1connect.UnimplementedGuardrailRulesServiceHandler
	lastCreate *platformv1.CreateGuardrailRuleRequest
	lastUpdate *platformv1.UpdateGuardrailRuleRequest
	rule       *platformv1.GuardrailRule
}

func (f *fakeGuardrailRulesHandler) CreateGuardrailRule(_ context.Context, req *platformv1.CreateGuardrailRuleRequest) (*platformv1.CreateGuardrailRuleResponse, error) {
	f.lastCreate = req
	return &platformv1.CreateGuardrailRuleResponse{GuardrailRule: f.rule}, nil
}

func (f *fakeGuardrailRulesHandler) UpdateGuardrailRule(_ context.Context, req *platformv1.UpdateGuardrailRuleRequest) (*platformv1.UpdateGuardrailRuleResponse, error) {
	f.lastUpdate = req
	return &platformv1.UpdateGuardrailRuleResponse{GuardrailRule: f.rule}, nil
}

func (f *fakeGuardrailRulesHandler) GetGuardrailRule(_ context.Context, _ *platformv1.GetGuardrailRuleRequest) (*platformv1.GetGuardrailRuleResponse, error) {
	if f.rule == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("gone"))
	}
	return &platformv1.GetGuardrailRuleResponse{GuardrailRule: f.rule}, nil
}

func newGuardrailRulesClient(t *testing.T, h *fakeGuardrailRulesHandler) *Client {
	t.Helper()
	path, handler := platformv1connect.NewGuardrailRulesServiceHandler(h)
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

func guardrailRuleProto(t *testing.T, projectID string, options map[string]any) *platformv1.GuardrailRule {
	t.Helper()
	sampleRate, isGuardrail := 0.5, true
	ref := &platformv1.GuardrailRuleGuardrail{Id: "guard_1", ExecuteOn: "input", SampleRate: &sampleRate, IsGuardrail: &isGuardrail}
	if options != nil {
		encoded, err := structpb.NewStruct(options)
		if err != nil {
			t.Fatalf("encoding options: %v", err)
		}
		ref.Options = encoded
	}
	return &platformv1.GuardrailRule{
		Id:          "gr_1",
		DisplayName: "pii",
		Description: "block pii",
		Enabled:     true,
		ProjectId:   projectID,
		Guardrails:  []*platformv1.GuardrailRuleGuardrail{ref},
	}
}

func TestGuardrailRules_CreateRoundTrip(t *testing.T) {
	h := &fakeGuardrailRulesHandler{rule: guardrailRuleProto(t, "proj_7", nil)}
	c := newGuardrailRulesClient(t, h)

	pid := "proj_7"
	rule, err := c.GuardrailRules().Create(context.Background(), GuardrailRuleCreateInput{
		DisplayName: "pii",
		ProjectID:   &pid,
		Guardrails:  []GuardrailRef{{ID: "guard_1", ExecuteOn: "input"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if h.lastCreate.GetProjectId() != "proj_7" {
		t.Errorf("project_id not sent: %v", h.lastCreate.GetProjectId())
	}
	if rule.ID != "gr_1" || rule.ProjectID != "proj_7" || !rule.Enabled {
		t.Errorf("read-back wrong: %+v", rule)
	}
	if len(rule.Guardrails) != 1 || rule.Guardrails[0].ID != "guard_1" || rule.Guardrails[0].SampleRate == nil {
		t.Errorf("guardrails read-back wrong: %+v", rule.Guardrails)
	}
	if rule.Guardrails[0].Options != nil {
		t.Errorf("absent options must read back as nil, got %+v", rule.Guardrails[0].Options)
	}
}

// TestGuardrailRules_OptionsRoundTrip proves per-guardrail options are sent on
// a write and read back, so an update never silently drops them.
func TestGuardrailRules_OptionsRoundTrip(t *testing.T) {
	opts := map[string]any{"language": "en", "threshold": 0.8, "entities": []any{"EMAIL", "PHONE"}}
	h := &fakeGuardrailRulesHandler{rule: guardrailRuleProto(t, "proj_7", opts)}
	c := newGuardrailRulesClient(t, h)

	created, err := c.GuardrailRules().Create(context.Background(), GuardrailRuleCreateInput{
		DisplayName: "pii",
		Guardrails:  []GuardrailRef{{ID: "guard_1", ExecuteOn: "input", Options: opts}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	sent := h.lastCreate.GetGuardrails()
	if len(sent) != 1 || sent[0].GetOptions().AsMap()["language"] != "en" {
		t.Fatalf("options not sent on create: %v", sent)
	}
	if len(created.Guardrails) != 1 || created.Guardrails[0].Options == nil {
		t.Fatalf("options dropped on read-back: %+v", created.Guardrails)
	}
	if created.Guardrails[0].Options["language"] != "en" || created.Guardrails[0].Options["threshold"] != 0.8 {
		t.Errorf("options not read back: %+v", created.Guardrails[0].Options)
	}
}

// TestGuardrailRules_UpdateCarriesOptions proves an update re-sends options so
// they are not wiped from the stored guardrail ref.
func TestGuardrailRules_UpdateCarriesOptions(t *testing.T) {
	opts := map[string]any{"language": "fr"}
	h := &fakeGuardrailRulesHandler{rule: guardrailRuleProto(t, "proj_7", opts)}
	c := newGuardrailRulesClient(t, h)

	name := "pii"
	updated, err := c.GuardrailRules().Update(context.Background(), GuardrailRuleUpdateInput{
		ID:          "gr_1",
		DisplayName: &name,
		Guardrails:  []GuardrailRef{{ID: "guard_1", ExecuteOn: "input", Options: opts}},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	sent := h.lastUpdate.GetGuardrails()
	if len(sent) != 1 || sent[0].GetOptions().AsMap()["language"] != "fr" {
		t.Errorf("update did not carry options: %v", sent)
	}
	if !h.lastUpdate.GetReplaceGuardrails() {
		t.Errorf("update must replace the guardrail list")
	}
	if updated.Guardrails[0].Options["language"] != "fr" {
		t.Errorf("options not read back on update: %+v", updated.Guardrails[0].Options)
	}
}

func TestGuardrailRules_NotFoundNormalized(t *testing.T) {
	c := newGuardrailRulesClient(t, &fakeGuardrailRulesHandler{})
	_, err := c.GuardrailRules().Get(context.Background(), "missing")
	if err == nil || CodeOf(err) != CodeNotFound {
		t.Fatalf("expected not_found, got %v", err)
	}
}
