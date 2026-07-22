package provider

import (
	"context"
	"fmt"
	"net/url"

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
	_ resource.Resource                = &modelResource{}
	_ resource.ResourceWithConfigure   = &modelResource{}
	_ resource.ResourceWithImportState = &modelResource{}
)

// modelTypeValues are the accepted model_type discriminators for a custom
// openai-like model (POST /v2/models/openai-like).
var modelTypeValues = []string{
	"chat", "embedding", "image", "rerank", "stt", "tts", "moderation", "realtime", "completion",
}

// NewModelResource is the factory registered on the provider.
func NewModelResource() resource.Resource { return &modelResource{} }

type modelResource struct {
	models client.ModelsAPI
}

type modelResourceModel struct {
	ID          types.String `tfsdk:"id"`
	DisplayName types.String `tfsdk:"display_name"`
	ModelID     types.String `tfsdk:"model_id"`
	ModelType   types.String `tfsdk:"model_type"`
	Region      types.String `tfsdk:"region"`
	BaseURL     types.String `tfsdk:"base_url"`
	APIKey      types.String `tfsdk:"api_key"`
	Description types.String `tfsdk:"description"`

	InputCost           types.Float64 `tfsdk:"input_cost"`
	OutputCost          types.Float64 `tfsdk:"output_cost"`
	CostPerImage        types.Float64 `tfsdk:"cost_per_image"`
	MaxTokens           types.Int64   `tfsdk:"max_tokens"`
	Temperature         types.Float64 `tfsdk:"temperature"`
	HasReasoning        types.Bool    `tfsdk:"has_reasoning"`
	SupportsVision      types.Bool    `tfsdk:"supports_vision"`
	SupportsToolCalling types.Bool    `tfsdk:"supports_tool_calling"`
	SupportsStrictTool  types.Bool    `tfsdk:"supports_strict_tool"`
	SupportsImageEdit   types.Bool    `tfsdk:"supports_image_edit"`

	Created types.String `tfsdk:"created"`
	Updated types.String `tfsdk:"updated"`
}

func (r *modelResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_model"
}

