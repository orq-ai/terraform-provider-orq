package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

var (
	_ resource.Resource                   = &modelFusionResource{}
	_ resource.ResourceWithConfigure      = &modelFusionResource{}
	_ resource.ResourceWithImportState    = &modelFusionResource{}
	_ resource.ResourceWithModifyPlan     = &modelFusionResource{}
	_ resource.ResourceWithValidateConfig = &modelFusionResource{}
)

func NewModelFusionResource() resource.Resource { return &modelFusionResource{} }

type modelFusionResource struct {
	fusions client.ModelFusionsAPI
}

type modelFusionResourceModel struct {
	ID                types.String `tfsdk:"id"`
	Key               types.String `tfsdk:"key"`
	ModelRef          types.String `tfsdk:"model_ref"`
	PanelModels       types.List   `tfsdk:"panel_models"`
	JudgeModel        types.String `tfsdk:"judge_model"`
	SynthesisMode     types.String `tfsdk:"synthesis_mode"`
	Preset            types.String `tfsdk:"preset"`
	OrchestratorModel types.String `tfsdk:"orchestrator_model"`
	WebTools          types.Bool   `tfsdk:"web_tools"`
	MaxToolCalls      types.Int64  `tfsdk:"max_tool_calls"`
	FeedRawToSynth    types.Bool   `tfsdk:"feed_raw_to_synth"`
	Enabled           types.Bool   `tfsdk:"enabled"`
	CreatedAt         types.String `tfsdk:"created_at"`
	UpdatedAt         types.String `tfsdk:"updated_at"`
}

func (r *modelFusionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_model_fusion"
}

func (r *modelFusionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A workspace Model Fusion: a virtual gateway model that combines independent " +
			"answers from a panel of models through a judge and an optional synthesizer.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Model Fusion ID assigned by orq.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"key": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Key that identifies the Model Fusion within the workspace and forms its " +
					"model reference. Lowercase letters, numbers, and hyphens. Changing this forces replacement.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    gatewayModelKeyValidators(),
			},
			"model_ref": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Workspace-qualified model reference to use in AI Gateway requests.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"panel_models": schema.ListAttribute{
				Required:    true,
				ElementType: types.StringType,
				MarkdownDescription: "One to eight distinct chat models in `provider/model` format, each " +
					"answering independently.",
				Validators: []validator.List{
					listvalidator.SizeBetween(1, 8),
					listvalidator.UniqueValues(),
					listvalidator.ValueStringsAre(modelReferenceValidators()...),
				},
			},
			"judge_model": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Model in `provider/model` format that judges the anonymized panel answers.",
				Validators:          modelReferenceValidators(),
			},
			"synthesis_mode": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "`analysis` has a separate `orchestrator_model` synthesize the final " +
					"answer; `judge_writes_final` has the judge write it.",
				Validators: []validator.String{stringvalidator.OneOf(client.ModelFusionSynthesisModes...)},
			},
			"preset": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Prompt preset applied to the panel, judge, and synthesis phases: " +
					"`general`, `quality`, `budget`, `code`, or `safety`.",
				Validators: []validator.String{stringvalidator.OneOf(client.ModelFusionPresets...)},
			},
			"orchestrator_model": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Model in `provider/model` format that synthesizes the final answer. " +
					"Required when `synthesis_mode` is `analysis` and not allowed otherwise.",
				Validators: modelReferenceValidators(),
			},
			"web_tools": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Whether the panel and judge phases get the provider-native web search " +
					"tool. Defaults to `true`, except with the `budget` preset or `max_tool_calls = 0`, " +
					"where it is always `false`.",
			},
			"max_tool_calls": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Default:  int64default.StaticInt64(8),
				MarkdownDescription: "Maximum Fusion-managed web tool calls per participating phase, between " +
					"0 and 20. Zero disables them. Defaults to `8`.",
				Validators: []validator.Int64{int64validator.Between(0, 20)},
			},
			"feed_raw_to_synth": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
				MarkdownDescription: "Whether raw panel answers are included in the synthesis input. Defaults to `true`.",
			},
			"enabled": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
				MarkdownDescription: "Whether the Model Fusion is available to AI Gateway requests. Defaults to `true`.",
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

func (r *modelFusionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	r.fusions = c.ModelFusions()
}

// The server stores web_tools as false whenever the preset is budget or no tool calls are allowed.
func (m *modelFusionResourceModel) webToolsUnavailable() bool {
	budgetPreset := m.Preset.ValueString() == client.ModelFusionPresetBudget
	noToolCalls := !m.MaxToolCalls.IsNull() && !m.MaxToolCalls.IsUnknown() && m.MaxToolCalls.ValueInt64() == 0
	return budgetPreset || noToolCalls
}

func (m *modelFusionResourceModel) crossFieldDiagnostics() diag.Diagnostics {
	var diags diag.Diagnostics
	if m.WebTools.ValueBool() && m.webToolsUnavailable() {
		diags.AddAttributeError(path.Root("web_tools"), "Web tools unavailable",
			"web_tools cannot be true with the budget preset or with max_tool_calls = 0: the server stores it "+
				"as false, which fails the apply with an inconsistent result. Set web_tools to false or omit it.")
	}
	if m.SynthesisMode.IsNull() || m.SynthesisMode.IsUnknown() || m.OrchestratorModel.IsUnknown() {
		return diags
	}
	analysis := m.SynthesisMode.ValueString() == client.ModelFusionSynthesisModeAnalysis
	if analysis && m.OrchestratorModel.IsNull() {
		diags.AddAttributeError(path.Root("orchestrator_model"), "Missing orchestrator model",
			"orchestrator_model is required when synthesis_mode is analysis.")
	}
	if !analysis && !m.OrchestratorModel.IsNull() {
		diags.AddAttributeError(path.Root("orchestrator_model"), "Unexpected orchestrator model",
			"orchestrator_model is only allowed when synthesis_mode is analysis.")
	}
	return diags
}

