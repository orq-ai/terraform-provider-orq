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

// modelResolver is the narrow seam the resource uses to turn a user-supplied
// model_id (a human-readable ref, or a document id) into the resolved catalog
// document. It is satisfied by client.ModelsAPI (c.Models()).
type modelResolver interface {
	Resolve(ctx context.Context, ref string) (*client.Model, error)
}

type workspaceModelResource struct {
	models   client.WorkspaceModelsAPI
	resolver modelResolver
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
				Computed: true,
				MarkdownDescription: "Resource identifier: the resolved model DOCUMENT id (a UUID on this " +
					"backend — the `id` field of the matching entry in `GET /v2/models`). It may differ from " +
					"`model_id` when that is supplied as a human-readable ref.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"model_id": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "The model to enable. The recommended form is the human-readable reference " +
					"`provider/model_id` (e.g. `openai/gpt-4o`); a workspace-custom model uses " +
					"`workspaceKey@provider/model_id` (e.g. `acme@openailike/my-model`). This is the `ref_id` " +
					"field of an entry in `GET /v2/models`. A model DOCUMENT id (the `id` field, a UUID) is also " +
					"accepted for compatibility and takes precedence when it exactly matches a document. If a ref " +
					"matches more than one document (the same model_id under multiple providers), resolution fails " +
					"as ambiguous — use the document id to select exactly one. Changing this forces replacement, so " +
					"an import by document id stores the canonical ref here instead (unless that ref is ambiguous), " +
					"keeping a ref-based config from planning a destroy/recreate.",
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
						Optional: true,
						MarkdownDescription: "Share with every project in the workspace. Only accepts `true` — to share with " +
							"no project set `project_ids = []` instead. Mutually exclusive with `project_ids`.",
						Validators: []validator.Bool{
							boolvalidator.ExactlyOneOf(
								path.MatchRelative().AtParent().AtName("project_ids"),
							),
							allProjectsTrueValidator{},
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
						MarkdownDescription: "Allow consuming projects to pin a specific version. Omitted stores `false`.",
					},
					"allow_fork": schema.BoolAttribute{
						Optional:            true,
						Computed:            true,
						MarkdownDescription: "Allow consuming projects to fork this model into a project-owned copy. Omitted stores `false`.",
					},
					"auto_grant_new_projects": schema.BoolAttribute{
						Optional: true,
						Computed: true,
						MarkdownDescription: "Automatically grant new projects access. Omitted stores `false`. Only valid with `all_projects` " +
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
	r.resolver = c.Models()
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

// all_projects has no false form: sharing with nobody is project_ids = []. A
// false slipped past ExactlyOneOf (which counts a present-but-false bool as set)
// and was written as selected-with-no-projects, whose read-back normalizes to
// all_projects = null — an inconsistent result that taints the resource.
const (
	invalidAllProjectsSummary = "Invalid sharing config"
	invalidAllProjectsDetail  = "all_projects only accepts true. To share with no project set project_ids = []; " +
		"to share with specific projects list them in project_ids."
)

type allProjectsTrueValidator struct{}

func (allProjectsTrueValidator) Description(context.Context) string {
	return "must be true when set (use project_ids = [] to share with no project)"
}

func (v allProjectsTrueValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (allProjectsTrueValidator) ValidateBool(_ context.Context, req validator.BoolRequest, resp *validator.BoolResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() || req.ConfigValue.ValueBool() {
		return
	}
	resp.Diagnostics.AddAttributeError(req.Path, invalidAllProjectsSummary, invalidAllProjectsDetail)
}

// validateAllProjects re-checks all_projects before the write. The schema
// validator only sees values known at plan time, so an interpolated false
// reaches apply unchecked. Called before any API call, so nothing is created.
func (s *workspaceModelSharingModel) validateAllProjects() diag.Diagnostics {
	var diags diag.Diagnostics
	if s == nil || s.AllProjects.IsNull() || s.AllProjects.IsUnknown() || s.AllProjects.ValueBool() {
		return diags
	}
	diags.AddAttributeError(
		path.Root("sharing").AtName("all_projects"),
		invalidAllProjectsSummary,
		invalidAllProjectsDetail,
	)
	return diags
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

// canonicalModelID picks the model_id to store for a freshly imported resource.
// A document id imports cleanly but plans as a replacement against the ref a
// config normally carries — and model_id is RequiresReplace, so the next apply
// would silently destroy and recreate the model. Storing the ref instead makes
// such a config plan clean.
//
// The ref is only adopted when it resolves back to the SAME document: a ref
// shared by several documents is ambiguous, and only the document id can name
// this one. This runs on the import read alone; an ordinary read keeps whichever
// form the operator wrote.
func (r *workspaceModelResource) canonicalModelID(ctx context.Context, doc *client.Model, given string) types.String {
	if doc.RefID == "" || doc.RefID == given {
		return types.StringValue(given)
	}
	back, err := r.resolver.Resolve(ctx, doc.RefID)
	if err != nil || back.ID != doc.ID {
		return types.StringValue(given)
	}
	return types.StringValue(doc.RefID)
}

func (r *workspaceModelResource) applyModel(wm *client.WorkspaceModel, m *workspaceModelResourceModel) {
	// id is the resolved DOCUMENT id (the catalog `id` we read by). model_id is the
	// caller's identity value (a ref or a document id) and is NEVER rewritten here —
	// overwriting it with the resolved UUID would produce a perpetual diff against
	// a config that used a human-readable ref.
	m.ID = types.StringValue(wm.ModelID)
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

	resp.Diagnostics.Append(plan.Sharing.validateAllProjects()...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Step 0: resolve model_id (a human-readable ref or a document id) to the
	// document UUID all subsequent API calls use. On failure nothing was created.
	doc, err := r.resolver.Resolve(ctx, plan.ModelID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Unable to resolve model reference", errDetail(err))
		return
	}
	modelID := doc.ID

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

	// Prefer the stored resolved document id: reading by it is stable even when the
	// human-readable ref later becomes ambiguous (a second provider's document
	// acquires the same ref_id, which would make a re-resolve fail). Only when id is
	// absent — right after `terraform import`, which seeds model_id alone — do we
	// resolve the ref to a document id.
	docID := state.ID.ValueString()
	if docID == "" {
		doc, err := r.resolver.Resolve(ctx, state.ModelID.ValueString())
		if err != nil {
			if isNotFound(err) {
				resp.State.RemoveResource(ctx)
				return
			}
			resp.Diagnostics.AddError("Unable to resolve model reference", errDetail(err))
			return
		}
		docID = doc.ID
		state.ModelID = r.canonicalModelID(ctx, doc, state.ModelID.ValueString())
	}

	wm, err := r.models.Get(ctx, docID)
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
	resp.Diagnostics.Append(plan.Sharing.validateAllProjects()...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Sharing-only update: model_id is RequiresReplace, so it is unchanged here and
	// the resolved document id carries over from state (via UseStateForUnknown). Use
	// it directly rather than re-resolving the ref, which could now be ambiguous.
	modelID := plan.ID.ValueString()

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
	// Disable / confirm by the resolved document id stored in state.
	modelID := state.ID.ValueString()

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
	// The import ID may be either a human-readable ref or a document id. Seed only
	// model_id and leave id null; Read resolves model_id to the document id (the
	// id-absent branch) and hydrates the rest of the state.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("model_id"), req.ID)...)
}