func (r *modelResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A custom OpenAI-compatible (\"openai-like\") model registered in the workspace " +
			"catalog (POST /v2/models/openai-like). This resource manages ONLY custom openai-like models " +
			"(server `provider` = `openailike`); Read and import REFUSE any other model (a system or " +
			"non-custom model) so it can never be PATCHed or DELETEd here.\n\n" +
			"**Secret in state:** `api_key` is stored in Terraform state (it is never returned by the API). " +
			"Use an encrypted remote backend. Because the update endpoint re-probes with the stored key and " +
			"cannot rotate it, changing `api_key` forces replacement.\n\n" +
			"**Import:** import recovers only the model id. `api_key` is required and unreadable, so after " +
			"importing you must add it to config; the next apply then REPLACES (destroy + recreate) the " +
			"model rather than adopting it in place.\n\n" +
			"**Refreshed fields:** the cost fields (`input_cost`, `output_cost`, `cost_per_image`) and the " +
			"`supports_*` capability booleans are read back from the server, so out-of-band changes surface " +
			"as drift. The server only stores those it applies for the given `model_type`; a field it drops " +
			"keeps its configured value (no false drift). `max_tokens`, `temperature` and `has_reasoning` " +
			"are encoded into the server's parameter list and cannot be refreshed or cleared, so removing " +
			"one from config keeps the last value in state (retain-on-null).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Model ID (UUID) assigned by orq.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"display_name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Human-readable model name (1-128 characters).",
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 128)},
			},
			"model_id": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "The upstream model identifier passed to the provider endpoint " +
					"(e.g. `liquid/lfm2.5-1.2b`). Mutable in place (the update endpoint re-probes on change).",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"model_type": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Model modality: one of `chat`, `embedding`, `image`, `rerank`, `stt`, `tts`, " +
					"`moderation`, `realtime`, `completion`. (The server contract is an open string; this enum " +
					"reflects the currently known modalities.)",
				Validators: []validator.String{stringvalidator.OneOf(modelTypeValues...)},
			},
			"region": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Deployment region label (must be non-empty, e.g. `europe`).",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"base_url": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Base URL of the OpenAI-compatible endpoint (an absolute http(s) URL, e.g. `https://host/v1`).",
				Validators:          []validator.String{absoluteHTTPURLValidator{}},
			},
			"api_key": schema.StringAttribute{
				Required:  true,
				Sensitive: true,
				MarkdownDescription: "API key for the endpoint. Accepts a literal key or an `env://VAR` " +
					"reference (stored verbatim; server-side env resolution is out of scope here). Stored in " +
					"Terraform state — use an encrypted remote backend. The server never returns it, so it is " +
					"held config-authoritatively and NEVER refreshed from a read. The update endpoint cannot " +
					"rotate it, so changing this value (or setting it after an import) forces replacement.",
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"description": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Optional description. Round-trips as a top-level field; an omitted value reads back empty.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"input_cost": schema.Float64Attribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Optional input cost. Refreshed from the server; a value the server does not apply for this `model_type` keeps the configured value.",
			},
			"output_cost": schema.Float64Attribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Optional output cost. Refreshed from the server; a value the server does not apply for this `model_type` keeps the configured value.",
			},
			"cost_per_image": schema.Float64Attribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Optional per-image cost (image models). Refreshed from the server metadata; kept as configured when the server does not apply it.",
			},
			"max_tokens": schema.Int64Attribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Optional max tokens. Config-authoritative: encoded into the server parameter list, so it is neither refreshed nor clearable — removing it from config keeps the last value (retain-on-null).",
			},
			"temperature": schema.Float64Attribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Optional default temperature. Config-authoritative: encoded into the server parameter list, so it is neither refreshed nor clearable — removing it from config keeps the last value (retain-on-null).",
			},
			"has_reasoning": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Whether the model exposes reasoning. Config-authoritative: encoded into the server parameter list, so it is neither refreshed nor clearable — removing it from config keeps the last value (retain-on-null).",
			},
			"supports_vision": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Whether the model accepts image input. Refreshed from the server metadata.",
			},
			"supports_tool_calling": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Whether the model supports tool calling. Refreshed from the server metadata.",
			},
			"supports_strict_tool": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Whether the model supports strict tool schemas. Refreshed from the server metadata.",
			},
			"supports_image_edit": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Whether the model supports image editing (image models). Refreshed from the server metadata.",
			},
			"created": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Creation time (RFC 3339).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Last update time (RFC 3339).",
			},
		},
	}
}

func (r *modelResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	r.models = c.Models()
}

// apply refreshes the server-echoed fields onto the model. The secret api_key is
// never touched (the server never echoes it — `data` carries it from the plan on
// create/update or prior state on read). The cost + supports_* fields ARE
// refreshed (so drift is visible), keeping the configured value when the server
// omits one for this model_type; max_tokens/temperature/has_reasoning stay
// config-authoritative with retain-on-null.
func (r *modelResource) apply(m *client.Model, data *modelResourceModel) {
	data.ID = types.StringValue(m.ID)
	data.DisplayName = types.StringValue(m.DisplayName)
	data.ModelID = types.StringValue(m.ModelID)
	data.ModelType = types.StringValue(m.ModelType)
	// description round-trips as a top-level field; Optional+Computed absorbs an
	// omitted config (the server elides it, reading back as "").
	data.Description = types.StringValue(m.Description)
	// region and base_url live under the server's `configuration` sub-object and
	// are Required, so `data` already holds the configured value. Refresh when the
	// server echoes a value; fall back to the configured value if it omits one.
	if m.Region != "" {
		data.Region = types.StringValue(m.Region)
	}
	if m.BaseURL != "" {
		data.BaseURL = types.StringValue(m.BaseURL)
	}
	data.Created = types.StringValue(m.Created)
	data.Updated = types.StringValue(m.Updated)

	// Refreshed cost + capability fields: server value when present, else keep the
	// operator's plan/prior value (a create-time unknown collapses to null).
	data.InputCost = refreshFloat(data.InputCost, m.InputCost)
	data.OutputCost = refreshFloat(data.OutputCost, m.OutputCost)
	data.CostPerImage = refreshFloat(data.CostPerImage, m.CostPerImage)
	data.SupportsVision = refreshBool(data.SupportsVision, m.SupportsVision)
	data.SupportsToolCalling = refreshBool(data.SupportsToolCalling, m.SupportsToolCalling)
	data.SupportsStrictTool = refreshBool(data.SupportsStrictTool, m.SupportsStrictTool)
	data.SupportsImageEdit = refreshBool(data.SupportsImageEdit, m.SupportsImageEdit)

	// Config-authoritative, retain-on-null: collapse a create-time unknown to null,
	// otherwise keep the plan/prior value (the server merges on PATCH and cannot
	// clear these, so a null-in-state would be a false null).
	data.MaxTokens = int64OrNull(data.MaxTokens)
	data.Temperature = float64UnknownToNull(data.Temperature)
	data.HasReasoning = boolUnknownToNull(data.HasReasoning)
	// api_key is deliberately NOT written — the server never echoes it.
}

