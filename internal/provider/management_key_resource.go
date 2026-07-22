package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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
	_ resource.Resource                   = &managementKeyResource{}
	_ resource.ResourceWithConfigure      = &managementKeyResource{}
	_ resource.ResourceWithImportState    = &managementKeyResource{}
	_ resource.ResourceWithValidateConfig = &managementKeyResource{}
)

// NewManagementKeyResource is the factory registered on the provider.
func NewManagementKeyResource() resource.Resource { return &managementKeyResource{} }

type managementKeyResource struct {
	keys client.ManagementKeysAPI
}

type managementKeyResourceModel struct {
	ID             types.String   `tfsdk:"id"`
	Name           types.String   `tfsdk:"name"`
	PermissionMode types.String   `tfsdk:"permission_mode"`
	Access         types.Map      `tfsdk:"access"`
	ExpiresAt      rfc3339Instant `tfsdk:"expires_at"`
	TokenPrefix    types.String   `tfsdk:"token_prefix"`
	Token          types.String   `tfsdk:"token"`
	Status         types.String   `tfsdk:"status"`
	CreatedAt      types.String   `tfsdk:"created_at"`
	UpdatedAt      types.String   `tfsdk:"updated_at"`
}

func (r *managementKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_management_key"
}

func (r *managementKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A workspace-scoped management key. The raw secret is returned only once on create and " +
			"is stored in state as a `Sensitive` computed attribute (`token`) — use an encrypted remote backend. " +
			"Import recovers metadata only; the secret cannot be re-read.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Management key ID assigned by orq.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Human-readable key name (1-128 characters).",
			},
			"permission_mode": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Permission preset. One of `MANAGEMENT_PERMISSION_MODE_ALL`, " +
					"`MANAGEMENT_PERMISSION_MODE_RESTRICTED`, `MANAGEMENT_PERMISSION_MODE_READ_ONLY`. " +
					"Defaults to `MANAGEMENT_PERMISSION_MODE_ALL`.",
				// The server rejects an omitted (UNSPECIFIED) permission mode on
				// create, so default it here rather than relying on a server-side
				// default that does not exist.
				Default: stringdefault.StaticString(client.ManagementPermissionModeAll),
				Validators: []validator.String{
					stringvalidator.OneOf(client.ManagementPermissionModeAll, client.ManagementPermissionModeRestricted, client.ManagementPermissionModeReadOnly),
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"access": schema.MapAttribute{
				Optional:    true,
				ElementType: types.StringType,
				MarkdownDescription: "Per-domain access map (catalog domain id → `ACCESS_LEVEL_NONE` / `ACCESS_LEVEL_READ` / " +
					"`ACCESS_LEVEL_WRITE`). Required when `permission_mode` is `MANAGEMENT_PERMISSION_MODE_RESTRICTED`; must be omitted otherwise.",
			},
			"expires_at": schema.StringAttribute{
				CustomType:          rfc3339InstantType{},
				Optional:            true,
				MarkdownDescription: "Optional expiration (RFC 3339). Must be in the future. Compared as an instant, so an equivalent value in a different UTC offset does not produce a diff.",
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
					"encrypted remote backend. Null after import (metadata-only). `token_hash` is never returned by the API.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Lifecycle status (e.g. `MANAGEMENT_KEY_STATUS_ACTIVE`).",
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

func (r *managementKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	r.keys = c.ManagementKeys()
}

func (r *managementKeyResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg managementKeyResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(validateAccessForMode(cfg.PermissionMode, cfg.Access, client.ManagementPermissionModeRestricted)...)
}

// apply writes server-returned metadata onto the model. Like the api-key
// resource it deliberately does NOT touch Token.
func (r *managementKeyResource) apply(k *client.ManagementKey, m *managementKeyResourceModel) {
	m.ID = types.StringValue(k.ID)
	m.Name = types.StringValue(k.Name)
	m.PermissionMode = optString(k.PermissionMode)
	// Only surface access when the key is RESTRICTED. After a RESTRICTED→ALL/
	// READ_ONLY switch the live server CLEARS the access map to {} (an empty map
	// on read); nulling it here keeps state aligned with config regardless.
	// ValidateConfig already forbids access unless RESTRICTED, so null here
	// matches config and never drifts.
	if k.PermissionMode == client.ManagementPermissionModeRestricted {
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

func (r *managementKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan managementKeyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	access, diags := stringMap(ctx, plan.Access)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, err := r.keys.Create(ctx, client.ManagementKeyCreateInput{
		Name:           plan.Name.ValueString(),
		PermissionMode: plan.PermissionMode.ValueString(),
		Access:         access,
		ExpiresAt:      plan.ExpiresAt.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to create management key", errDetail(err))
		return
	}
	r.apply(&res.Key, &plan)
	plan.Token = types.StringValue(res.Token)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *managementKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state managementKeyResourceModel
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
		resp.Diagnostics.AddError("Unable to read management key", errDetail(err))
		return
	}
	r.apply(k, &state) // preserves state.Token
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *managementKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan managementKeyResourceModel
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
	in := client.ManagementKeyUpdateInput{
		ID:             plan.ID.ValueString(),
		Name:           &name,
		PermissionMode: &mode,
		Access:         access,
		ExpiresAt:      plan.ExpiresAt.ValueString(),
	}
	if plan.ExpiresAt.IsNull() {
		in.ClearExpiresAt = true
	}
	k, err := r.keys.Update(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError("Unable to update management key", errDetail(err))
		return
	}
	r.apply(k, &plan) // preserves plan.Token
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *managementKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state managementKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.keys.Delete(ctx, state.ID.ValueString()); err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to delete management key", errDetail(err))
	}
}

func (r *managementKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
