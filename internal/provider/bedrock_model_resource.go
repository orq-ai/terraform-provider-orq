package provider

import (
	"context"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/float64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

var (
	_ resource.Resource                   = &bedrockModelResource{}
	_ resource.ResourceWithConfigure      = &bedrockModelResource{}
	_ resource.ResourceWithImportState    = &bedrockModelResource{}
	_ resource.ResourceWithValidateConfig = &bedrockModelResource{}
)

// bedrockInferenceProfileARNPattern is copied verbatim from the server
// (apps/platform-api/models/aws_bedrock.go), which rejects anything else on both
// create and update. A plain foundation-model id is NOT accepted: the endpoint
// registers an inference profile.
var bedrockInferenceProfileARNPattern = regexp.MustCompile(
	`^arn:aws:bedrock:[a-z0-9-]+:\d+:(application-inference-profile|inference-profile)\/.+$`)

// bedrockModelTypeValues are the model_type discriminators the server's Bedrock
// create endpoint validates (`oneof=chat embedding`).
var bedrockModelTypeValues = []string{"chat", "embedding"}

var bedrockAuthModeValues = []string{
	client.BedrockAuthModeIntegration,
	client.BedrockAuthModePodIdentity,
}

// NewBedrockModelResource is the factory registered on the provider.
func NewBedrockModelResource() resource.Resource { return &bedrockModelResource{} }

type bedrockModelResource struct {
	models client.BedrockModelsAPI
}

type bedrockModelResourceModel struct {
	ID             types.String `tfsdk:"id"`
	DisplayName    types.String `tfsdk:"display_name"`
	ModelID        types.String `tfsdk:"model_id"`
	Region         types.String `tfsdk:"region"`
	ModelType      types.String `tfsdk:"model_type"`
	ModelDeveloper types.String `tfsdk:"model_developer"`
	ModelFamily    types.String `tfsdk:"model_family"`
	Description    types.String `tfsdk:"description"`

	AuthMode             types.String `tfsdk:"auth_mode"`
	IntegrationID        types.String `tfsdk:"integration_id"`
	AssumeRoleArn        types.String `tfsdk:"assume_role_arn"`
	AssumeRoleExternalID types.String `tfsdk:"assume_role_external_id"`

	MaxTokens   types.Int64   `tfsdk:"max_tokens"`
	Temperature types.Float64 `tfsdk:"temperature"`
	InputCost   types.Float64 `tfsdk:"input_cost"`
	OutputCost  types.Float64 `tfsdk:"output_cost"`

	SupportsToolCalling       types.Bool `tfsdk:"supports_tool_calling"`
	SupportsVision            types.Bool `tfsdk:"supports_vision"`
	SupportsStrictTool        types.Bool `tfsdk:"supports_strict_tool"`
	SupportsJSONMode          types.Bool `tfsdk:"supports_json_mode"`
	SupportsJSONSchema        types.Bool `tfsdk:"supports_json_schema"`
	HasReasoning              types.Bool `tfsdk:"has_reasoning"`
	SupportsAdaptiveReasoning types.Bool `tfsdk:"supports_adaptive_reasoning"`
	SupportsExtendedThinking  types.Bool `tfsdk:"supports_extended_thinking"`

	Created types.String `tfsdk:"created"`
	Updated types.String `tfsdk:"updated"`
}

func (r *bedrockModelResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_bedrock_model"
}

func (r *bedrockModelResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An AWS Bedrock inference profile registered as a custom model in the workspace " +
			"catalog (POST /v2/models/aws-bedrock). Read and import REFUSE any model that is not a Bedrock " +
			"one (server `provider` = `aws` plus a Bedrock `auth_mode` and inference-profile ARN), so a " +
			"system or other-provider model can never be PATCHed or DELETEd here.\n\n" +
			"**No credentials are stored on the model.** They are resolved at request time from either a " +
			"dashboard-created AWS integration (`auth_mode = \"integration\"`) or the pod's IAM identity " +
			"(`auth_mode = \"pod-identity\"`). `auth_mode`, `integration_id` and `model_type` are not " +
			"accepted by the update endpoint, so changing any of them forces replacement.\n\n" +
			"**Write-only assume-role fields:** the server strips `assume_role_arn` and " +
			"`assume_role_external_id` from every response, so they can never be refreshed and " +
			"out-of-band changes to them are invisible. They are also honored ONLY in `pod-identity` mode. " +
			"The update endpoint treats an empty value as \"not sent\" and cannot clear one, so REMOVING a " +
			"previously set assume-role value forces replacement rather than silently leaving the role " +
			"attached.\n\n" +
			"**Refreshed fields:** `input_cost` and `output_cost` are always serialized by the server, so " +
			"they are refreshed unconditionally and an out-of-band change — including to `0` — surfaces as " +
			"drift. The `supports_*` / `has_reasoning` capability booleans live under the server's " +
			"`metadata` with `omitempty`: a `false` is DROPPED on the wire, so on absence the prior value " +
			"is retained (retain-on-null) and an out-of-band flip to `false` is NOT visible as drift. " +
			"`supports_tool_calling` is the exception — the server also mirrors it into the always-present " +
			"top-level `has_functions`, so it refreshes in both directions. `max_tokens` and `temperature` " +
			"are encoded into the server's parameter list and cannot be refreshed or cleared, so removing " +
			"one from config keeps the last value in state (retain-on-null); the same retain-on-null " +
			"applies to `description` and `model_family`.",
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
				MarkdownDescription: "The Bedrock inference profile ARN, e.g. " +
					"`arn:aws:bedrock:eu-central-1:123456789012:application-inference-profile/abc123`. A bare " +
					"foundation-model id is rejected — the endpoint registers an inference profile. Mutable " +
					"in place (the update endpoint re-validates the ARN).",
				Validators: []validator.String{bedrockARNValidator{}},
			},
			"region": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "AWS region the inference profile lives in (e.g. `eu-central-1`). Mutable in place.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"model_type": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Model modality: `chat` (server default) or `embedding`. Fixed at creation — " +
					"the update endpoint does not accept it, so changing it forces replacement.",
				Validators: []validator.String{stringvalidator.OneOf(bedrockModelTypeValues...)},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"model_developer": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Developer of the underlying model (e.g. `anthropic`). Mutable in place.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"model_family": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Model family (e.g. `claude`). Defaults to `unknown` server-side. The update " +
					"endpoint ignores an empty value, so removing it from config keeps the last applied value " +
					"in state (retain-on-null) rather than resetting it.",
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"description": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Optional description. Round-trips as a top-level field; an omitted value reads " +
					"back empty. The update endpoint only overwrites it when a value is sent, so REMOVING it from " +
					"config keeps the last applied value in state (retain-on-null) — set it to `\"\"` to blank it.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"auth_mode": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "How Bedrock credentials are resolved at request time: `integration` (use a " +
					"dashboard-created AWS integration, named by `integration_id`) or `pod-identity` (use the " +
					"platform pod's own IAM identity). The update endpoint does not accept it, so changing it " +
					"forces replacement.",
				Validators:    []validator.String{stringvalidator.OneOf(bedrockAuthModeValues...)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"integration_id": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Document id of the AWS integration holding the credentials. REQUIRED when " +
					"`auth_mode = \"integration\"` and rejected otherwise (a `pod-identity` model never reads " +
					"it). The update endpoint does not accept it, so changing it forces replacement.",
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"assume_role_arn": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "ARN of a role to assume via STS before calling Bedrock (cross-account " +
					"access). Honored ONLY when `auth_mode = \"pod-identity\"`; setting it alongside " +
					"`integration` is rejected because the server would store but never use it. Write-only: " +
					"the server strips it from every response, so it is held config-authoritatively and never " +
					"refreshed. The update endpoint cannot clear it, so REMOVING it from config forces " +
					"replacement.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
				PlanModifiers: []planmodifier.String{
					requiresReplaceOnClear("assume_role_arn"),
				},
			},
			"assume_role_external_id": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				MarkdownDescription: "External ID passed to STS AssumeRole (confused-deputy protection). Requires " +
					"`assume_role_arn`, and like it is honored only in `pod-identity` mode. Sensitive and " +
					"write-only: the server strips it from every response, so it is never refreshed. The " +
					"update endpoint cannot clear it, so REMOVING it from config forces replacement.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
				PlanModifiers: []planmodifier.String{
					requiresReplaceOnClear("assume_role_external_id"),
				},
			},
			"max_tokens": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Optional max tokens (1-128000). Config-authoritative: encoded into the server " +
					"parameter list, so it is neither refreshed nor clearable — removing it from config keeps " +
					"the last value (retain-on-null).",
				Validators: []validator.Int64{int64validator.Between(1, 128000)},
			},
			"temperature": schema.Float64Attribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Optional default temperature (0-2). Config-authoritative: encoded into the " +
					"server parameter list, so it is neither refreshed nor clearable — removing it from config " +
					"keeps the last value (retain-on-null).",
				Validators: []validator.Float64{float64validator.Between(0, 2)},
			},
			"input_cost": schema.Float64Attribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Optional input cost in USD per 1K tokens (the server also derives its " +
					"per-million metadata from it). Always serialized by the server, so it is refreshed " +
					"unconditionally on read and an out-of-band change — including to `0` — surfaces as drift.",
			},
			"output_cost": schema.Float64Attribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Optional output cost in USD per 1K tokens. Always serialized by the server, " +
					"so it is refreshed unconditionally on read and an out-of-band change — including to `0` — " +
					"surfaces as drift.",
			},
			"supports_tool_calling": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Whether the model supports tool calling. The server mirrors it into the " +
					"always-present top-level `has_functions`, so unlike the other capability booleans it " +
					"refreshes in both directions and a flip to `false` surfaces as drift.",
			},
			"supports_vision": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Whether the model accepts image input. Refreshed from the server `metadata`, " +
					"which omits a `false` value; on absence the prior value is retained (retain-on-null), so " +
					"an out-of-band flip to `false` is NOT surfaced as drift.",
			},
			"supports_strict_tool": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Whether the model supports strict tool schemas. Retain-on-null (the server " +
					"omits a `false` value).",
			},
			"supports_json_mode": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Whether the model supports the JSON-mode response format. Retain-on-null " +
					"(the server omits a `false` value).",
			},
			"supports_json_schema": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Whether the model supports the JSON-schema response format. Retain-on-null " +
					"(the server omits a `false` value).",
			},
			"has_reasoning": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Whether the model exposes reasoning. Also drives the server's " +
					"`reasoning_effort` parameter. Retain-on-null (the server omits a `false` value).",
			},
			"supports_adaptive_reasoning": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Whether the model uses adaptive reasoning. The reasoning mechanism is " +
					"model-version specific — set this OR `supports_extended_thinking`, matching the model. " +
					"Retain-on-null (the server omits a `false` value).",
			},
			"supports_extended_thinking": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Whether the model uses extended thinking. The reasoning mechanism is " +
					"model-version specific — set this OR `supports_adaptive_reasoning`, matching the model. " +
					"Retain-on-null (the server omits a `false` value).",
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

