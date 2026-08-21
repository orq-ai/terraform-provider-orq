package provider

import (
	"context"
	"fmt"
	"net/url"

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
	_ resource.Resource                = &modelResource{}
	_ resource.ResourceWithConfigure   = &modelResource{}
	_ resource.ResourceWithImportState = &modelResource{}
)

// modelTypeValues are the accepted model_type discriminators for a custom
// openai-like model. The server's openai-like create AND update endpoints both
// validate `oneof=chat completion embedding image` (apps/platform-api/models/
// openai_like.go), so anything outside this set deterministically fails at apply
// — the validator rejects it up front instead.
var modelTypeValues = []string{
	"chat", "completion", "embedding", "image",
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
			"**Refreshed fields:** `input_cost` and `output_cost` are always serialized by the server (no " +
			"`omitempty`), so they are refreshed unconditionally and an out-of-band change — including to " +
			"`0` — surfaces as drift. `cost_per_image` and the `supports_*` capability booleans live under " +
			"the server's `metadata` with `omitempty`: a `false`/`0` value is DROPPED on the wire and is " +
			"indistinguishable from \"the server does not apply this for the given `model_type`\", so on " +
			"absence the prior value is retained (retain-on-null) — a consequence is that an out-of-band " +
			"flip of one of these to `false`/`0` is NOT visible as drift. `max_tokens`, `temperature` and " +
			"`has_reasoning` are encoded into the server's parameter list and cannot be refreshed or " +
			"cleared, so removing one from config keeps the last value in state (retain-on-null); the same " +
			"retain-on-null applies to `description`.",
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
				MarkdownDescription: "Model modality. One of `chat`, `completion`, `embedding`, `image` — the only " +
					"values the server's openai-like create and update endpoints accept; any other value is " +
					"rejected before apply.",
				Validators: []validator.String{stringvalidator.OneOf(modelTypeValues...)},
			},
			"region": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Deployment region label (must be non-empty, e.g. `europe`).",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"base_url": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Base URL of the OpenAI-compatible endpoint (an absolute http(s) URL, e.g. " +
					"`https://host/v1`). The server appends the endpoint path (e.g. `/chat/completions`) to it, " +
					"so it must NOT embed userinfo credentials (`https://user:pass@host`) or a query string — " +
					"both are rejected up front. base_url is not treated as a secret and is logged on a " +
					"server-side validation failure, so put all credentials in `api_key`, never in the URL.",
				Validators: []validator.String{absoluteHTTPURLValidator{}},
			},
			"api_key": schema.StringAttribute{
				Required:  true,
				Sensitive: true,
				MarkdownDescription: "API key for the endpoint. Sensitive: passed through to the server " +
					"verbatim, so source it from a Terraform variable or secret store rather than hard-coding " +
					"it. Stored in Terraform state — use an encrypted remote backend. The server never returns " +
					"it, so it is held config-authoritatively and NEVER refreshed from a read. The update " +
					"endpoint cannot rotate it, so changing this value (or setting it after an import) forces " +
					"replacement.",
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"description": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Optional description. Round-trips as a top-level field; an omitted value reads " +
					"back empty. Optional+Computed, and the update endpoint only overwrites it when a value is " +
					"sent, so REMOVING it from config keeps the last applied value in state (retain-on-null) " +
					"rather than clearing it — set it to `\"\"` to blank it.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"input_cost": schema.Float64Attribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Optional input cost. The server always serializes this (no `omitempty`), so it " +
					"is refreshed unconditionally on read and an out-of-band change — including to `0` — surfaces as " +
					"drift. Rejected for `model_type` `image` (which ignores per-token costs and would store `0`, " +
					"breaking plan consistency) — use `cost_per_image` instead.",
			},
			"output_cost": schema.Float64Attribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Optional output cost. The server always serializes this (no `omitempty`), so it " +
					"is refreshed unconditionally on read and an out-of-band change — including to `0` — surfaces as " +
					"drift. Rejected for `model_type` `image` (which ignores per-token costs) — use `cost_per_image`.",
			},
			"cost_per_image": schema.Float64Attribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Optional per-image cost (image models). Refreshed from the server `metadata`, " +
					"which omits a `0` value (`omitempty`) indistinguishably from \"not applicable for this " +
					"`model_type`\"; on absence the prior value is retained (retain-on-null), so an out-of-band " +
					"change TO `0` is NOT surfaced as drift.",
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
				Optional: true,
				Computed: true,
				MarkdownDescription: "Whether the model accepts image input. Refreshed from the server `metadata`, " +
					"which omits a `false` value (`omitempty`); on absence the prior value is retained " +
					"(retain-on-null), so an out-of-band flip to `false` is NOT surfaced as drift.",
			},
			"supports_tool_calling": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Whether the model supports tool calling. Refreshed from the server `metadata`, " +
					"which omits a `false` value (`omitempty`); on absence the prior value is retained " +
					"(retain-on-null), so an out-of-band flip to `false` is NOT surfaced as drift.",
			},
			"supports_strict_tool": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Whether the model supports strict tool schemas. Refreshed from the server " +
					"`metadata`, which omits a `false` value (`omitempty`); on absence the prior value is retained " +
					"(retain-on-null), so an out-of-band flip to `false` is NOT surfaced as drift.",
			},
			"supports_image_edit": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Whether the model supports image editing (image models). Refreshed from the " +
					"server `metadata`, which omits a `false` value (`omitempty`); on absence the prior value is " +
					"retained (retain-on-null), so an out-of-band flip to `false` is NOT surfaced as drift.",
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
// create/update or prior state on read). input_cost/output_cost are always on the
// wire (no omitempty): on READ (preservePlannedCosts=false) they refresh
// unconditionally so an out-of-band change — including to 0 — surfaces as drift; on
// CREATE/UPDATE (preservePlannedCosts=true) a KNOWN planned value is PRESERVED for
// plan consistency, since the server normalizes some costs (e.g. it stores 0 for an
// image model — openAILikeCosts) and overwriting a known plan value would raise
// "inconsistent result after apply". cost_per_image and the supports_* bools are
// metadata `omitempty` fields whose false/0 is dropped on the wire, so they retain
// the prior value on absence; max_tokens/temperature/has_reasoning stay
// config-authoritative with retain-on-null.
func (r *modelResource) apply(m *client.Model, data *modelResourceModel, preservePlannedCosts bool) {
	data.ID = types.StringValue(m.ID)
	data.DisplayName = types.StringValue(m.DisplayName)
	data.ModelID = types.StringValue(m.ModelID)
	data.ModelType = types.StringValue(m.ModelType)
	// description round-trips as a top-level field; Optional+Computed absorbs an
	// omitted config (the server elides it, reading back as "").
	data.Description = types.StringValue(m.Description)
	// region (from metadata.region) and base_url (from configuration.base_url) are
	// Required, so `data` already holds the configured value. Refresh when the
	// server echoes a value; fall back to the configured value if it omits one.
	if m.Region != "" {
		data.Region = types.StringValue(m.Region)
	}
	if m.BaseURL != "" {
		data.BaseURL = types.StringValue(m.BaseURL)
	}
	data.Created = types.StringValue(m.Created)
	data.Updated = types.StringValue(m.Updated)

	// input_cost/output_cost have no omitempty on the wire. On READ the server value —
	// including 0 — always wins (an out-of-band change to 0 surfaces as drift). On
	// CREATE/UPDATE a KNOWN planned value is preserved (only a null/unknown plan is
	// filled from the server), so the server's normalization of a cost it ignores for
	// the model_type (e.g. 0 for an image model) can never change a known planned value
	// out from under the plan.
	if preservePlannedCosts {
		data.InputCost = preservePlannedFloat(data.InputCost, m.InputCost)
		data.OutputCost = preservePlannedFloat(data.OutputCost, m.OutputCost)
	} else {
		data.InputCost = refreshFloatAuthoritative(m.InputCost)
		data.OutputCost = refreshFloatAuthoritative(m.OutputCost)
	}
	// cost_per_image is a metadata `omitempty` field: a 0 is dropped on the wire and
	// is indistinguishable from "not applied for this model_type", so keep the prior
	// value on absence (a create-time unknown collapses to null).
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

// refreshFloatAuthoritative reflects a server field that is ALWAYS present on the
// wire (no omitempty). The server value wins unconditionally — including 0 — so a
// non-zero → 0 out-of-band change surfaces. A nil pointer would mean the server
// sent an explicit null (not expected for a managed model); collapse it to null
// rather than retaining a stale prior value, so state stays honest to the server.
func refreshFloatAuthoritative(srv *float64) types.Float64 {
	if srv != nil {
		return types.Float64Value(*srv)
	}
	return types.Float64Null()
}

// preservePlannedFloat keeps a KNOWN planned value — the Create/Update contract
// requires post-apply state to equal the plan for every known value — and fills from
// the server only when the plan left the attribute null/unknown (an Optional+Computed
// attribute with no config value). Used on the create/update path for
// input_cost/output_cost, which the server may normalize (e.g. to 0 for an image
// model, whose per-token costs it ignores) in a way that would otherwise break plan
// consistency.
func preservePlannedFloat(planned types.Float64, srv *float64) types.Float64 {
	if !planned.IsNull() && !planned.IsUnknown() {
		return planned
	}
	if srv != nil {
		return types.Float64Value(*srv)
	}
	return types.Float64Null()
}

// refreshFloat takes the server value when present; otherwise keeps the current
// (plan/prior) value, collapsing an unknown to null so post-apply state is
// concrete. Used for metadata `omitempty` fields where absence cannot be told
// apart from a legitimate 0 (retain-on-null — see cost_per_image).
func refreshFloat(cur types.Float64, srv *float64) types.Float64 {
	if srv != nil {
		return types.Float64Value(*srv)
	}
	return float64UnknownToNull(cur)
}

// refreshBool mirrors refreshFloat for the supports_* capability booleans: they
// are metadata `omitempty` fields, so a false read-back arrives as nil and the
// prior value is retained (an out-of-band flip to false is not surfaced).
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

// validateImageModelCosts rejects a config that sets input_cost or output_cost on an
// image model. The server IGNORES per-token costs for model_type "image"
// (openAILikeCosts returns (0, cost_per_image)), so a set input_cost/output_cost is
// silently stored as 0 — and because those attributes are planned as KNOWN values,
// letting the server normalize them would break plan consistency ("inconsistent
// result after apply"). Point the operator at cost_per_image instead.
//
// Only a KNOWN, non-null cost is a violation; an omitted (Optional+Computed → unknown)
// cost is fine (the server fills it). model_type is Required, so it is always known
// here. Enforced at the top of Create/Update — there is no cross-field ValidateConfig
// for this resource.
func validateImageModelCosts(modelType types.String, inputCost, outputCost types.Float64) diag.Diagnostics {
	var diags diag.Diagnostics
	if modelType.IsNull() || modelType.IsUnknown() || modelType.ValueString() != "image" {
		return diags
	}
	if !inputCost.IsNull() && !inputCost.IsUnknown() {
		diags.AddAttributeError(path.Root("input_cost"), "input_cost is not supported for image models",
			"The server ignores per-token costs for model_type \"image\" and stores 0, which would break "+
				"plan consistency. Remove input_cost and set cost_per_image instead.")
	}
	if !outputCost.IsNull() && !outputCost.IsUnknown() {
		diags.AddAttributeError(path.Root("output_cost"), "output_cost is not supported for image models",
			"The server ignores per-token costs for model_type \"image\" and stores 0, which would break "+
				"plan consistency. Remove output_cost and set cost_per_image instead.")
	}
	return diags
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
	resp.Diagnostics.Append(revalidatePlan(ctx, req.Plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Reject input_cost/output_cost on an image model before any server call: the
	// server ignores per-token costs for that model_type and would normalize them to
	// 0, breaking plan consistency (model_type is known here — there is no cross-field
	// ValidateConfig for this resource).
	resp.Diagnostics.Append(validateImageModelCosts(plan.ModelType, plan.InputCost, plan.OutputCost)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m, err := r.models.Create(ctx, r.createInput(&plan))
	if err != nil {
		resp.Diagnostics.AddError("Unable to create model", errDetail(err))
		return
	}
	// apply leaves api_key as configured in `plan`, preserves the known planned costs,
	// and refreshes the rest.
	r.apply(m, &plan, true)
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
	// apply preserves the config-authoritative api_key already in `state` and
	// refreshes input_cost/output_cost unconditionally (drift belongs on read).
	r.apply(m, &state, false)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *modelResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan modelResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(revalidatePlan(ctx, req.Plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Reject input_cost/output_cost on an image model before any server call (same
	// guardrail as Create — the server ignores per-token costs for that model_type).
	resp.Diagnostics.Append(validateImageModelCosts(plan.ModelType, plan.InputCost, plan.OutputCost)...)
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
	// Preserve the known planned costs (same rationale as Create).
	r.apply(m, &plan, true)
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

// absoluteHTTPURLValidator rejects a base_url that is not an absolute http(s)
// URL, or that embeds credentials (userinfo or a query string). base_url is NOT
// Sensitive and the platform logs the full URL on a validation failure, so any
// credential smuggled into it would leak; rejecting these up front (client-side,
// at `terraform validate`) keeps the credential-bearing URL off the wire and out
// of the server logs entirely.
type absoluteHTTPURLValidator struct{}

func (absoluteHTTPURLValidator) Description(context.Context) string {
	return "must be an absolute http(s) URL with no embedded credentials (no userinfo, no query string)"
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
		return
	}
	// Reject embedded userinfo credentials (https://user:pass@host). base_url is
	// not a secret and the platform logs it on a validation failure, so a password
	// in the authority would leak. The diagnostic deliberately does not echo the
	// URL. Put the secret in api_key.
	if u.User != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Credentials in base URL",
			"base_url must not embed userinfo credentials (https://user:pass@host); "+
				"pass the secret via api_key instead.")
		return
	}
	// Reject ANY query string. The server only appends path segments to base_url
	// (joinURL → /chat/completions, /embeddings, …) and never consumes a query, so
	// one is at best inert and at worst a credential smuggled into the logs
	// (e.g. ?api-key=…). Rule: no query at all — put auth in api_key.
	if u.RawQuery != "" || u.ForceQuery {
		resp.Diagnostics.AddAttributeError(req.Path, "Query string in base URL",
			"base_url must not contain a query string; the server appends the endpoint "+
				"path to it. Put credentials in api_key, not the URL.")
		return
	}
}
