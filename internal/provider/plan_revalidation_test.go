package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

// planFor builds a plan for a resource from a partially filled model. Every
// field left at its zero value is null, which is what an omitted attribute is.
func planFor(t *testing.T, r resource.Resource, model any) tfsdk.Plan {
	t.Helper()
	ctx := context.Background()
	var sch resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sch)
	s := sch.Schema
	state := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	if diags := state.Set(ctx, model); diags.HasError() {
		t.Fatalf("building plan: %v", diags)
	}
	return tfsdk.Plan{Schema: s, Raw: state.Raw}
}

// TestRevalidatePlanWalksEverySchemaKind fails the build when a resource grows
// an attribute or block kind the walker cannot descend into — silently losing
// apply-time coverage for everything nested under it.
func TestRevalidatePlanWalksEverySchemaKind(t *testing.T) {
	ctx := context.Background()
	for _, f := range New("test")().Resources(ctx) {
		r := f()
		var meta resource.MetadataResponse
		r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "orq"}, &meta)
		var sch resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &sch)
		s := sch.Schema

		w := &planWalker{cfg: tfsdk.Config{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}}
		w.attributes(ctx, path.Empty(), s.Attributes)
		w.blocks(ctx, path.Empty(), s.Blocks)
		if len(w.unhandled) > 0 {
			t.Errorf("%s: revalidatePlan cannot walk %v", meta.TypeName, w.unhandled)
		}
	}
}

// TestRevalidatePlanRejectsResolvedValues drives the values a schema validator
// SKIPS at plan time because they were unknown then. The framework never re-runs
// those validators at apply, so each of these used to reach the server, come
// back normalized (uppercased enum, "" stored as absence) and fail the apply
// with an inconsistent result on an already-created resource.
func TestRevalidatePlanRejectsResolvedValues(t *testing.T) {
	ctx := context.Background()
	limits := &budgetLimitsModel{Period: types.StringValue("MONTHLY"), Amount: types.Float64Value(10)}

	cases := []struct {
		name     string
		resource resource.Resource
		model    any
		wantPath string
	}{
		{
			name: "budget scope kind resolved lowercase", resource: NewBudgetResource(),
			model:    &budgetResourceModel{Scope: &budgetScopeModel{Kind: types.StringValue("workspace")}, Limits: limits},
			wantPath: "scope.kind",
		},
		{
			name: "budget period resolved lowercase", resource: NewBudgetResource(),
			model: &budgetResourceModel{
				Scope:  &budgetScopeModel{Kind: types.StringValue(client.BudgetScopeWorkspace)},
				Limits: &budgetLimitsModel{Period: types.StringValue("monthly"), Amount: types.Float64Value(10)},
			},
			wantPath: "limits.period",
		},
		{
			name: "budget alert dimension resolved lowercase", resource: NewBudgetResource(),
			model: &budgetResourceModel{
				Scope:  &budgetScopeModel{Kind: types.StringValue(client.BudgetScopeWorkspace)},
				Limits: limits,
				Alerts: []budgetAlertModel{{
					ThresholdPercent: types.Int64Value(80),
					NotifierIDs:      stringList("nf_1"),
					Dimension:        types.StringValue("cost"),
				}},
			},
			wantPath: "alerts[0].dimension",
		},
		{
			name: "notifier type resolved lowercase", resource: NewNotifierResource(),
			model: &notifierResourceModel{
				DisplayName: types.StringValue("alerts"),
				Type:        types.StringValue("email"),
				Emails:      types.ListNull(types.StringType),
			},
			wantPath: "type",
		},
		{
			name: "workspace_model all_projects resolved false", resource: NewWorkspaceModelResource(),
			model: &workspaceModelResourceModel{
				ModelID: types.StringValue("openai/gpt-4o"),
				Sharing: &workspaceModelSharingModel{AllProjects: types.BoolValue(false), ProjectIDs: types.ListNull(types.StringType)},
			},
			wantPath: "sharing.all_projects",
		},
		{
			name: "workspace_model duplicate project_ids", resource: NewWorkspaceModelResource(),
			model: &workspaceModelResourceModel{
				ModelID: types.StringValue("openai/gpt-4o"),
				Sharing: &workspaceModelSharingModel{AllProjects: types.BoolNull(), ProjectIDs: stringList("p1", "p1")},
			},
			wantPath: "sharing.project_ids[1]",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diags := revalidatePlan(ctx, planFor(t, tc.resource, tc.model))
			if !diags.HasError() {
				t.Fatal("the resolved value must be rejected before the write")
			}
			var paths []string
			for _, e := range diags.Errors() {
				if a, ok := e.(interface{ Path() path.Path }); ok {
					paths = append(paths, a.Path().String())
				}
			}
			if !slicesContains(paths, tc.wantPath) {
				t.Errorf("expected an error at %q, got %v (%v)", tc.wantPath, paths, diags)
			}
		})
	}
}

func slicesContains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// A valid plan must stay valid: the pass re-runs the same validators the plan
// phase already ran, so it can only reject what was unknown back then.
func TestRevalidatePlanAcceptsValidPlans(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		resource resource.Resource
		model    any
	}{
		{"budget", NewBudgetResource(), &budgetResourceModel{
			Scope:  &budgetScopeModel{Kind: types.StringValue(client.BudgetScopeWorkspace)},
			Limits: &budgetLimitsModel{Period: types.StringValue("MONTHLY"), Amount: types.Float64Value(10)},
		}},
		{"notifier", NewNotifierResource(), &notifierResourceModel{
			DisplayName: types.StringValue("alerts"),
			Type:        types.StringValue(client.NotifierTypeEmail),
			Emails:      stringList("ops@x.io"),
		}},
		{"workspace_model", NewWorkspaceModelResource(), &workspaceModelResourceModel{
			ModelID: types.StringValue("openai/gpt-4o"),
			Sharing: &workspaceModelSharingModel{AllProjects: types.BoolValue(true), ProjectIDs: types.ListNull(types.StringType)},
		}},
		{"api_key", NewAPIKeyResource(), &apiKeyResourceModel{
			Name:           types.StringValue("ci"),
			PermissionMode: types.StringValue(client.PermissionModeAll),
			Access:         types.MapNull(types.StringType),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diags := revalidatePlan(ctx, planFor(t, tc.resource, tc.model)); diags.HasError() {
				t.Errorf("a valid plan must pass: %v", diags)
			}
		})
	}
}

// The pass is wired ahead of every write, not merely available.
func TestRevalidatePlanRunsBeforeTheWrite(t *testing.T) {
	ctx := context.Background()
	s := notifierSchema(t)
	plan := planFor(t, NewNotifierResource(), &notifierResourceModel{
		DisplayName: types.StringValue("alerts"),
		Type:        types.StringValue("email"), // an interpolation that resolved lowercase
		Emails:      stringList("ops@x.io"),
	})
	emptyState := func() tfsdk.State {
		return tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	}

	api := &fakeNotifiers{}
	createResp := resource.CreateResponse{State: emptyState()}
	(&notifierResource{notifiers: api}).Create(ctx, resource.CreateRequest{Plan: plan}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Fatal("a lowercase type must fail the create")
	}
	updateResp := resource.UpdateResponse{State: emptyState()}
	(&notifierResource{notifiers: api}).Update(ctx, resource.UpdateRequest{Plan: plan, State: emptyState()}, &updateResp)
	if !updateResp.Diagnostics.HasError() {
		t.Fatal("a lowercase type must fail the update")
	}
	if api.creates != 0 || api.updates != 0 {
		t.Errorf("nothing must be written: %d creates, %d updates", api.creates, api.updates)
	}
}
