package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/boolvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

var (
	_ resource.Resource                   = &workspaceModelResource{}
	_ resource.ResourceWithConfigure      = &workspaceModelResource{}
	_ resource.ResourceWithImportState    = &workspaceModelResource{}
	_ resource.ResourceWithValidateConfig = &workspaceModelResource{}
)

// NewWorkspaceModelResource is the factory registered on the provider.
func NewWorkspaceModelResource() resource.Resource { return &workspaceModelResource{} }

type workspaceModelResource struct {
	models client.WorkspaceModelsAPI
}

type workspaceModelResourceModel struct {
	ID          types.String                `tfsdk:"id"`
	ModelID     types.String                `tfsdk:"model_id"`
	Enabled     types.Bool                  `tfsdk:"enabled"`
	DisplayName types.String                `tfsdk:"display_name"`
	Sharing     *workspaceModelSharingModel `tfsdk:"sharing"`
}

type workspaceModelSharingModel struct {
	AllProjects          types.Bool `tfsdk:"all_projects"`
	ProjectIDs           types.List `tfsdk:"project_ids"`
	AllowVersionPin      types.Bool `tfsdk:"allow_version_pin"`
	AllowFork            types.Bool `tfsdk:"allow_fork"`
	AutoGrantNewProjects types.Bool `tfsdk:"auto_grant_new_projects"`
}

func (r *workspaceModelResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_workspace_model"
}

func (r *workspaceModelResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Enables a model in the workspace catalog and manages its project sharing. " +
			"A bare enabled model defaults to all-projects (a fail-open hazard), so this resource always " +
			"writes an explicit `sharing` block as part of create; if the sharing write fails after the " +
			"model is enabled, the resource is tainted so the next apply re-reconciles it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Resource identifier (equals `model_id`).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"model_id": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "The model reference (the model document ID, e.g. `openai/gpt-4o` for a " +
					"system model). Changing it forces replacement.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"enabled": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether the model is enabled in the workspace catalog (always true while managed).",
			},
			"display_name": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Human-readable model name.",
			},
			"sharing": schema.SingleNestedAttribute{
				Required:            true,
				MarkdownDescription: "Project sharing config. Exactly one of `all_projects` or `project_ids` must be set.",
				Attributes: map[string]schema.Attribute{
					"all_projects": schema.BoolAttribute{
						Optional:            true,
						MarkdownDescription: "Share with every project in the workspace. Mutually exclusive with `project_ids`.",
						Validators: []validator.Bool{
							boolvalidator.ExactlyOneOf(
								path.MatchRelative().AtParent().AtName("project_ids"),
							),
						},
					},
					"project_ids": schema.ListAttribute{
						Optional:    true,
						ElementType: types.StringType,
						MarkdownDescription: "Share with exactly these projects. An empty list means shared with no " +
							"project (still workspace-visible to admins). Mutually exclusive with `all_projects`.",
					},
					"allow_version_pin": schema.BoolAttribute{
						Optional:            true,
						Computed:            true,
						MarkdownDescription: "Allow consuming projects to pin a specific version.",
					},
					"allow_fork": schema.BoolAttribute{
						Optional:            true,
						Computed:            true,
						MarkdownDescription: "Allow consuming projects to fork this model into a project-owned copy.",
					},
					"auto_grant_new_projects": schema.BoolAttribute{
						Optional: true,
						Computed: true,
						MarkdownDescription: "Automatically grant new projects access. Only valid with `all_projects` " +
							"(combining it with an explicit `project_ids` list is a perpetual-diff trap and is rejected).",
					},
				},
			},
		},
	}
}

func (r *workspaceModelResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	r.models = c.WorkspaceModels()
}

// ValidateConfig enforces the auto_grant guard: auto_grant_new_projects = true
// is only meaningful with all_projects. With an explicit project_ids list the
// stored list would be mutated on new-project creation, producing a perpetual
// diff, so it is rejected at plan time.
func (r *workspaceModelResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg workspaceModelResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(validateSharingAutoGrant(cfg.Sharing)...)
}

