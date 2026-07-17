package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

var (
	_ resource.Resource                   = &apiKeyResource{}
	_ resource.ResourceWithConfigure      = &apiKeyResource{}
	_ resource.ResourceWithImportState    = &apiKeyResource{}
	_ resource.ResourceWithValidateConfig = &apiKeyResource{}
)

// NewAPIKeyResource is the factory registered on the provider.
func NewAPIKeyResource() resource.Resource { return &apiKeyResource{} }

type apiKeyResource struct {
	keys client.APIKeysAPI
}

type apiKeyResourceModel struct {
	ID             types.String   `tfsdk:"id"`
	Name           types.String   `tfsdk:"name"`
	ProjectID      types.String   `tfsdk:"project_id"`
	PermissionMode types.String   `tfsdk:"permission_mode"`
	Access         types.Map      `tfsdk:"access"`
	ExpiresAt      rfc3339Instant `tfsdk:"expires_at"`
	TokenPrefix    types.String   `tfsdk:"token_prefix"`
	Token          types.String   `tfsdk:"token"`
	Status         types.String   `tfsdk:"status"`
	CreatedAt      types.String   `tfsdk:"created_at"`
	UpdatedAt      types.String   `tfsdk:"updated_at"`
}

func (r *apiKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_api_key"
}

