package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

// --- access-vs-mode validation (api_key + management_key share this) --------

func TestValidateAccessForMode(t *testing.T) {
	accessMap := stringMapValue(map[string]string{"project": client.AccessLevelWrite})
	cases := []struct {
		name      string
		mode      types.String
		access    types.Map
		wantError bool
	}{
		{"restricted with access ok", types.StringValue(client.ManagementPermissionModeRestricted), accessMap, false},
		{"restricted without access rejected", types.StringValue(client.ManagementPermissionModeRestricted), types.MapNull(types.StringType), true},
		{"all with access rejected", types.StringValue(client.ManagementPermissionModeAll), accessMap, true},
		{"all without access ok", types.StringValue(client.ManagementPermissionModeAll), types.MapNull(types.StringType), false},
		{"unknown mode with access rejected", types.StringUnknown(), accessMap, true},
		{"null mode without access ok", types.StringNull(), types.MapNull(types.StringType), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diags := validateAccessForMode(tc.mode, tc.access, client.ManagementPermissionModeRestricted)
			if diags.HasError() != tc.wantError {
				t.Errorf("HasError = %v, want %v (%v)", diags.HasError(), tc.wantError, diags)
			}
		})
	}
}

// --- api_key one-time token stored + preserved on read ----------------------

// TestAPIKeyApplyPreservesToken proves apply() (used by Read/Update) never
// clobbers the one-time secret already in state, while Create sets it.
func TestAPIKeyApplyPreservesToken(t *testing.T) {
	r := &apiKeyResource{}
	m := &apiKeyResourceModel{Token: types.StringValue("sk-orq-ak_1-secret")}
	r.apply(&client.APIKey{ID: "ak_1", Name: "svc", AllProjects: true, PermissionMode: client.PermissionModeAll, Status: "API_KEY_STATUS_ACTIVE", TokenPrefix: "sk-orq-ak_1"}, m)
	if m.Token.ValueString() != "sk-orq-ak_1-secret" {
		t.Errorf("apply() must preserve the one-time token, got %q", m.Token.ValueString())
	}
	// All-projects scope reads back as a null project_id.
	if !m.ProjectID.IsNull() {
		t.Errorf("all-projects key must have null project_id, got %q", m.ProjectID.ValueString())
	}
	if m.TokenPrefix.ValueString() != "sk-orq-ak_1" {
		t.Errorf("token_prefix not applied: %q", m.TokenPrefix.ValueString())
	}
}

func TestManagementKeyApplyPreservesToken(t *testing.T) {
	r := &managementKeyResource{}
	m := &managementKeyResourceModel{Token: types.StringValue("sk-orq-mk_1-secret")}
	r.apply(&client.ManagementKey{
		ID:             "mk_1",
		Name:           "ci",
		PermissionMode: client.ManagementPermissionModeRestricted,
		Access:         map[string]string{"project": client.AccessLevelWrite},
		Status:         "MANAGEMENT_KEY_STATUS_ACTIVE",
		TokenPrefix:    "sk-orq-mk_1",
	}, m)
	if m.Token.ValueString() != "sk-orq-mk_1-secret" {
		t.Errorf("apply() must preserve the one-time token, got %q", m.Token.ValueString())
	}
	if m.Access.IsNull() || len(m.Access.Elements()) != 1 {
		t.Errorf("access not applied: %+v", m.Access)
	}
}

// --- routing_rule expression + models_config round-trip ---------------------

func TestRoutingRuleApplyExpressionAndModels(t *testing.T) {
	r := &routingRuleResource{}
	var m routingRuleResourceModel
	r.apply(&client.RoutingRule{
		ID:            "rrl_1",
		DisplayName:   "route",
		Priority:      3,
		ExpressionCEL: `model == "gpt-4"`,
		ModelsConfig:  []byte(`{"mode":"fallback"}`),
	}, &m)
	if m.Expression == nil || m.Expression.Cel.ValueString() != `model == "gpt-4"` {
		t.Errorf("expression not applied: %+v", m.Expression)
	}
	if m.ModelsConfig.IsNull() {
		t.Errorf("models_config not applied")
	}
	// No expression => nil nested block.
	var m2 routingRuleResourceModel
	r.apply(&client.RoutingRule{ID: "rrl_2"}, &m2)
	if m2.Expression != nil {
		t.Errorf("empty expression must be nil, got %+v", m2.Expression)
	}
}

// --- policy evaluators round-trip through the model -------------------------

func TestPolicyEvaluatorsModelRoundTrip(t *testing.T) {
	models := []policyEvaluatorModel{{
		ID:        types.StringValue("ev_1"),
		ExecuteOn: types.StringValue("input"),
		Options:   jsontypes.NewNormalizedValue(`{"language":"en"}`),
	}}
	refs, diags := policyEvaluatorsFromModel(models)
	if diags.HasError() {
		t.Fatalf("policyEvaluatorsFromModel: %v", diags)
	}
	if len(refs) != 1 || refs[0].Options == nil || refs[0].Options["language"] != "en" {
		t.Fatalf("options not decoded: %+v", refs)
	}

	var back policyResourceModel
	res := &policyResource{}
	res.apply(&client.Policy{ID: "pol_1", Evaluators: refs}, &back)
	if len(back.Evaluators) != 1 || back.Evaluators[0].Options.IsNull() {
		t.Fatalf("evaluators dropped on read-back: %+v", back.Evaluators)
	}
	eq, _ := models[0].Options.StringSemanticEquals(context.Background(), back.Evaluators[0].Options)
	if !eq {
		t.Errorf("options did not round-trip: in=%s out=%s",
			models[0].Options.ValueString(), back.Evaluators[0].Options.ValueString())
	}
}