// refreshFloat takes the server value when present; otherwise keeps the current
// (plan/prior) value, collapsing an unknown to null so post-apply state is concrete.
func refreshFloat(cur types.Float64, srv *float64) types.Float64 {
	if srv != nil {
		return types.Float64Value(*srv)
	}
	return float64UnknownToNull(cur)
}

// refreshBool mirrors refreshFloat for the supports_* capability booleans.
func refreshBool(cur types.Bool, srv *bool) types.Bool {
	if srv != nil {
		return types.BoolValue(*srv)
	}
	return boolUnknownToNull(cur)
}

func float64UnknownToNull(v types.Float64) types.Float64 {
	if v.IsUnknown() {
		return types.Float64Null()
	}
	return v
}

func int64OrNull(v types.Int64) types.Int64 {
	if v.IsUnknown() {
		return types.Int64Null()
	}
	return v
}

func boolUnknownToNull(v types.Bool) types.Bool {
	if v.IsUnknown() {
		return types.BoolNull()
	}
	return v
}

func (r *modelResource) createInput(plan *modelResourceModel) client.ModelCreateInput {
	return client.ModelCreateInput{
		APIKey:              plan.APIKey.ValueString(),
		BaseURL:             plan.BaseURL.ValueString(),
		DisplayName:         plan.DisplayName.ValueString(),
		ModelID:             plan.ModelID.ValueString(),
		ModelType:           plan.ModelType.ValueString(),
		Region:              plan.Region.ValueString(),
		Description:         strPtr(plan.Description),
		InputCost:           float64Ptr(plan.InputCost),
		OutputCost:          float64Ptr(plan.OutputCost),
		CostPerImage:        float64Ptr(plan.CostPerImage),
		MaxTokens:           int64Ptr(plan.MaxTokens),
		Temperature:         float64Ptr(plan.Temperature),
		HasReasoning:        boolPtr(plan.HasReasoning),
		SupportsVision:      boolPtr(plan.SupportsVision),
		SupportsToolCalling: boolPtr(plan.SupportsToolCalling),
		SupportsStrictTool:  boolPtr(plan.SupportsStrictTool),
		SupportsImageEdit:   boolPtr(plan.SupportsImageEdit),
	}
}