// validateSharingAutoGrant rejects auto_grant_new_projects = true combined with
// an explicit project_ids list (a perpetual-diff trap). It is a pure function so
// the guard is unit-testable without constructing a tfsdk.Config.
func validateSharingAutoGrant(s *workspaceModelSharingModel) diag.Diagnostics {
	var diags diag.Diagnostics
	if s == nil {
		return diags
	}
	if s.AutoGrantNewProjects.IsUnknown() || s.ProjectIDs.IsUnknown() {
		return diags
	}
	if s.AutoGrantNewProjects.ValueBool() && !s.ProjectIDs.IsNull() {
		diags.AddAttributeError(
			path.Root("sharing").AtName("auto_grant_new_projects"),
			"Invalid sharing config",
			"auto_grant_new_projects = true cannot be combined with an explicit project_ids list "+
				"(it would mutate the stored list on new-project creation, causing a perpetual diff). "+
				"Use it only with all_projects = true.",
		)
	}
	return diags
}

// sharingInput builds the write shape from the plan's sharing block.
func (s *workspaceModelSharingModel) sharingInput(ctx context.Context) (client.SharingInput, diag.Diagnostics) {
	in := client.SharingInput{
		AllowVersionPin:      s.AllowVersionPin.ValueBool(),
		AllowFork:            s.AllowFork.ValueBool(),
		AutoGrantNewProjects: s.AutoGrantNewProjects.ValueBool(),
	}
	if !s.AllProjects.IsNull() && !s.AllProjects.IsUnknown() && s.AllProjects.ValueBool() {
		in.AllProjects = true
		return in, nil
	}
	// selected mode
	ids, diags := stringSlice(ctx, s.ProjectIDs)
	if ids == nil {
		ids = []string{}
	}
	in.ProjectIDs = ids
	return in, diags
}

// applySharing writes the normalized read-back sharing config into the model,
// resolving the empty-vs-null quirk (selected + no ids => []).
func applySharing(cfg *client.SharingConfig, m *workspaceModelResourceModel) {
	if cfg == nil {
		m.Sharing = nil
		return
	}
	out := &workspaceModelSharingModel{
		AllowVersionPin:      types.BoolValue(cfg.AllowVersionPin),
		AllowFork:            types.BoolValue(cfg.AllowFork),
		AutoGrantNewProjects: types.BoolValue(cfg.AutoGrantNewProjects),
	}
	switch cfg.Mode {
	case client.SharingModeAllProjects:
		out.AllProjects = types.BoolValue(true)
		out.ProjectIDs = types.ListNull(types.StringType)
	default: // selected
		out.AllProjects = types.BoolNull()
		out.ProjectIDs = stringListValue(cfg.ProjectIDs) // non-nil => [] when empty
	}
	m.Sharing = out
}

func (r *workspaceModelResource) applyModel(wm *client.WorkspaceModel, m *workspaceModelResourceModel) {
	m.ID = types.StringValue(wm.ModelID)
	m.ModelID = types.StringValue(wm.ModelID)
	m.Enabled = types.BoolValue(wm.Enabled)
	m.DisplayName = optString(wm.DisplayName)
	applySharing(wm.Sharing, m)
}

