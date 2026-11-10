package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
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
	_ resource.Resource                = &vaultResource{}
	_ resource.ResourceWithConfigure   = &vaultResource{}
	_ resource.ResourceWithImportState = &vaultResource{}
)

func NewVaultResource() resource.Resource { return &vaultResource{} }

type vaultResource struct {
	vaults client.VaultsAPI
}

type vaultResourceModel struct {
	ID               types.String `tfsdk:"id"`
	DisplayName      types.String `tfsdk:"display_name"`
	Description      types.String `tfsdk:"description"`
	FactoryID        types.String `tfsdk:"factory_id"`
	SharedFactoryIDs types.List   `tfsdk:"shared_factory_ids"`
	CreatedAt        types.String `tfsdk:"created_at"`
	UpdatedAt        types.String `tfsdk:"updated_at"`
}

const (
	maxSharedFactoryIDs       = 100
	maxVaultDisplayNameLength = 100
	maxVaultDescriptionLength = 2000
	maxVaultEntityIDLength    = 64

	invalidSharedFactoryIDsSummary = "Invalid shared_factory_ids"
	duplicateSharedFactoryIDDetail = "shared_factory_ids must not repeat a factory id. The server rejects the vault."
	nullSharedFactoryIDDetail      = "shared_factory_ids must not contain a null element. Remove it, or use an empty list to share the vault with no factory."
)

func (r *vaultResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vault"
}

func (r *vaultResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A vault: a named collection of secrets that factories attach to expose them as environment " +
			"variables to work items. A vault is owned by one factory (`factory_id`) or by the workspace. " +
			"Deleting a vault deletes its secrets and is rejected while a factory still has it attached.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Vault ID assigned by orq.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"display_name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Vault name (1-100 characters), unique within the workspace.",
				Validators:          []validator.String{stringvalidator.UTF8LengthBetween(1, maxVaultDisplayNameLength)},
			},
			"description": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Free-form description (up to 2000 characters).",
				Validators:          []validator.String{stringvalidator.UTF8LengthAtMost(maxVaultDescriptionLength)},
			},
			"factory_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Owning factory; only that factory can attach the vault. Omit for a workspace vault. Changing it replaces the vault.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					nonEmptyStringValidator{remedy: "Omit factory_id for a workspace vault."},
					stringvalidator.LengthAtMost(maxVaultEntityIDLength),
				},
			},
			"shared_factory_ids": schema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				MarkdownDescription: "Factories allowed to attach a workspace vault. Omit to share the vault with every factory; " +
					"an empty list shares it with none. Cannot be combined with `factory_id`. " +
					"Removing a factory that still has the vault attached is rejected.",
				Validators: []validator.List{
					listvalidator.ConflictsWith(path.MatchRoot("factory_id")),
					listvalidator.SizeAtMost(maxSharedFactoryIDs),
					listvalidator.ValueStringsAre(stringvalidator.LengthBetween(1, maxVaultEntityIDLength)),
					uniqueStringsValidator{
						summary:         invalidSharedFactoryIDsSummary,
						duplicateDetail: duplicateSharedFactoryIDDetail,
						nullDetail:      nullSharedFactoryIDDetail,
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

func (r *vaultResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	r.vaults = c.Vaults()
}

func (m *vaultResourceModel) isFactoryOwned() bool {
	return !m.FactoryID.IsNull() && !m.FactoryID.IsUnknown()
}

func (m *vaultResourceModel) workspaceSharing(ctx context.Context) (*client.VaultSharing, diag.Diagnostics) {
	if m.isFactoryOwned() {
		return nil, nil
	}
	if m.SharedFactoryIDs.IsNull() || m.SharedFactoryIDs.IsUnknown() {
		return &client.VaultSharing{AllFactories: true}, nil
	}
	factoryIDs, diags := stringSlice(ctx, m.SharedFactoryIDs)
	return &client.VaultSharing{FactoryIDs: factoryIDs}, diags
}

func sharedFactoryIDsValue(prior types.List, sharing *client.VaultSharing) types.List {
	if sharing == nil || sharing.AllFactories {
		return types.ListNull(types.StringType)
	}
	return preserveListOrder(prior, sharing.FactoryIDs)
}

func (r *vaultResource) apply(vault *client.Vault, m *vaultResourceModel) {
	m.ID = types.StringValue(vault.ID)
	m.DisplayName = types.StringValue(vault.DisplayName)
	m.Description = preserveEmptyString(m.Description, vault.Description)
	m.FactoryID = optString(vault.FactoryID)
	m.SharedFactoryIDs = sharedFactoryIDsValue(m.SharedFactoryIDs, vault.Sharing)
	m.CreatedAt = types.StringValue(vault.CreatedAt)
	m.UpdatedAt = types.StringValue(vault.UpdatedAt)
}

func (r *vaultResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan vaultResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(revalidatePlan(ctx, req.Plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	sharing, diags := plan.workspaceSharing(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	vault, err := r.vaults.Create(ctx, client.VaultCreateInput{
		DisplayName: plan.DisplayName.ValueString(),
		Description: plan.Description.ValueString(),
		FactoryID:   plan.FactoryID.ValueString(),
		Sharing:     sharing,
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to create vault", errDetail(err))
		return
	}

	r.apply(vault, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *vaultResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state vaultResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	vault, err := r.vaults.Get(ctx, state.ID.ValueString())
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read vault", errDetail(err))
		return
	}

	r.apply(vault, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *vaultResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan vaultResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(revalidatePlan(ctx, req.Plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	sharing, diags := plan.workspaceSharing(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	displayName := plan.DisplayName.ValueString()
	description := plan.Description.ValueString()
	vault, err := r.vaults.Update(ctx, client.VaultUpdateInput{
		ID:          plan.ID.ValueString(),
		DisplayName: &displayName,
		Description: &description,
		Sharing:     sharing,
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to update vault", errDetail(err))
		return
	}

	r.apply(vault, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *vaultResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state vaultResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.vaults.Delete(ctx, state.ID.ValueString()); err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to delete vault", errDetail(err))
	}
}

func (r *vaultResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