func (r *modelResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan modelResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m, err := r.models.Create(ctx, r.createInput(&plan))
	if err != nil {
		resp.Diagnostics.AddError("Unable to create model", errDetail(err))
		return
	}
	// apply leaves api_key as configured in `plan` and refreshes the rest.
	r.apply(m, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *modelResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state modelResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m, err := r.models.Get(ctx, state.ID.ValueString())
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read model", errDetail(err))
		return
	}
	if summary, detail, ok := modelNotCustom(m); ok {
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	// apply preserves the config-authoritative api_key already in `state`.
	r.apply(m, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *modelResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan modelResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m, err := r.models.Update(ctx, client.ModelUpdateInput{
		ID:                  plan.ID.ValueString(),
		BaseURL:             plan.BaseURL.ValueString(),
		DisplayName:         plan.DisplayName.ValueString(),
		ModelID:             plan.ModelID.ValueString(),
		ModelType:           plan.ModelType.ValueString(),
		Region:              plan.Region.ValueString(),
		Description:         strPtr(plan.Description),
		InputCost:           float64Ptr(plan.InputCost),
		OutputCost:          float64Ptr(plan.OutputCost),
		CostPerImage:        float64Ptr(plan.CostPerImage),
		MaxTokens:           int64Ptr(plan.MaxTokens),
		Temperature:         float64Ptr(plan.Temperature),
		HasReasoning:        boolPtr(plan.HasReasoning),
		SupportsVision:      boolPtr(plan.SupportsVision),
		SupportsToolCalling: boolPtr(plan.SupportsToolCalling),
		SupportsStrictTool:  boolPtr(plan.SupportsStrictTool),
		SupportsImageEdit:   boolPtr(plan.SupportsImageEdit),
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to update model", errDetail(err))
		return
	}
	r.apply(m, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *modelResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state modelResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.models.Delete(ctx, state.ID.ValueString()); err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to delete model", errDetail(err))
	}
}

// ImportState verifies the id is a custom openai-like model (refusing a system /
// non-custom model, which would otherwise be PATCHed / DELETEd destructively),
// seeds the id, and warns that api_key must be set afterwards (which replaces).
func (r *modelResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// r.models is set by Configure, which the framework runs before import. Guard
	// defensively: if it is somehow unset, seed the id and let Read verify.
	if r.models != nil {
		m, err := r.models.Get(ctx, req.ID)
		if err != nil {
			if isNotFound(err) {
				resp.Diagnostics.AddError("Model not found",
					"No model with id "+req.ID+" exists in the workspace catalog.")
				return
			}
			resp.Diagnostics.AddError("Unable to read model for import", errDetail(err))
			return
		}
		if summary, detail, ok := modelNotCustom(m); ok {
			resp.Diagnostics.AddError(summary, detail)
			return
		}
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.AddWarning("api_key must be set in config after import",
		"orq_model.api_key is required and can never be read back from the server, so it is not populated "+
			"by import. Add api_key to config after importing; because the update endpoint cannot rotate the "+
			"key, the next apply will REPLACE (destroy + recreate) the model rather than adopting it in place.")
}

// modelNotCustom reports whether m is NOT a custom openai-like model managed by
// this resource (server `provider` != "openailike"), returning a diagnostic
// summary/detail for it. System models have owner "system"; other custom models
// carry a different provider — neither can be safely PATCHed/DELETEd here.
func modelNotCustom(m *client.Model) (summary, detail string, notCustom bool) {
	if m.Provider == client.ModelProviderOpenAILike {
		return "", "", false
	}
	owner := m.Owner
	if owner == "" {
		owner = "unknown"
	}
	provider := m.Provider
	if provider == "" {
		provider = "unknown"
	}
	return "Not a custom openai-like model",
		fmt.Sprintf("Model %q is not a custom OpenAI-compatible model managed by orq_model "+
			"(provider = %q, owner = %q). orq_model only manages models created via "+
			"POST /v2/models/openai-like (provider %q). Refusing to manage it, because doing so "+
			"would PATCH it via the openai-like endpoint and DELETE it — potentially destroying a "+
			"system or non-custom model.", m.ID, provider, owner, client.ModelProviderOpenAILike),
		true
}

// absoluteHTTPURLValidator rejects a base_url that is not an absolute http(s) URL.
type absoluteHTTPURLValidator struct{}

func (absoluteHTTPURLValidator) Description(context.Context) string {
	return "must be an absolute http(s) URL"
}

func (v absoluteHTTPURLValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (absoluteHTTPURLValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	u, err := url.Parse(req.ConfigValue.ValueString())
	if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid base URL",
			"base_url must be an absolute http(s) URL (e.g. https://host/v1)")
	}
}
