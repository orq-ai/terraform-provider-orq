package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/defaults"
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
		{"null mode without access ok", types.StringNull(), types.MapNull(types.StringType), false},
		// Unknown involved value → defer (no diagnostics); the cross-field decision
		// can't be made on an interpolated placeholder, so it is re-enforced by the
		// Create/Update recheck once known (the framework never re-runs ValidateConfig).
		{"unknown mode with access defers", types.StringUnknown(), accessMap, false},
		{"unknown mode without access defers", types.StringUnknown(), types.MapNull(types.StringType), false},
		{"restricted with unknown access defers", types.StringValue(client.ManagementPermissionModeRestricted), types.MapUnknown(types.StringType), false},
		{"all with unknown access defers", types.StringValue(client.ManagementPermissionModeAll), types.MapUnknown(types.StringType), false},
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

// TestKeyAccessInvariantRecheckedAtCreate documents the Create/Update recheck
// (FINDING 1): the framework runs ValidateConfig ONLY at validate/plan time and
// DEFERS the access-vs-mode check when the mode is unknown (interpolated), and it
// never re-runs ValidateConfig at apply. So Create/Update call validateAccessForMode
// again with the NOW-KNOWN plan values. This drives that helper with the exact
// FINDING 1(a) scenario — an interpolation that resolved to a non-restricted mode
// (ALL) while carrying a non-empty access map — and proves it is rejected before any
// server call (which would otherwise orphan a created key and trip an inconsistent
// result). Both key resources share the helper; only the restricted constant differs.
func TestKeyAccessInvariantRecheckedAtCreate(t *testing.T) {
	accessMap := stringMapValue(map[string]string{"project": client.AccessLevelWrite})
	for _, tc := range []struct {
		name       string
		restricted string
	}{
		{"api_key", client.PermissionModeRestricted},
		{"management_key", client.ManagementPermissionModeRestricted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Interpolation resolved to ALL (a non-restricted mode) with an access map:
			// must be rejected by the Create/Update recheck.
			resolvedToAll := types.StringValue(map[string]string{
				"api_key":        client.PermissionModeAll,
				"management_key": client.ManagementPermissionModeAll,
			}[tc.name])
			if d := validateAccessForMode(resolvedToAll, accessMap, tc.restricted); !d.HasError() {
				t.Error("mode resolved to ALL with a non-empty access map must be rejected at create")
			}
			// Interpolation resolved to RESTRICTED with the access map: accepted.
			if d := validateAccessForMode(types.StringValue(tc.restricted), accessMap, tc.restricted); d.HasError() {
				t.Errorf("RESTRICTED + access must be accepted: %v", d)
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

// TestKeyPermissionModeSchemaDefaultsToAll proves both key resources default
// permission_mode to the ALL enum so an omitted value never reaches the server
// as UNSPECIFIED (which the server rejects on create).
func TestKeyPermissionModeSchemaDefaultsToAll(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		res  resource.Resource
		want string
	}{
		{"api_key", NewAPIKeyResource(), client.PermissionModeAll},
		{"management_key", NewManagementKeyResource(), client.ManagementPermissionModeAll},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sch resource.SchemaResponse
			tc.res.Schema(ctx, resource.SchemaRequest{}, &sch)
			attr, ok := sch.Schema.Attributes["permission_mode"].(schema.StringAttribute)
			if !ok {
				t.Fatalf("permission_mode is not a StringAttribute")
			}
			if attr.Default == nil {
				t.Fatal("permission_mode must declare a schema Default")
			}
			dresp := defaults.StringResponse{}
			attr.Default.DefaultString(ctx, defaults.StringRequest{}, &dresp)
			if dresp.PlanValue.ValueString() != tc.want {
				t.Errorf("default = %q, want %q", dresp.PlanValue.ValueString(), tc.want)
			}
		})
	}
}
