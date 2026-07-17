package provider

import (
	"context"
	"fmt"

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
			"catalog (POST /v2/models/openai-like). Only custom models are managed here; system models " +
			"(slug ids like `openai/gpt-4o`) are not.\n\n" +
			"The secret `api_key` is never returned by the API, so it is stored config-authoritatively and " +
			"changing it forces replacement (the update endpoint re-probes with the stored key and cannot " +
			"rotate it). The optional cost / capability metadata is likewise kept config-authoritative: the " +
			"server scatters, defaults and merges those fields, so the provider does not refresh them (an " +
			"out-of-band change to them is not detected as drift).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Model ID (UUID) assigned by orq.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"display_name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Human-readable model name.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"model_id": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "The upstream model identifier passed to the provider endpoint " +
					"(e.g. `liquid/lfm2.5-1.2b`). Mutable in place (the update endpoint re-probes on change).",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"model_type": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Model modality: one of `chat`, `embedding`, `image`, `rerank`, `stt`, `tts`, `moderation`, `realtime`, `completion`.",
				Validators:          []validator.String{stringvalidator.OneOf(modelTypeValues...)},
			},
			"region": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Deployment region label (must be non-empty, e.g. `europe`).",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"base_url": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Base URL of the OpenAI-compatible endpoint (e.g. `https://host/v1`).",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"api_key": schema.StringAttribute{
				Required:  true,
				Sensitive: true,
				MarkdownDescription: "API key for the endpoint. Accepts a literal key or an `env://VAR` " +
					"reference (stored verbatim; server-side env resolution is out of scope here). The server " +
					"never returns it, so it is held config-authoritatively and NEVER refreshed from a read. " +
					"The update endpoint cannot rotate it, so changing this value forces replacement.",
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
				MarkdownDescription: "Optional input cost. Config-authoritative (not refreshed from the server).",
			},
			"output_cost": schema.Float64Attribute{
				Optional:            true,
				MarkdownDescription: "Optional output cost. Config-authoritative (not refreshed from the server).",
			},
			"cost_per_image": schema.Float64Attribute{
				Optional:            true,
				MarkdownDescription: "Optional per-image cost. Config-authoritative (not refreshed from the server).",
			},
			"max_tokens": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Optional max tokens. Config-authoritative (not refreshed from the server).",
			},
			"temperature": schema.Float64Attribute{
				Optional:            true,
				MarkdownDescription: "Optional default temperature. Config-authoritative (not refreshed from the server).",
			},
			"has_reasoning": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "Whether the model exposes reasoning. Config-authoritative (not refreshed from the server).",
			},
			"supports_vision": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "Whether the model accepts image input. Config-authoritative (not refreshed from the server).",
			},
			"supports_tool_calling": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "Whether the model supports tool calling. Config-authoritative (not refreshed from the server).",
			},
			"supports_strict_tool": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "Whether the model supports strict tool schemas. Config-authoritative (not refreshed from the server).",
			},
			"supports_image_edit": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "Whether the model supports image editing. Config-authoritative (not refreshed from the server).",
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

// apply refreshes the reliably-echoed fields from a server projection while
// leaving the config-authoritative fields (api_key + the derived metadata
// optionals) untouched. `data` must already carry those from the plan (on
// create/update) or prior state (on read), so leaving them alone keeps state ==
// config and can never manufacture drift from a server default / transform.
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
	// server echoes a value; fall back to the configured value if it omits one (the
	// wire fields are optional) so a Required attribute never reads back empty.
	if m.Region != "" {
		data.Region = types.StringValue(m.Region)
	}
	if m.BaseURL != "" {
		data.BaseURL = types.StringValue(m.BaseURL)
	}
	data.Created = types.StringValue(m.Created)
	data.Updated = types.StringValue(m.Updated)
	// api_key and input_cost/output_cost/cost_per_image/max_tokens/temperature/
	// has_reasoning/supports_* are deliberately NOT written here — see the struct
	// doc on client.Model and the schema description.
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
	// apply leaves api_key + the metadata optionals as configured in `plan`.
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
	// apply preserves the config-authoritative fields already in `state` (the
	// server never echoes api_key, and the metadata optionals are not refreshed).
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

// ImportState seeds the resource id. api_key cannot be imported (the server
// never returns it); the operator must set it in config after import.
func (r *modelResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