func (r *modelFusionResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config modelFusionResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(config.crossFieldDiagnostics()...)
}

func (r *modelFusionResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan, config modelFusionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !config.WebTools.IsNull() || plan.Preset.IsUnknown() || plan.MaxToolCalls.IsUnknown() {
		return
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("web_tools"), types.BoolValue(!plan.webToolsUnavailable()))...)
}

func (r *modelFusionResource) apply(g *client.ModelFusion, m *modelFusionResourceModel) {
	m.ID = types.StringValue(g.ID)
	m.Key = types.StringValue(g.Key)
	m.ModelRef = types.StringValue(g.ModelRef)
	m.PanelModels = stringListValue(g.PanelModels)
	m.JudgeModel = types.StringValue(g.JudgeModel)
	m.SynthesisMode = types.StringValue(g.SynthesisMode)
	m.Preset = types.StringValue(g.Preset)
	m.OrchestratorModel = optStringPtr(g.OrchestratorModel)
	m.WebTools = types.BoolValue(g.WebTools)
	m.MaxToolCalls = types.Int64Value(g.MaxToolCalls)
	m.FeedRawToSynth = types.BoolValue(g.FeedRawToSynth)
	m.Enabled = types.BoolValue(g.Enabled)
	m.CreatedAt = types.StringValue(g.CreatedAt)
	m.UpdatedAt = types.StringValue(g.UpdatedAt)
}

func (m *modelFusionResourceModel) configInput(ctx context.Context) (client.ModelFusionConfigInput, diag.Diagnostics) {
	panelModels, diags := stringSlice(ctx, m.PanelModels)
	return client.ModelFusionConfigInput{
		PanelModels:       panelModels,
		JudgeModel:        m.JudgeModel.ValueString(),
		SynthesisMode:     m.SynthesisMode.ValueString(),
		Preset:            m.Preset.ValueString(),
		OrchestratorModel: strPtr(m.OrchestratorModel),
		WebTools:          boolPtr(m.WebTools),
		MaxToolCalls:      int64Ptr(m.MaxToolCalls),
		FeedRawToSynth:    boolPtr(m.FeedRawToSynth),
	}, diags
}

func (m *modelFusionResourceModel) configurationChanged(state *modelFusionResourceModel) bool {
	return !m.PanelModels.Equal(state.PanelModels) ||
		!m.JudgeModel.Equal(state.JudgeModel) ||
		!m.SynthesisMode.Equal(state.SynthesisMode) ||
		!m.Preset.Equal(state.Preset) ||
		!m.OrchestratorModel.Equal(state.OrchestratorModel) ||
		!m.WebTools.Equal(state.WebTools) ||
		!m.MaxToolCalls.Equal(state.MaxToolCalls) ||
		!m.FeedRawToSynth.Equal(state.FeedRawToSynth)
}

func (r *modelFusionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan modelFusionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(revalidatePlan(ctx, req.Plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(plan.crossFieldDiagnostics()...)
	config, diags := plan.configInput(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, err := r.fusions.Create(ctx, plan.Key.ValueString(), config)
	if err != nil {
		resp.Diagnostics.AddError("Unable to create model fusion", errDetail(err))
		return
	}
	if !plan.Enabled.ValueBool() {
		disabled, err := r.fusions.SetEnabled(ctx, g.ID, false)
		if err != nil {
			resp.Diagnostics.AddError("Unable to disable model fusion", errDetail(err))
		} else {
			g = disabled
		}
	}
	r.apply(g, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *modelFusionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state modelFusionResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, err := r.fusions.Get(ctx, state.ID.ValueString())
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read model fusion", errDetail(err))
		return
	}
	r.apply(g, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *modelFusionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state modelFusionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(revalidatePlan(ctx, req.Plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(plan.crossFieldDiagnostics()...)
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

// A pure enabled toggle skips the configuration write, which re-resolves every source model.
func (r *modelFusionResource) reconcile(ctx context.Context, plan, state *modelFusionResourceModel) (*client.ModelFusion, diag.Diagnostics) {
	var g *client.ModelFusion
	enabledChanged := !plan.Enabled.Equal(state.Enabled)
	config, diags := plan.configInput(ctx)
	if diags.HasError() {
		return nil, diags
	}
	if !enabledChanged || plan.configurationChanged(state) {
		updated, err := r.fusions.Update(ctx, plan.ID.ValueString(), config)
		if err != nil {
			diags.AddError("Unable to update model fusion", errDetail(err))
			return nil, diags
		}
		g = updated
	}
	if enabledChanged {
		toggled, err := r.fusions.SetEnabled(ctx, plan.ID.ValueString(), plan.Enabled.ValueBool())
		if err != nil {
			diags.AddError("Unable to set model fusion enabled state", errDetail(err))
			return g, diags
		}
		g = toggled
	}
	return g, diags
}

func (r *modelFusionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state modelFusionResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.fusions.Delete(ctx, state.ID.ValueString()); err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to delete model fusion", errDetail(err))
	}
}

func (r *modelFusionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
