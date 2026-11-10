package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

var (
	_ resource.Resource                = &telemetryDestinationResource{}
	_ resource.ResourceWithConfigure   = &telemetryDestinationResource{}
	_ resource.ResourceWithImportState = &telemetryDestinationResource{}
)

func NewTelemetryDestinationResource() resource.Resource { return &telemetryDestinationResource{} }

type telemetryDestinationResource struct {
	destinations client.TelemetryDestinationsAPI
}

type telemetryDestinationResourceModel struct {
	ID             types.String `tfsdk:"id"`
	DisplayName    types.String `tfsdk:"display_name"`
	EndpointURL    types.String `tfsdk:"endpoint_url"`
	RequestHeaders types.Map    `tfsdk:"request_headers"`
	ContentMode    types.String `tfsdk:"content_mode"`
	ProjectScope   types.String `tfsdk:"project_scope"`
	ProjectIDs     types.List   `tfsdk:"project_ids"`
	Enabled        types.Bool   `tfsdk:"enabled"`
	CreatedAt      types.String `tfsdk:"created_at"`
	UpdatedAt      types.String `tfsdk:"updated_at"`
}

const (
	invalidTelemetryProjectIDsSummary = "Invalid project_ids"
	duplicateTelemetryProjectIDDetail = "project_ids must not repeat a project. The server stores a deduplicated list, " +
		"so the extra element vanishes from the read-back."
	nullTelemetryProjectIDDetail = "project_ids must not contain a null element."
)

func (r *telemetryDestinationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_telemetry_destination"
}

func (r *telemetryDestinationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An OpenTelemetry (OTLP/HTTP) destination that workspace traces are exported to. " +
			"A workspace holds at most 10 destinations, and the management key must be workspace-scoped.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Destination ID assigned by orq.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"display_name": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Destination name, unique within the workspace (up to 160 characters). Defaults to the endpoint host.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Validators: []validator.String{
					stringvalidator.LengthAtMost(160),
					nonEmptyStringValidator{remedy: "Omit display_name to default it to the endpoint host."},
				},
			},
			"endpoint_url": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Full public HTTPS OTLP/HTTP traces URL, including the path such as `/v1/traces`.",
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 2048)},
			},
			"request_headers": schema.MapAttribute{
				Optional:    true,
				Sensitive:   true,
				ElementType: types.StringType,
				MarkdownDescription: "Headers sent with every export, typically authentication (up to 20). Values are " +
					"write-only: orq never returns them, so a value changed outside Terraform is not detected, and an " +
					"imported destination reads its header names with empty values until the configuration is applied.",
			},
			"content_mode": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "`METADATA_ONLY` exports spans without prompt and completion content; `FULL_CONTENT` includes it.",
				Validators: []validator.String{
					stringvalidator.OneOf(client.TelemetryContentModeMetadataOnly, client.TelemetryContentModeFullContent),
				},
			},
			"project_scope": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Which projects are exported: `ALL`, `SELECTED` (only `project_ids`), or " +
					"`ALL_EXCEPT` (everything but `project_ids`).",
				Validators: []validator.String{
					stringvalidator.OneOf(client.TelemetryProjectScopeAll, client.TelemetryProjectScopeSelected, client.TelemetryProjectScopeAllExcept),
				},
			},
			"project_ids": schema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				MarkdownDescription: "Projects the scope applies to (up to 1000). Required for `SELECTED` and " +
					"`ALL_EXCEPT`, and must be omitted for `ALL`.",
				Validators: []validator.List{uniqueStringsValidator{
					summary:         invalidTelemetryProjectIDsSummary,
					duplicateDetail: duplicateTelemetryProjectIDDetail,
					nullDetail:      nullTelemetryProjectIDDetail,
				}},
			},
			"enabled": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
				MarkdownDescription: "Whether spans are exported. Defaults to `true`. orq disables a destination after " +
					"repeated delivery failures, which shows up as drift.",
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