func (r *bedrockModelResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	r.models = c.BedrockModels()
}

// apply refreshes the server-echoed fields onto the model. assume_role_arn /
// assume_role_external_id are never touched: the server strips both from every
// response, so `data` keeps the plan (create/update) or prior state (read) value.
// input_cost/output_cost are always on the wire: on READ
// (preservePlannedCosts=false) they refresh unconditionally so an out-of-band
// change surfaces as drift; on CREATE/UPDATE a KNOWN planned value is PRESERVED
// for plan consistency. The metadata capability booleans are `omitempty`, so a
// false is dropped on the wire and the prior value is retained on absence;
// max_tokens/temperature stay config-authoritative with retain-on-null.
func (r *bedrockModelResource) apply(m *client.BedrockModel, data *bedrockModelResourceModel, preservePlannedCosts bool) {
	data.ID = types.StringValue(m.ID)
	data.DisplayName = types.StringValue(m.DisplayName)
	data.ModelID = types.StringValue(m.ModelID)
	data.ModelType = types.StringValue(m.ModelType)
	data.Description = types.StringValue(m.Description)
	data.Created = types.StringValue(m.Created)
	data.Updated = types.StringValue(m.Updated)

	// region / model_developer / model_family / auth_mode are optional on the
	// wire: refresh when the server echoes a value, otherwise keep the configured
	// one (collapsing a create-time unknown to null).
	data.Region = refreshString(data.Region, m.Region)
	data.ModelDeveloper = refreshString(data.ModelDeveloper, m.ModelDeveloper)
	data.ModelFamily = refreshString(data.ModelFamily, m.ModelFamily)
	data.AuthMode = refreshString(data.AuthMode, m.AuthMode)
	// integration_id is Optional-only and authoritatively echoed for an
	// integration model (and absent for a pod-identity one), so it refreshes
	// straight from the server — a change surfaces as drift and forces replace.
	data.IntegrationID = optString(m.IntegrationID)

	if preservePlannedCosts {
		data.InputCost = preservePlannedFloat(data.InputCost, m.InputCost)
		data.OutputCost = preservePlannedFloat(data.OutputCost, m.OutputCost)
	} else {
		data.InputCost = refreshFloatAuthoritative(m.InputCost)
		data.OutputCost = refreshFloatAuthoritative(m.OutputCost)
	}

	// supports_tool_calling is mirrored into the always-present top-level
	// has_functions, so a metadata absence still yields the real value.
	if m.SupportsToolCalling != nil {
		data.SupportsToolCalling = types.BoolValue(*m.SupportsToolCalling)
	} else {
		data.SupportsToolCalling = types.BoolValue(m.HasFunctions)
	}
	data.SupportsVision = refreshBool(data.SupportsVision, m.SupportsVision)
	data.SupportsStrictTool = refreshBool(data.SupportsStrictTool, m.SupportsStrictTool)
	data.SupportsJSONMode = refreshBool(data.SupportsJSONMode, m.SupportsJSONMode)
	data.SupportsJSONSchema = refreshBool(data.SupportsJSONSchema, m.SupportsJSONSchema)
	data.HasReasoning = refreshBool(data.HasReasoning, m.HasReasoning)
	data.SupportsAdaptiveReasoning = refreshBool(data.SupportsAdaptiveReasoning, m.SupportsAdaptiveReasoning)
	data.SupportsExtendedThinking = refreshBool(data.SupportsExtendedThinking, m.SupportsExtendedThinking)

	data.MaxTokens = int64OrNull(data.MaxTokens)
	data.Temperature = float64UnknownToNull(data.Temperature)
}