func (r *apiKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An API key. The raw secret is returned only once on create and is stored in state " +
			"as a `Sensitive` computed attribute (`token`) — use an encrypted remote backend. Import recovers " +
			"metadata only; the secret cannot be re-read.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "API key ID assigned by orq.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Human-readable key name (1-128 characters).",
			},
			"project_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Single-project scope. Omit for an all-projects key. Mutable (updates in place).",
			},
			"permission_mode": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Permission preset. One of `PERMISSION_MODE_ALL`, `PERMISSION_MODE_RESTRICTED`, " +
					"`PERMISSION_MODE_READ_ONLY`. Defaults to `PERMISSION_MODE_ALL`.",
				// The server rejects an omitted (UNSPECIFIED) permission mode on
				// create, so default it here rather than relying on a server-side
				// default that does not exist.
				Default: stringdefault.StaticString(client.PermissionModeAll),
				Validators: []validator.String{
					stringvalidator.OneOf(client.PermissionModeAll, client.PermissionModeRestricted, client.PermissionModeReadOnly),
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"access": schema.MapAttribute{
				Optional:    true,
				ElementType: types.StringType,
				MarkdownDescription: "Per-domain access map (catalog domain id → `ACCESS_LEVEL_NONE` / `ACCESS_LEVEL_READ` / " +
					"`ACCESS_LEVEL_WRITE`). Required when `permission_mode` is `PERMISSION_MODE_RESTRICTED`; must be omitted otherwise.",
			},
			"expires_at": schema.StringAttribute{
				CustomType:          rfc3339InstantType{},
				Optional:            true,
				MarkdownDescription: "Optional expiration (RFC 3339). Compared as an instant, so an equivalent value in a different UTC offset does not produce a diff.",
			},
			"token_prefix": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Displayable, non-secret token prefix (e.g. `sk-orq-01HXY...`).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"token": schema.StringAttribute{
				Computed:  true,
				Sensitive: true,
				MarkdownDescription: "Raw `sk-orq-...` secret. Returned ONCE on create and stored in state — use an " +
					"encrypted remote backend. Null after import (metadata-only).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Lifecycle status (e.g. `API_KEY_STATUS_ACTIVE`).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
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

func (r *apiKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	r.keys = c.APIKeys()
}

func (r *apiKeyResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg apiKeyResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(validateAccessForMode(cfg.PermissionMode, cfg.Access, client.PermissionModeRestricted)...)
}

// validateAccessForMode enforces that the access map is present exactly when the
// permission mode is RESTRICTED. It is pure so it is unit-testable. A null/unknown
// permission_mode (computed default) is treated as non-restricted.
func validateAccessForMode(mode types.String, access types.Map, restricted string) diag.Diagnostics {
	var diags diag.Diagnostics
	isRestricted := !mode.IsNull() && !mode.IsUnknown() && mode.ValueString() == restricted
	hasAccess := !access.IsNull() && !access.IsUnknown() && len(access.Elements()) > 0
	if isRestricted && !hasAccess {
		diags.AddAttributeError(path.Root("access"), "Missing access map",
			"`access` is required when `permission_mode` is "+restricted+".")
	}
	if !isRestricted && hasAccess {
		diags.AddAttributeError(path.Root("access"), "Unexpected access map",
			"`access` must be omitted unless `permission_mode` is "+restricted+" (it is ignored server-side and would drift).")
	}
	return diags
}

// apply writes server-returned metadata onto the model. It deliberately does NOT
// touch Token: the secret is set only by Create and preserved from prior state
// on Read/Update (the server never returns it again).
func (r *apiKeyResource) apply(k *client.APIKey, m *apiKeyResourceModel) {
	m.ID = types.StringValue(k.ID)
	m.Name = types.StringValue(k.Name)
	if k.AllProjects {
		m.ProjectID = types.StringNull()
	} else {
		m.ProjectID = optString(k.ProjectID)
	}
	m.PermissionMode = optString(k.PermissionMode)
	// Only surface access when the key is RESTRICTED. After a RESTRICTED→ALL/
	// READ_ONLY switch the server retains the prior access map (a nil access in
	// the sparse update means "keep"), which would otherwise read back as a
	// non-null map against a null config and drift forever. ValidateConfig
	// already forbids access unless RESTRICTED, so null here matches config.
	if k.PermissionMode == client.PermissionModeRestricted {
		m.Access = stringMapValue(k.Access)
	} else {
		m.Access = types.MapNull(types.StringType)
	}
	m.ExpiresAt = rfc3339InstantValue(k.ExpiresAt)
	m.TokenPrefix = types.StringValue(k.TokenPrefix)
	m.Status = types.StringValue(k.Status)
	m.CreatedAt = types.StringValue(k.CreatedAt)
	m.UpdatedAt = types.StringValue(k.UpdatedAt)
}

func (r *apiKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan apiKeyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	access, diags := stringMap(ctx, plan.Access)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, err := r.keys.Create(ctx, client.APIKeyCreateInput{
		Name:           plan.Name.ValueString(),
		ProjectID:      plan.ProjectID.ValueString(),
		PermissionMode: plan.PermissionMode.ValueString(),
		Access:         access,
		ExpiresAt:      plan.ExpiresAt.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to create API key", errDetail(err))
		return
	}
	r.apply(&res.Key, &plan)
	// The one-time secret: store it now; it is never returned again.
	plan.Token = types.StringValue(res.Token)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *apiKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state apiKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	k, err := r.keys.Get(ctx, state.ID.ValueString())
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read API key", errDetail(err))
		return
	}
	r.apply(k, &state) // preserves state.Token
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *apiKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan apiKeyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	access, diags := stringMap(ctx, plan.Access)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := plan.Name.ValueString()
	mode := plan.PermissionMode.ValueString()
	in := client.APIKeyUpdateInput{
		ID:              plan.ID.ValueString(),
		Name:            &name,
		PermissionMode:  &mode,
		Access:          access,
		SetProjectScope: true,
		ProjectID:       plan.ProjectID.ValueString(),
		ExpiresAt:       plan.ExpiresAt.ValueString(),
	}
	if plan.ExpiresAt.IsNull() {
		in.ClearExpiresAt = true
	}
	k, err := r.keys.Update(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError("Unable to update API key", errDetail(err))
		return
	}
	r.apply(k, &plan) // preserves plan.Token (UseStateForUnknown carried it in)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *apiKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state apiKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.keys.Delete(ctx, state.ID.ValueString()); err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to delete API key", errDetail(err))
	}
}

func (r *apiKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
