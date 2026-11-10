package provider

import (
	"context"
	"fmt"
	"reflect"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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
	_ resource.Resource                = &smartRouterResource{}
	_ resource.ResourceWithConfigure   = &smartRouterResource{}
	_ resource.ResourceWithImportState = &smartRouterResource{}
)

var (
	gatewayModelKeyPattern       = regexp.MustCompile(`^[a-z0-9-]+$`)
	modelReferencePattern        = regexp.MustCompile(`^[^/]+/.+$`)
	surroundingWhitespacePattern = regexp.MustCompile(`^\S(.*\S)?$`)
)

func NewSmartRouterResource() resource.Resource { return &smartRouterResource{} }

type smartRouterResource struct {
	routers client.SmartRoutersAPI
}

type smartRouterResourceModel struct {
	ID              types.String                     `tfsdk:"id"`
	Key             types.String                     `tfsdk:"key"`
	ModelRef        types.String                     `tfsdk:"model_ref"`
	Models          types.List                       `tfsdk:"models"`
	Profile         types.String                     `tfsdk:"profile"`
	SessionAffinity *smartRouterSessionAffinityModel `tfsdk:"session_affinity"`
	Enabled         types.Bool                       `tfsdk:"enabled"`
	CreatedAt       types.String                     `tfsdk:"created_at"`
	UpdatedAt       types.String                     `tfsdk:"updated_at"`
}

type smartRouterSessionAffinityModel struct {
	Enabled    types.Bool  `tfsdk:"enabled"`
	TTLSeconds types.Int64 `tfsdk:"ttl_seconds"`
}

func gatewayModelKeyValidators() []validator.String {
	return []validator.String{
		stringvalidator.LengthBetween(1, 128),
		stringvalidator.RegexMatches(gatewayModelKeyPattern, "must contain only lowercase letters, numbers, and hyphens"),
	}
}

func modelReferenceValidators() []validator.String {
	return []validator.String{
		stringvalidator.LengthBetween(3, 256),
		stringvalidator.RegexMatches(modelReferencePattern, "must be a model reference in `provider/model` format"),
		stringvalidator.RegexMatches(surroundingWhitespacePattern, "must not have leading or trailing whitespace"),
	}
}

func (r *smartRouterResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_smart_router"
}

func (r *smartRouterResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A workspace Smart Router: a virtual gateway model that picks one model from a pool " +
			"for each request according to a quality, balanced, or cost profile.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Smart Router ID assigned by orq.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"key": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Key that identifies the Smart Router within the workspace and forms its " +
					"model reference. Lowercase letters, numbers, and hyphens. Changing this forces replacement.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    gatewayModelKeyValidators(),
			},
			"model_ref": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Model reference to use in AI Gateway requests, in " +
					"`<workspace-key>@orq/<key>` format.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"models": schema.ListAttribute{
				Required:    true,
				ElementType: types.StringType,
				MarkdownDescription: "Pool of 2 to 50 distinct models in `provider/model` format. Every model " +
					"must be available to the workspace and eligible for Smart Router use.",
				Validators: []validator.List{
					listvalidator.SizeBetween(2, 50),
					listvalidator.UniqueValues(),
					listvalidator.ValueStringsAre(modelReferenceValidators()...),
				},
			},
			"profile": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Routing preference: `quality` favors more capable models, `cost` favors " +
					"less expensive models, and `balanced` trades off both.",
				Validators: []validator.String{stringvalidator.OneOf(client.SmartRouterProfiles...)},
			},
			"session_affinity": schema.SingleNestedAttribute{
				Optional: true,
				MarkdownDescription: "Session affinity settings. Removing the block from a managed router " +
					"turns session affinity off.",
				Attributes: map[string]schema.Attribute{
					"enabled": schema.BoolAttribute{
						Required: true,
						MarkdownDescription: "Whether later turns of a thread stay on the model chosen for its " +
							"first turn. Only requests that carry a thread id take part.",
					},
					"ttl_seconds": schema.Int64Attribute{
						Optional: true,
						MarkdownDescription: "Sliding idle window in seconds, between 60 and 3600. The gateway " +
							"uses 600 when omitted.",
						Validators: []validator.Int64{int64validator.Between(60, 3600)},
					},
				},
			},
			"enabled": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
				MarkdownDescription: "Whether the Smart Router can be used in AI Gateway requests. Defaults to `true`.",
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

func (r *smartRouterResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	r.routers = c.SmartRouters()
}