func (r *telemetryDestinationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	r.destinations = c.TelemetryDestinations()
}

func (r *telemetryDestinationResource) apply(d *client.TelemetryDestination, m *telemetryDestinationResourceModel) {
	m.ID = types.StringValue(d.ID)
	m.DisplayName = types.StringValue(d.DisplayName)
	m.EndpointURL = types.StringValue(d.EndpointURL)
	m.RequestHeaders = retainHeaderValues(m.RequestHeaders, d.RequestHeaderNames)
	m.ContentMode = types.StringValue(d.ContentMode)
	m.ProjectScope = types.StringValue(d.ProjectScope)
	m.ProjectIDs = preserveEmptyList(m.ProjectIDs, d.ProjectIDs)
	m.Enabled = types.BoolValue(d.Enabled)
	m.CreatedAt = types.StringValue(d.CreatedAt)
	m.UpdatedAt = types.StringValue(d.UpdatedAt)
}

// retainHeaderValues keeps the known values for the header names the server still holds, matched
// case-insensitively because the server lowercases names; a name it holds that is not known reads as "".
func retainHeaderValues(known types.Map, serverNames []string) types.Map {
	if len(serverNames) == 0 {
		if known.IsNull() || known.IsUnknown() {
			return types.MapNull(types.StringType)
		}
		return types.MapValueMust(types.StringType, nil)
	}
	held := make(map[string]bool, len(serverNames))
	for _, name := range serverNames {
		held[name] = false
	}
	headers := make(map[string]attr.Value, len(serverNames))
	if !known.IsNull() && !known.IsUnknown() {
		for name, value := range known.Elements() {
			if _, ok := held[strings.ToLower(name)]; ok {
				headers[name] = value
				held[strings.ToLower(name)] = true
			}
		}
	}
	for name, retained := range held {
		if !retained {
			headers[name] = types.StringValue("")
		}
	}
	return types.MapValueMust(types.StringType, headers)
}

func telemetryDestinationInput(ctx context.Context, plan telemetryDestinationResourceModel) (client.TelemetryDestinationInput, diag.Diagnostics) {
	headers, diags := stringMap(ctx, plan.RequestHeaders)
	projectIDs, projectDiags := stringSlice(ctx, plan.ProjectIDs)
	diags.Append(projectDiags...)
	return client.TelemetryDestinationInput{
		DisplayName:    plan.DisplayName.ValueString(),
		EndpointURL:    plan.EndpointURL.ValueString(),
		RequestHeaders: headers,
		ContentMode:    plan.ContentMode.ValueString(),
		ProjectScope:   plan.ProjectScope.ValueString(),
		ProjectIDs:     projectIDs,
		Enabled:        plan.Enabled.ValueBool(),
	}, diags
}

func (r *telemetryDestinationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan telemetryDestinationResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(revalidatePlan(ctx, req.Plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	input, diags := telemetryDestinationInput(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	d, err := r.destinations.Create(ctx, input)
	if err != nil {
		resp.Diagnostics.AddError("Unable to create telemetry destination", errDetail(err))
		return
	}
	r.apply(d, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *telemetryDestinationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state telemetryDestinationResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	d, err := r.destinations.Get(ctx, state.ID.ValueString())
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read telemetry destination", errDetail(err))
		return
	}
	r.apply(d, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *telemetryDestinationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan telemetryDestinationResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(revalidatePlan(ctx, req.Plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	input, diags := telemetryDestinationInput(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	d, err := r.destinations.Update(ctx, plan.ID.ValueString(), input)
	if err != nil {
		resp.Diagnostics.AddError("Unable to update telemetry destination", errDetail(err))
		return
	}
	r.apply(d, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *telemetryDestinationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state telemetryDestinationResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.destinations.Delete(ctx, state.ID.ValueString()); err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to delete telemetry destination", errDetail(err))
	}
}

func (r *telemetryDestinationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