// refreshString takes the server value when present; otherwise keeps the current
// (plan/prior) value, collapsing an unknown to null so post-apply state is
// concrete.
func refreshString(cur types.String, srv string) types.String {
	if srv != "" {
		return types.StringValue(srv)
	}
	if cur.IsUnknown() {
		return types.StringNull()
	}
	return cur
}

func (r *bedrockModelResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan bedrockModelResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Re-enforce the auth-mode invariants at apply: the framework runs
	// ValidateConfig only at validate/plan time, and a value that was unknown
	// there is known now.
	resp.Diagnostics.Append(validateBedrockAuthMode(bedrockAuthConfig{
		AuthMode:             plan.AuthMode,
		IntegrationID:        plan.IntegrationID,
		AssumeRoleArn:        plan.AssumeRoleArn,
		AssumeRoleExternalID: plan.AssumeRoleExternalID,
	})...)
	if resp.Diagnostics.HasError() {
		return
	}
	m, err := r.models.Create(ctx, client.BedrockModelCreateInput{
		DisplayName:    plan.DisplayName.ValueString(),
		ModelID:        plan.ModelID.ValueString(),
		Region:         plan.Region.ValueString(),
		ModelDeveloper: plan.ModelDeveloper.ValueString(),
		AuthMode:       plan.AuthMode.ValueString(),

		ModelType:     strPtr(plan.ModelType),
		ModelFamily:   strPtr(plan.ModelFamily),
		Description:   strPtr(plan.Description),
		IntegrationID: strPtr(plan.IntegrationID),

		AssumeRoleArn:        strPtr(plan.AssumeRoleArn),
		AssumeRoleExternalID: strPtr(plan.AssumeRoleExternalID),

		MaxTokens:   int64Ptr(plan.MaxTokens),
		Temperature: float64Ptr(plan.Temperature),
		InputCost:   float64Ptr(plan.InputCost),
		OutputCost:  float64Ptr(plan.OutputCost),

		SupportsToolCalling:       boolPtr(plan.SupportsToolCalling),
		SupportsVision:            boolPtr(plan.SupportsVision),
		SupportsStrictTool:        boolPtr(plan.SupportsStrictTool),
		SupportsJSONMode:          boolPtr(plan.SupportsJSONMode),
		SupportsJSONSchema:        boolPtr(plan.SupportsJSONSchema),
		HasReasoning:              boolPtr(plan.HasReasoning),
		SupportsAdaptiveReasoning: boolPtr(plan.SupportsAdaptiveReasoning),
		SupportsExtendedThinking:  boolPtr(plan.SupportsExtendedThinking),
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to create Bedrock model", errDetail(err))
		return
	}
	r.apply(m, &plan, true)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *bedrockModelResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state bedrockModelResourceModel
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
		resp.Diagnostics.AddError("Unable to read Bedrock model", errDetail(err))
		return
	}
	if summary, detail, ok := bedrockModelNotManaged(m); ok {
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	r.apply(m, &state, false)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *bedrockModelResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan bedrockModelResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(validateBedrockAuthMode(bedrockAuthConfig{
		AuthMode:             plan.AuthMode,
		IntegrationID:        plan.IntegrationID,
		AssumeRoleArn:        plan.AssumeRoleArn,
		AssumeRoleExternalID: plan.AssumeRoleExternalID,
	})...)
	if resp.Diagnostics.HasError() {
		return
	}
	m, err := r.models.Update(ctx, client.BedrockModelUpdateInput{
		ID:             plan.ID.ValueString(),
		DisplayName:    plan.DisplayName.ValueString(),
		ModelID:        plan.ModelID.ValueString(),
		Region:         plan.Region.ValueString(),
		ModelDeveloper: plan.ModelDeveloper.ValueString(),

		ModelFamily: strPtr(plan.ModelFamily),
		Description: strPtr(plan.Description),

		AssumeRoleArn:        strPtr(plan.AssumeRoleArn),
		AssumeRoleExternalID: strPtr(plan.AssumeRoleExternalID),

		MaxTokens:   int64Ptr(plan.MaxTokens),
		Temperature: float64Ptr(plan.Temperature),
		InputCost:   float64Ptr(plan.InputCost),
		OutputCost:  float64Ptr(plan.OutputCost),

		SupportsToolCalling:       boolPtr(plan.SupportsToolCalling),
		SupportsVision:            boolPtr(plan.SupportsVision),
		SupportsStrictTool:        boolPtr(plan.SupportsStrictTool),
		SupportsJSONMode:          boolPtr(plan.SupportsJSONMode),
		SupportsJSONSchema:        boolPtr(plan.SupportsJSONSchema),
		HasReasoning:              boolPtr(plan.HasReasoning),
		SupportsAdaptiveReasoning: boolPtr(plan.SupportsAdaptiveReasoning),
		SupportsExtendedThinking:  boolPtr(plan.SupportsExtendedThinking),
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to update Bedrock model", errDetail(err))
		return
	}
	r.apply(m, &plan, true)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *bedrockModelResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state bedrockModelResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.models.Delete(ctx, state.ID.ValueString()); err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to delete Bedrock model", errDetail(err))
	}
}

// ImportState verifies the id is a Bedrock model (refusing a system or
// other-provider model, which would otherwise be PATCHed / DELETEd
// destructively), seeds the id, and warns that the write-only assume-role fields
// cannot be recovered.
func (r *bedrockModelResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// r.models is set by Configure, which the framework runs before import. Guard
	// defensively: if it is somehow unset, seed the id and let Read verify.
	if r.models != nil {
		m, err := r.models.Get(ctx, req.ID)
		if err != nil {
			if isNotFound(err) {
				resp.Diagnostics.AddError("Bedrock model not found",
					"No model with id "+req.ID+" exists in the workspace catalog.")
				return
			}
			resp.Diagnostics.AddError("Unable to read Bedrock model for import", errDetail(err))
			return
		}
		if summary, detail, ok := bedrockModelNotManaged(m); ok {
			resp.Diagnostics.AddError(summary, detail)
			return
		}
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.AddWarning("assume-role settings are not recovered by import",
		"The server strips assume_role_arn and assume_role_external_id from every response, so import "+
			"cannot populate them. If the imported model uses an assume-role, add both to config to match "+
			"it; adding a value that the model does not actually have will be applied as an update, and "+
			"omitting one it does have leaves it attached server-side.")
}

// bedrockModelNotManaged reports whether m is NOT a Bedrock model managed by this
// resource, returning a diagnostic summary/detail for it. `provider` = "aws" is
// shared with legacy access-key AWS models, so the inference-profile ARN and
// Bedrock auth_mode in the configuration are what actually identify one.
func bedrockModelNotManaged(m *client.BedrockModel) (summary, detail string, notManaged bool) {
	if m.IsBedrock() {
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
	return "Not an AWS Bedrock custom model",
		fmt.Sprintf("Model %q is not an AWS Bedrock model managed by orq_bedrock_model "+
			"(provider = %q, owner = %q, auth_mode = %q). orq_bedrock_model only manages models created "+
			"via POST /v2/models/aws-bedrock — provider %q with an inference-profile ARN and a %q or %q "+
			"auth_mode. Refusing to manage it, because doing so would PATCH it via the Bedrock endpoint "+
			"and DELETE it — potentially destroying a system or other-provider model.",
			m.ID, provider, owner, m.AuthMode, client.ModelProviderAWS,
			client.BedrockAuthModeIntegration, client.BedrockAuthModePodIdentity),
		true
}

// ---------------------------------------------------------------------------
// validation
// ---------------------------------------------------------------------------

// bedrockARNValidator rejects a model_id that is not a Bedrock inference-profile
// ARN. The server applies the same regex on create AND update, so anything else
// deterministically fails at apply.
type bedrockARNValidator struct{}

func (bedrockARNValidator) Description(context.Context) string {
	return "must be a Bedrock inference-profile ARN " +
		"(arn:aws:bedrock:<region>:<account-id>:application-inference-profile/<profile-id>)"
}

func (v bedrockARNValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v bedrockARNValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if !bedrockInferenceProfileARNPattern.MatchString(req.ConfigValue.ValueString()) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid inference profile ARN",
			"model_id "+v.Description(ctx)+". A bare foundation-model id (e.g. "+
				"anthropic.claude-sonnet-4-20250514-v1:0) is not accepted — create an inference profile "+
				"in AWS and use its ARN.")
	}
}

// requiresReplaceOnClear forces replacement when a previously set value is
// removed from config. The Bedrock update endpoint treats an empty value as "not
// sent" and merges rather than clears, so an in-place update could not honor the
// removal and would silently leave the old value attached server-side.
func requiresReplaceOnClear(attribute string) planmodifier.String {
	description := "removing " + attribute + " requires replacement (the update endpoint cannot clear it)"
	return stringplanmodifier.RequiresReplaceIf(
		func(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
			resp.RequiresReplace = !req.StateValue.IsNull() && req.PlanValue.IsNull()
		},
		description,
		description,
	)
}

// bedrockAuthConfig is the subset of the config the cross-field auth rules
// inspect. It is read attribute-by-attribute rather than through
// bedrockModelResourceModel because a value may still be UNKNOWN at validate time.
type bedrockAuthConfig struct {
	AuthMode             types.String
	IntegrationID        types.String
	AssumeRoleArn        types.String
	AssumeRoleExternalID types.String
}

func (r *bedrockModelResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	cfg, diags := readBedrockAuthConfig(ctx, req.Config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(validateBedrockAuthMode(cfg)...)
}

func readBedrockAuthConfig(ctx context.Context, cfg tfsdk.Config) (bedrockAuthConfig, diag.Diagnostics) {
	var c bedrockAuthConfig
	var diags diag.Diagnostics
	diags.Append(cfg.GetAttribute(ctx, path.Root("auth_mode"), &c.AuthMode)...)
	diags.Append(cfg.GetAttribute(ctx, path.Root("integration_id"), &c.IntegrationID)...)
	diags.Append(cfg.GetAttribute(ctx, path.Root("assume_role_arn"), &c.AssumeRoleArn)...)
	diags.Append(cfg.GetAttribute(ctx, path.Root("assume_role_external_id"), &c.AssumeRoleExternalID)...)
	return c, diags
}

// validateBedrockAuthMode enforces the credential-resolution invariants:
// integration_id is required by, and exclusive to, auth_mode "integration"; the
// assume-role pair is honored only in "pod-identity" mode (libs/go/providers/
// aws.go skips it otherwise, so an integration-mode value is dead config); and an
// external id without a role ARN is never used.
func validateBedrockAuthMode(c bedrockAuthConfig) diag.Diagnostics {
	var diags diag.Diagnostics
	if c.AuthMode.IsUnknown() {
		return diags
	}
	hasIntegrationID := isSetString(c.IntegrationID)
	hasRoleArn := isSetString(c.AssumeRoleArn)
	hasExternalID := isSetString(c.AssumeRoleExternalID)

	switch c.AuthMode.ValueString() {
	case client.BedrockAuthModeIntegration:
		if !hasIntegrationID && !c.IntegrationID.IsUnknown() {
			diags.AddAttributeError(path.Root("integration_id"), "integration_id is required for auth_mode \"integration\"",
				"auth_mode = \"integration\" resolves Bedrock credentials from a dashboard-created AWS "+
					"integration, so integration_id must name one. Set integration_id, or switch to "+
					"auth_mode = \"pod-identity\".")
		}
		if hasRoleArn || hasExternalID {
			diags.AddAttributeError(path.Root("assume_role_arn"), "assume-role is not used with auth_mode \"integration\"",
				"The server only assumes a role in pod-identity mode; in integration mode the assume-role "+
					"settings are stored but never used, which silently misrepresents how the model "+
					"authenticates. Remove assume_role_arn / assume_role_external_id, or switch to "+
					"auth_mode = \"pod-identity\".")
		}
	case client.BedrockAuthModePodIdentity:
		if hasIntegrationID {
			diags.AddAttributeError(path.Root("integration_id"), "integration_id is not used with auth_mode \"pod-identity\"",
				"auth_mode = \"pod-identity\" resolves credentials from the platform pod's own IAM "+
					"identity and never reads integration_id. Remove it, or switch to "+
					"auth_mode = \"integration\".")
		}
	}

	if hasExternalID && !hasRoleArn && !c.AssumeRoleArn.IsUnknown() {
		diags.AddAttributeError(path.Root("assume_role_external_id"), "assume_role_external_id requires assume_role_arn",
			"The external id is only passed to STS AssumeRole; without assume_role_arn there is no role "+
				"to assume and the value is never used.")
	}
	return diags
}

// isSetString reports whether an attribute carries a usable configured value —
// an unknown is deferred to the apply-time recheck, not treated as set.
func isSetString(v types.String) bool {
	return !v.IsNull() && !v.IsUnknown() && v.ValueString() != ""
}