func (r *smartRouterResource) apply(g *client.SmartRouter, m *smartRouterResourceModel) {
	m.ID = types.StringValue(g.ID)
	m.Key = types.StringValue(g.Key)
	m.ModelRef = types.StringValue(g.ModelRef)
	m.Models = stringListValue(g.Models)
	m.Profile = types.StringValue(g.Profile)
	m.SessionAffinity = sessionAffinityModel(m.SessionAffinity, g.SessionAffinity)
	m.Enabled = types.BoolValue(g.Enabled)
	m.CreatedAt = types.StringValue(g.CreatedAt)
	m.UpdatedAt = types.StringValue(g.UpdatedAt)
}

// The API cannot unset session affinity, so a stored "off" with no TTL reads as an absent block.
func sessionAffinityModel(prior *smartRouterSessionAffinityModel, affinity *client.SmartRouterSessionAffinity) *smartRouterSessionAffinityModel {
	if affinity == nil {
		return nil
	}
	if prior == nil && !affinity.Enabled && affinity.TTLSeconds == nil {
		return nil
	}
	return &smartRouterSessionAffinityModel{
		Enabled:    types.BoolValue(affinity.Enabled),
		TTLSeconds: types.Int64PointerValue(affinity.TTLSeconds),
	}
}

func (m *smartRouterSessionAffinityModel) input() *client.SmartRouterSessionAffinity {
	if m == nil {
		return nil
	}
	return &client.SmartRouterSessionAffinity{
		Enabled:    m.Enabled.ValueBool(),
		TTLSeconds: int64Ptr(m.TTLSeconds),
	}
}

func (m *smartRouterResourceModel) sessionAffinityUpdate(state *smartRouterResourceModel) *client.SmartRouterSessionAffinity {
	if m.SessionAffinity == nil && state.SessionAffinity != nil {
		return &client.SmartRouterSessionAffinity{Enabled: false}
	}
	return m.SessionAffinity.input()
}

func (m *smartRouterResourceModel) configurationChanged(state *smartRouterResourceModel) bool {
	return !m.Models.Equal(state.Models) ||
		!m.Profile.Equal(state.Profile) ||
		!reflect.DeepEqual(m.SessionAffinity.input(), state.SessionAffinity.input())
}

func (r *smartRouterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan smartRouterResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(revalidatePlan(ctx, req.Plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	models, diags := stringSlice(ctx, plan.Models)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, err := r.routers.Create(ctx, client.SmartRouterCreateInput{
		Key:             plan.Key.ValueString(),
		Models:          models,
		Profile:         plan.Profile.ValueString(),
		SessionAffinity: plan.SessionAffinity.input(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to create smart router", errDetail(err))
		return
	}
	if !plan.Enabled.ValueBool() {
		disabled, err := r.routers.SetEnabled(ctx, g.ID, false)
		if err != nil {
			resp.Diagnostics.AddError("Unable to disable smart router", errDetail(err))
		} else {
			g = disabled
		}
	}
	r.apply(g, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *smartRouterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state smartRouterResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, err := r.routers.Get(ctx, state.ID.ValueString())
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read smart router", errDetail(err))
		return
	}
	r.apply(g, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *smartRouterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state smartRouterResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(revalidatePlan(ctx, req.Plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, diags := r.reconcile(ctx, &plan, &state)
	resp.Diagnostics.Append(diags...)
	if g == nil {
		return
	}
	r.apply(g, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// A pure enabled toggle skips the configuration write, which re-resolves every pool model.
func (r *smartRouterResource) reconcile(ctx context.Context, plan, state *smartRouterResourceModel) (*client.SmartRouter, diag.Diagnostics) {
	var diags diag.Diagnostics
	var g *client.SmartRouter
	enabledChanged := !plan.Enabled.Equal(state.Enabled)
	if !enabledChanged || plan.configurationChanged(state) {
		models, listDiags := stringSlice(ctx, plan.Models)
		diags.Append(listDiags...)
		if diags.HasError() {
			return nil, diags
		}
		updated, err := r.routers.Update(ctx, client.SmartRouterUpdateInput{
			ID:              plan.ID.ValueString(),
			Models:          models,
			Profile:         plan.Profile.ValueString(),
			SessionAffinity: plan.sessionAffinityUpdate(state),
		})
		if err != nil {
			diags.AddError("Unable to update smart router", errDetail(err))
			return nil, diags
		}
		g = updated
	}
	if enabledChanged {
		toggled, err := r.routers.SetEnabled(ctx, plan.ID.ValueString(), plan.Enabled.ValueBool())
		if err != nil {
			diags.AddError("Unable to set smart router enabled state", errDetail(err))
			return g, diags
		}
		g = toggled
	}
	return g, diags
}

func (r *smartRouterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state smartRouterResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.routers.Delete(ctx, state.ID.ValueString()); err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to delete smart router", errDetail(err))
	}
}

func (r *smartRouterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