func (r *workspaceModelResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan workspaceModelResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	modelID := plan.ModelID.ValueString()

	// Step 1: enable. On failure nothing was created; state is left null.
	if err := r.models.Enable(ctx, modelID); err != nil {
		resp.Diagnostics.AddError("Unable to enable model", errDetail(err))
		return
	}

	// Step 2: write sharing. The model is now enabled — a failure here leaves a
	// bare all-projects model, a fail-open hazard, so we persist a partial state
	// AND return an error to taint the resource for the next apply.
	in, sdiags := plan.Sharing.sharingInput(ctx)
	resp.Diagnostics.Append(sdiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.models.SetSharing(ctx, modelID, in); err != nil {
		// Persist what we know: the model is enabled. Keep the requested sharing
		// block so the operator sees the intended config.
		plan.ID = types.StringValue(modelID)
		plan.Enabled = types.BoolValue(true)
		plan.DisplayName = types.StringNull()
		// The three sharing booleans are Optional+Computed and may still be
		// UNKNOWN on Create when unset in config. A post-apply state carrying
		// unknown values is rejected by the framework, which would abort this
		// State.Set and defeat the taint we are trying to persist — so pin them
		// to concrete values first.
		if plan.Sharing != nil {
			plan.Sharing.AllowVersionPin = concreteBool(plan.Sharing.AllowVersionPin)
			plan.Sharing.AllowFork = concreteBool(plan.Sharing.AllowFork)
			plan.Sharing.AutoGrantNewProjects = concreteBool(plan.Sharing.AutoGrantNewProjects)
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		resp.Diagnostics.AddError("Model enabled but sharing write failed",
			"The model was enabled but its sharing config could not be written, so it currently "+
				"defaults to all-projects (fail-open). The resource has been marked tainted; re-apply "+
				"to reconcile the sharing config.\n\n"+errDetail(err))
		return
	}

	// Step 3: read back for computed fields + normalized sharing.
	wm, err := r.models.Get(ctx, modelID)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read model after enable", errDetail(err))
		return
	}
	if wm == nil {
		resp.Diagnostics.AddError("Model missing after enable",
			"The model was enabled and shared but is not present in the catalog read-back. This may indicate a backend issue.")
		return
	}
	r.applyModel(wm, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *workspaceModelResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state workspaceModelResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	wm, err := r.models.Get(ctx, state.ModelID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Unable to read model", errDetail(err))
		return
	}
	if wm == nil || !wm.Enabled {
		// Not enabled in the workspace catalog anymore — drop from state.
		resp.State.RemoveResource(ctx)
		return
	}

	r.applyModel(wm, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *workspaceModelResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan workspaceModelResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	modelID := plan.ModelID.ValueString()

	in, sdiags := plan.Sharing.sharingInput(ctx)
	resp.Diagnostics.Append(sdiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.models.SetSharing(ctx, modelID, in); err != nil {
		resp.Diagnostics.AddError("Unable to update model sharing", errDetail(err))
		return
	}

	wm, err := r.models.Get(ctx, modelID)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read model after sharing update", errDetail(err))
		return
	}
	if wm == nil {
		resp.Diagnostics.AddError("Model missing after sharing update",
			"The model is not present in the catalog read-back after a sharing update.")
		return
	}
	r.applyModel(wm, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *workspaceModelResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state workspaceModelResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	modelID := state.ModelID.ValueString()

	// A system model id contains a slash, so the disable call can silently
	// no-op server-side. Do NOT trust the disable status (nor treat not_found
	// as done) — a hard error still surfaces, but success/not_found only lets
	// us proceed to the confirming read below.
	if err := r.models.Disable(ctx, modelID); err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Unable to disable model", errDetail(err))
		return
	}

	// Confirm the model is actually gone from the catalog. If it is still
	// enabled, the disable did not take effect; surface that instead of
	// dropping the resource from state (which would strand an enabled model).
	wm, err := r.models.Get(ctx, modelID)
	if err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to confirm model was disabled", errDetail(err))
		return
	}
	if wm != nil && wm.Enabled {
		resp.Diagnostics.AddError("Model still enabled after disable",
			"The disable request reported success but the model is still enabled in the workspace catalog. "+
				"System model ids contain a slash, which can cause the disable to no-op server-side. The "+
				"resource was left in state; retry the destroy.")
	}
}

func (r *workspaceModelResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// The import ID is the model reference; it seeds both id and model_id.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("model_id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
