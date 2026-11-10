package provider

import (
	"context"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/boolvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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
	_ resource.Resource                   = &environmentResource{}
	_ resource.ResourceWithConfigure      = &environmentResource{}
	_ resource.ResourceWithImportState    = &environmentResource{}
	_ resource.ResourceWithValidateConfig = &environmentResource{}
)

func NewEnvironmentResource() resource.Resource { return &environmentResource{} }

type environmentResource struct {
	environments client.EnvironmentsAPI
}

type environmentResourceModel struct {
	ID          types.String                `tfsdk:"id"`
	Slug        types.String                `tfsdk:"slug"`
	DisplayName types.String                `tfsdk:"display_name"`
	Description types.String                `tfsdk:"description"`
	ProjectID   types.String                `tfsdk:"project_id"`
	Sharing     *workspaceModelSharingModel `tfsdk:"sharing"`
	CreatedAt   types.String                `tfsdk:"created_at"`
	UpdatedAt   types.String                `tfsdk:"updated_at"`
}

var environmentSlugPattern = regexp.MustCompile(`^[a-z0-9]+(?:[-_][a-z0-9]+)*$`)

func (r *environmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_environment"
}

func (r *environmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A deployment environment. It is owned either by one project (`project_id`) or by " +
			"the workspace (`sharing`); exactly one of the two must be set and the choice cannot change " +
			"without replacement.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Environment ID assigned by orq.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"slug": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Environment slug (1-64 characters: lowercase letters and digits, separated " +
					"by single `-` or `_`). Unique among the environments a project can see. Changing this " +
					"forces replacement.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 64),
					stringvalidator.RegexMatches(environmentSlugPattern,
						"must be lowercase letters and digits separated by single '-' or '_'"),
				},
			},
			"display_name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Human-readable environment name (1-255 characters).",
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 255)},
			},
			"description": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Optional description (at most 1024 characters). Removing the attribute keeps " +
					"the stored value; set it to `\"\"` to clear it.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Validators:    []validator.String{stringvalidator.LengthAtMost(1024)},
			},
			"project_id": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Project that owns the environment. Mutually exclusive with `sharing`. " +
					"Changing this forces replacement.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					stringvalidator.ExactlyOneOf(path.MatchRoot("sharing")),
					nonEmptyStringValidator{remedy: "Omit project_id and set sharing for a workspace-owned environment."},
				},
			},
			"sharing": schema.SingleNestedAttribute{
				Optional: true,
				MarkdownDescription: "Project access for a workspace-owned environment. Mutually exclusive with " +
					"`project_id`. Exactly one of `all_projects` or `project_ids` must be set.",
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
						MarkdownDescription: "Share with exactly these projects (at most 100). An empty list means shared " +
							"with no project. Ids must be unique and non-null. Mutually exclusive with `all_projects`.",
						Validators: []validator.List{uniqueStringsValidator{
							summary:         invalidSharingSummary,
							duplicateDetail: duplicateEnvironmentProjectIDDetail,
							nullDetail:      invalidNullProjectIDDetail,
						}},
					},
					"allow_version_pin": schema.BoolAttribute{
						Optional:            true,
						Computed:            true,
						MarkdownDescription: "Allow consuming projects to pin a specific version. Omitted stores `false`.",
					},
					"allow_fork": schema.BoolAttribute{
						Optional:            true,
						Computed:            true,
						MarkdownDescription: "Allow consuming projects to fork the environment into a project-owned copy. Omitted stores `false`.",
					},
					"auto_grant_new_projects": schema.BoolAttribute{
						Optional: true,
						Computed: true,
						MarkdownDescription: "Automatically grant new projects access. Omitted stores `false`. Only valid with " +
							"`all_projects` (combining it with an explicit `project_ids` list is rejected).",
					},
				},
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Creation time (RFC 3339).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Last update time (RFC 3339).",
			},
		},
	}
}

const duplicateEnvironmentProjectIDDetail = "project_ids must not repeat a project id. The server rejects the " +
	"sharing config with \"repeated value must contain unique items\"."

func (r *environmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	r.environments = c.Environments()
}

func (r *environmentResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg environmentResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(validateSharingAutoGrant(cfg.Sharing)...)
}

func (r *environmentResource) apply(e *client.Environment, m *environmentResourceModel) {
	m.ID = types.StringValue(e.ID)
	m.Slug = types.StringValue(e.Slug)
	m.DisplayName = types.StringValue(e.DisplayName)
	m.Description = preserveEmptyString(m.Description, e.Description)
	m.ProjectID = optString(e.ProjectID)
	m.Sharing = sharingModel(m.Sharing, e.Sharing)
	m.CreatedAt = types.StringValue(e.CreatedAt)
	m.UpdatedAt = types.StringValue(e.UpdatedAt)
}

func environmentSharingInput(ctx context.Context, sharing *workspaceModelSharingModel) (*client.SharingInput, diag.Diagnostics) {
	if sharing == nil {
		return nil, nil
	}
	diags := validateSharingAutoGrant(sharing)
	if diags.HasError() {
		return nil, diags
	}
	in, inputDiags := sharing.sharingInput(ctx)
	diags.Append(inputDiags...)
	return &in, diags
}

func (r *environmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan environmentResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(revalidatePlan(ctx, req.Plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	sharing, diags := environmentSharingInput(ctx, plan.Sharing)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	e, err := r.environments.Create(ctx, client.EnvironmentCreateInput{
		Slug:        plan.Slug.ValueString(),
		DisplayName: plan.DisplayName.ValueString(),
		Description: plan.Description.ValueString(),
		ProjectID:   plan.ProjectID.ValueString(),
		Sharing:     sharing,
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to create environment", errDetail(err))
		return
	}

	r.apply(e, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *environmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state environmentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	e, err := r.environments.Get(ctx, state.ID.ValueString())
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read environment", errDetail(err))
		return
	}

	r.apply(e, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *environmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan environmentResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(revalidatePlan(ctx, req.Plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	sharing, diags := environmentSharingInput(ctx, plan.Sharing)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := plan.DisplayName.ValueString()
	e, err := r.environments.Update(ctx, client.EnvironmentUpdateInput{
		ID:          plan.ID.ValueString(),
		DisplayName: &name,
		Description: strPtr(plan.Description),
		Sharing:     sharing,
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to update environment", errDetail(err))
		return
	}

	r.apply(e, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *environmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state environmentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.environments.Delete(ctx, state.ID.ValueString()); err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to delete environment", errDetail(err))
	}
}

func (r *environmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
