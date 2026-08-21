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
	_ resource.Resource                = &notifierResource{}
	_ resource.ResourceWithConfigure   = &notifierResource{}
	_ resource.ResourceWithImportState = &notifierResource{}
)

// NewNotifierResource is the factory registered on the provider.
func NewNotifierResource() resource.Resource { return &notifierResource{} }

type notifierResource struct {
	notifiers client.NotifiersAPI
}

type notifierResourceModel struct {
	ID                 types.String `tfsdk:"id"`
	DisplayName        types.String `tfsdk:"display_name"`
	Type               types.String `tfsdk:"type"`
	ProjectID          types.String `tfsdk:"project_id"`
	Emails             types.List   `tfsdk:"emails"`
	IncomingWebhookURL types.String `tfsdk:"incoming_webhook_url"`
	WebhookURL         types.String `tfsdk:"webhook_url"`
	CreatedAt          types.String `tfsdk:"created_at"`
	UpdatedAt          types.String `tfsdk:"updated_at"`
}

func (r *notifierResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_notifier"
}

func (r *notifierResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A notifier destination (email, Slack webhook, or generic webhook).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Notifier ID assigned by orq.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"display_name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Human-readable notifier name (1-255 characters).",
			},
			"type": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Destination type: `EMAIL`, `SLACK_WEBHOOK`, or `WEBHOOK`.",
				Validators: []validator.String{
					stringvalidator.OneOf(client.NotifierTypeEmail, client.NotifierTypeSlackWebhook, client.NotifierTypeWebhook),
				},
			},
			"project_id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Containing project. Omit for a workspace-wide notifier.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"emails": schema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				MarkdownDescription: "Email recipients. Required when `type` is `EMAIL`. Addresses must be unique " +
					"and non-null — the server stores a deduplicated list.",
				Validators: []validator.List{
					listvalidator.UniqueValues(),
					listvalidator.NoNullValues(),
				},
			},
			"incoming_webhook_url": schema.StringAttribute{
				Optional:            true,
				Sensitive:           true,
				MarkdownDescription: "Slack incoming webhook URL. Required when `type` is `SLACK_WEBHOOK`.",
			},
			"webhook_url": schema.StringAttribute{
				Optional:            true,
				Sensitive:           true,
				MarkdownDescription: "Generic webhook URL. Required when `type` is `WEBHOOK`.",
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

func (r *notifierResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	r.notifiers = c.Notifiers()
}

// apply projects the server record onto the model, which still holds the planned
// (create/update) or prior (read) values. An empty result on the wire cannot be
// told apart from an unset field, so the configured empty-vs-absent shape is
// preserved: `emails = []` must not read back as null, and neither must an
// explicitly empty URL.
func (r *notifierResource) apply(n *client.Notifier, m *notifierResourceModel) {
	m.ID = types.StringValue(n.ID)
	m.DisplayName = types.StringValue(n.DisplayName)
	m.Type = types.StringValue(n.Type)
	m.ProjectID = preserveEmptyString(m.ProjectID, n.ProjectID)
	m.Emails = preserveEmptyList(m.Emails, n.Emails)
	m.IncomingWebhookURL = preserveEmptyString(m.IncomingWebhookURL, n.IncomingWebhookURL)
	m.WebhookURL = preserveEmptyString(m.WebhookURL, n.WebhookURL)
	m.CreatedAt = types.StringValue(n.CreatedAt)
	m.UpdatedAt = types.StringValue(n.UpdatedAt)
}

const (
	invalidNotifierEmailsSummary = "Invalid emails"
	duplicateNotifierEmailDetail = "emails must not repeat an address. The server stores a deduplicated list, so " +
		"the extra element vanishes from the read-back and taints the notifier."
	nullNotifierEmailDetail = "emails must not contain a null element. Remove it, or use an empty list for no recipients."
)

// validateNotifierEmails is the apply-time backstop for the schema validators,
// which defer on an interpolated list. It runs before the write, so a rejected
// config never orphans a notifier.
func validateNotifierEmails(l types.List) diag.Diagnostics {
	return uniqueNonNullStrings(l, path.Root("emails"),
		invalidNotifierEmailsSummary, duplicateNotifierEmailDetail, nullNotifierEmailDetail)
}

func (r *notifierResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan notifierResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(validateNotifierEmails(plan.Emails)...)
	emails, diags := stringSlice(ctx, plan.Emails)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	n, err := r.notifiers.Create(ctx, client.NotifierCreateInput{
		ProjectID:          plan.ProjectID.ValueString(),
		DisplayName:        plan.DisplayName.ValueString(),
		Type:               plan.Type.ValueString(),
		Emails:             emails,
		IncomingWebhookURL: plan.IncomingWebhookURL.ValueString(),
		WebhookURL:         plan.WebhookURL.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to create notifier", errDetail(err))
		return
	}

	// Preserve the sensitive URLs from the plan: the server may mask or omit
	// them on the write response, and we don't want state to drop the value.
	preserveNotifierSecrets(&plan, n)
	r.apply(n, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *notifierResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state notifierResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	n, err := r.notifiers.Get(ctx, state.ID.ValueString())
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read notifier", errDetail(err))
		return
	}

	prior := state
	r.apply(n, &state)
	// If the server returned an empty URL (masked), keep the prior known value.
	if n.IncomingWebhookURL == "" && !prior.IncomingWebhookURL.IsNull() {
		state.IncomingWebhookURL = prior.IncomingWebhookURL
	}
	if n.WebhookURL == "" && !prior.WebhookURL.IsNull() {
		state.WebhookURL = prior.WebhookURL
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *notifierResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan notifierResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(validateNotifierEmails(plan.Emails)...)
	emails, diags := stringSlice(ctx, plan.Emails)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := plan.DisplayName.ValueString()
	ntype := plan.Type.ValueString()
	n, err := r.notifiers.Update(ctx, client.NotifierUpdateInput{
		ID:                 plan.ID.ValueString(),
		ProjectID:          strPtr(plan.ProjectID),
		DisplayName:        &name,
		Type:               &ntype,
		Emails:             emails,
		IncomingWebhookURL: strPtr(plan.IncomingWebhookURL),
		WebhookURL:         strPtr(plan.WebhookURL),
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to update notifier", errDetail(err))
		return
	}

	preserveNotifierSecrets(&plan, n)
	r.apply(n, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *notifierResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state notifierResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.notifiers.Delete(ctx, state.ID.ValueString()); err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to delete notifier", errDetail(err))
	}
}

func (r *notifierResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// preserveNotifierSecrets keeps the plan's configured webhook URLs on the
// returned projection when the server masks/omits them on a write response.
func preserveNotifierSecrets(plan *notifierResourceModel, n *client.Notifier) {
	if n.IncomingWebhookURL == "" && !plan.IncomingWebhookURL.IsNull() && !plan.IncomingWebhookURL.IsUnknown() {
		n.IncomingWebhookURL = plan.IncomingWebhookURL.ValueString()
	}
	if n.WebhookURL == "" && !plan.WebhookURL.IsNull() && !plan.WebhookURL.IsUnknown() {
		n.WebhookURL = plan.WebhookURL.ValueString()
	}
}
