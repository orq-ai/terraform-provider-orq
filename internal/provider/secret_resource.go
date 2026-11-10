package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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
	_ resource.Resource                = &secretResource{}
	_ resource.ResourceWithConfigure   = &secretResource{}
	_ resource.ResourceWithImportState = &secretResource{}
)

func NewSecretResource() resource.Resource { return &secretResource{} }

type secretResource struct {
	secrets client.SecretsAPI
}

type secretResourceModel struct {
	ID          types.String `tfsdk:"id"`
	VaultID     types.String `tfsdk:"vault_id"`
	Name        types.String `tfsdk:"name"`
	Kind        types.String `tfsdk:"kind"`
	Value       types.String `tfsdk:"value"`
	Description types.String `tfsdk:"description"`
	CreatedAt   types.String `tfsdk:"created_at"`
	UpdatedAt   types.String `tfsdk:"updated_at"`
}

const (
	maxSecretNameLength        = 128
	maxSecretValueBytes        = 65536
	maxSecretDescriptionLength = 2000
	secretImportIDSeparator    = "/"
)

var secretNamePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

func (r *secretResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_secret"
}

func (r *secretResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A secret stored in a vault and exposed to factory work items as an environment variable. " +
			"The value is write-only on the server: it is never read back, so it is kept in Terraform state as configured " +
			"and a change made outside Terraform is not detected.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Secret ID assigned by orq.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"vault_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Vault that holds the secret. Changing it replaces the secret.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          []validator.String{stringvalidator.LengthBetween(1, maxVaultEntityIDLength)},
			},
			"name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Environment variable name the work item sees: uppercase letters, digits and underscores, " +
					"unique within the vault. Names starting with `ORQ_` or `_` are reserved. Changing it replaces the secret.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, maxSecretNameLength),
					stringvalidator.RegexMatches(secretNamePattern, "must contain only uppercase letters, digits and underscores, and must not start with a digit"),
				},
			},
			"kind": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "How the value is exposed: `VARIABLE` (plain configuration, not redacted), `SECRET` " +
					"(redacted from events, transcripts, commits and pushes) or `SETUP_SECRET` (present only while blueprint " +
					"setup and maintenance run).",
				Validators: []validator.String{
					stringvalidator.OneOf(client.SecretKindVariable, client.SecretKindSecret, client.SecretKindSetupSecret),
				},
			},
			"value": schema.StringAttribute{
				Required:            true,
				Sensitive:           true,
				MarkdownDescription: "Secret value (up to 65536 bytes). Never returned by the API; changing it rotates the stored value.",
				Validators:          []validator.String{stringvalidator.LengthAtMost(maxSecretValueBytes)},
			},
			"description": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Free-form description (up to 2000 characters).",
				Validators:          []validator.String{stringvalidator.UTF8LengthAtMost(maxSecretDescriptionLength)},
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

func (r *secretResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	r.secrets = c.Secrets()
}

// apply leaves Value untouched: the server never returns it.
func (r *secretResource) apply(secret *client.Secret, m *secretResourceModel) {
	m.ID = types.StringValue(secret.ID)
	m.VaultID = types.StringValue(secret.VaultID)
	m.Name = types.StringValue(secret.Name)
	m.Kind = types.StringValue(secret.Kind)
	m.Description = preserveEmptyString(m.Description, secret.Description)
	m.CreatedAt = types.StringValue(secret.CreatedAt)
	m.UpdatedAt = types.StringValue(secret.UpdatedAt)
}

func (r *secretResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan secretResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(revalidatePlan(ctx, req.Plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	secret, err := r.secrets.Create(ctx, client.SecretCreateInput{
		VaultID:     plan.VaultID.ValueString(),
		Name:        plan.Name.ValueString(),
		Kind:        plan.Kind.ValueString(),
		Value:       plan.Value.ValueString(),
		Description: plan.Description.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to create secret", errDetail(err))
		return
	}

	r.apply(secret, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *secretResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state secretResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	secret, err := r.secrets.Get(ctx, state.VaultID.ValueString(), state.ID.ValueString())
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read secret", errDetail(err))
		return
	}

	r.apply(secret, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func rotatedSecretValue(plan, state types.String) *string {
	if plan.Equal(state) {
		return nil
	}
	return strPtr(plan)
}

func (r *secretResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state secretResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(revalidatePlan(ctx, req.Plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	kind := plan.Kind.ValueString()
	description := plan.Description.ValueString()
	secret, err := r.secrets.Update(ctx, client.SecretUpdateInput{
		VaultID:     plan.VaultID.ValueString(),
		ID:          plan.ID.ValueString(),
		Kind:        &kind,
		Value:       rotatedSecretValue(plan.Value, state.Value),
		Description: &description,
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to update secret", errDetail(err))
		return
	}

	r.apply(secret, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *secretResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state secretResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.secrets.Delete(ctx, state.VaultID.ValueString(), state.ID.ValueString()); err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to delete secret", errDetail(err))
	}
}

func (r *secretResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	vaultID, secretID, found := strings.Cut(req.ID, secretImportIDSeparator)
	if !found || vaultID == "" || secretID == "" || strings.Contains(secretID, secretImportIDSeparator) {
		resp.Diagnostics.AddError("Invalid secret import id",
			fmt.Sprintf("Expected <vault_id>/<secret_id>, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vault_id"), vaultID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), secretID)...)
}
