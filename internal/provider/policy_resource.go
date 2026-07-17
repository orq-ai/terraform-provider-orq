package provider

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
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
	_ resource.Resource                = &policyResource{}
	_ resource.ResourceWithConfigure   = &policyResource{}
	_ resource.ResourceWithImportState = &policyResource{}
)

// NewPolicyResource is the factory registered on the provider.
func NewPolicyResource() resource.Resource { return &policyResource{} }

type policyResource struct {
	policies client.PoliciesAPI
}

type policyResourceModel struct {
	ID           types.String           `tfsdk:"id"`
	DisplayName  types.String           `tfsdk:"display_name"`
	Description  types.String           `tfsdk:"description"`
	Enabled      types.Bool             `tfsdk:"enabled"`
	ProjectID    types.String           `tfsdk:"project_id"`
	Slug         types.String           `tfsdk:"slug"`
	Timeout      types.Int64            `tfsdk:"timeout"`
	Evaluators   []policyEvaluatorModel `tfsdk:"evaluators"`
	Limits       jsontypes.Normalized   `tfsdk:"limits"`
	ModelsConfig jsontypes.Normalized   `tfsdk:"models_config"`
	RetryConfig  jsontypes.Normalized   `tfsdk:"retry_config"`
	CreatedAt    types.String           `tfsdk:"created_at"`
	UpdatedAt    types.String           `tfsdk:"updated_at"`
}

type policyEvaluatorModel struct {
	ID          types.String         `tfsdk:"id"`
	ExecuteOn   types.String         `tfsdk:"execute_on"`
	SampleRate  types.Float64        `tfsdk:"sample_rate"`
	IsGuardrail types.Bool           `tfsdk:"is_guardrail"`
	Options     jsontypes.Normalized `tfsdk:"options"`
}

func (r *policyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_policy"
}

func (r *policyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A policy (router entity). Carries a mutable nullable `project_id`; unlike " +
			"routing/guardrail rules, changing `project_id` updates in place (no replacement).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Policy ID assigned by orq.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"display_name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Human-readable policy name.",
			},
			"description": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Optional description.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"enabled": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Whether the policy is enabled. Defaults server-side when omitted.",
			},
			"project_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Owning project. Omit for a workspace-global policy. Mutable (updates in place).",
			},
			"slug": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "URL-safe slug derived from the display name by orq.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"timeout": schema.Int64Attribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Evaluation timeout in milliseconds (>= 1000). Defaults server-side when omitted.",
			},
			"limits": schema.StringAttribute{
				CustomType:          jsontypes.NormalizedType{},
				Optional:            true,
				MarkdownDescription: "Budget / request / token limits as a JSON object string. Compared semantically.",
			},
			"models_config": schema.StringAttribute{
				CustomType: jsontypes.NormalizedType{},
				Optional:   true,
				Computed:   true,
				MarkdownDescription: "Model routing configuration as a JSON object string. Compared semantically. " +
					"A model with an omitted or zero `weight` is stored by the server with `weight` = 0.5; " +
					"that default is canonicalized into the plan so config and read-back converge.",
				PlanModifiers: []planmodifier.String{
					jsonCanonPlanModifier{fn: modelsConfigWeightCanon, retainOnNull: true, description: "canonicalize model weights to the server default"},
				},
			},
			"retry_config": schema.StringAttribute{
				CustomType: jsontypes.NormalizedType{},
				Optional:   true,
				Computed:   true,
				MarkdownDescription: "Retry configuration as a JSON object string (`{\"count\":...,\"on_codes\":[...]}`). " +
					"Compared semantically. An empty `on_codes` is elided by the server on read; that is " +
					"canonicalized into the plan so config and read-back converge.",
				PlanModifiers: []planmodifier.String{
					jsonCanonPlanModifier{fn: retryConfigOnCodesCanon, retainOnNull: true, description: "canonicalize empty on_codes to the server's elided form"},
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
		Blocks: map[string]schema.Block{
			"evaluators": schema.ListNestedBlock{
				MarkdownDescription: "Referenced evaluators (mirrors guardrail refs).",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "Evaluator ID.",
						},
						"execute_on": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "When to run: `input`, `output`, or `both`.",
							Validators: []validator.String{
								stringvalidator.OneOf("input", "output", "both"),
							},
						},
						"sample_rate": schema.Float64Attribute{
							Optional:            true,
							MarkdownDescription: "Fraction of requests to evaluate (0-1).",
						},
						"is_guardrail": schema.BoolAttribute{
							Optional: true,
							Computed: true,
							MarkdownDescription: "Whether this reference is enforced as a guardrail (blocking). " +
								"The server stores a non-pointer bool defaulting to `false` and elides it on read; " +
								"an omitted value reads back as `false`.",
						},
						"options": schema.StringAttribute{
							CustomType: jsontypes.NormalizedType{},
							Optional:   true,
							Computed:   true,
							MarkdownDescription: "Arbitrary per-evaluator configuration as a JSON object string. " +
								"Compared semantically. An empty object is elided by the server on read; that is " +
								"canonicalized to null in the plan so config and read-back converge. Preserved across updates.",
							PlanModifiers: []planmodifier.String{
								jsonCanonPlanModifier{fn: optionsEmptyToNullCanon, retainOnNull: false, description: "canonicalize an empty options object to null"},
							},
						},
					},
				},
			},
		},
	}
}

func (r *policyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	r.policies = c.Policies()
}

func policyEvaluatorsFromModel(models []policyEvaluatorModel) ([]client.EvaluatorRef, diag.Diagnostics) {
	var diags diag.Diagnostics
	if models == nil {
		return nil, diags
	}
	out := make([]client.EvaluatorRef, 0, len(models))
	for i, m := range models {
		ref := client.EvaluatorRef{
			ID:          m.ID.ValueString(),
			ExecuteOn:   m.ExecuteOn.ValueString(),
			SampleRate:  float64Ptr(m.SampleRate),
			IsGuardrail: boolPtr(m.IsGuardrail),
		}
		if !m.Options.IsNull() && !m.Options.IsUnknown() {
			var opts map[string]any
			if err := json.Unmarshal([]byte(m.Options.ValueString()), &opts); err != nil {
				diags.AddAttributeError(
					path.Root("evaluators").AtListIndex(i).AtName("options"),
					"Invalid evaluator options",
					"options must be a JSON object: "+err.Error(),
				)
				continue
			}
			ref.Options = opts
		}
		out = append(out, ref)
	}
	return out, diags
}

func (r *policyResource) apply(p *client.Policy, m *policyResourceModel) {
	m.ID = types.StringValue(p.ID)
	m.DisplayName = types.StringValue(p.DisplayName)
	// description is Optional+Computed: the server elides an empty description
	// (omitempty), so a "" config would otherwise read back null and error as an
	// inconsistent apply. Normalize empty to "" and let Computed absorb an
	// omitted config (prior state is retained, so no perpetual diff).
	m.Description = types.StringValue(p.Description)
	m.Enabled = types.BoolValue(p.Enabled)
	m.ProjectID = optString(p.ProjectID)
	m.Slug = types.StringValue(p.Slug)
	m.Timeout = types.Int64Value(p.Timeout)
	m.Limits = rawToNormalized(p.Limits)
	m.ModelsConfig = rawToNormalized(p.ModelsConfig)
	m.RetryConfig = rawToNormalized(p.RetryConfig)
	m.CreatedAt = types.StringValue(p.CreatedAt)
	m.UpdatedAt = types.StringValue(p.UpdatedAt)

	if len(p.Evaluators) == 0 {
		m.Evaluators = nil
		return
	}
	evs := make([]policyEvaluatorModel, 0, len(p.Evaluators))
	for _, e := range p.Evaluators {
		em := policyEvaluatorModel{
			ID:        types.StringValue(e.ID),
			ExecuteOn: types.StringValue(e.ExecuteOn),
		}
		if e.SampleRate != nil {
			em.SampleRate = types.Float64Value(*e.SampleRate)
		} else {
			em.SampleRate = types.Float64Null()
		}
		// The server stores is_guardrail as a non-pointer bool defaulting to
		// false and elides it on read (omitempty), so a nil pointer means false.
		// Surfacing false (not null) keeps state == an explicit `is_guardrail =
		// false` config; the attribute is Computed so an omitted config converges.
		if e.IsGuardrail != nil {
			em.IsGuardrail = types.BoolValue(*e.IsGuardrail)
		} else {
			em.IsGuardrail = types.BoolValue(false)
		}
		if e.Options != nil {
			if b, err := json.Marshal(e.Options); err == nil {
				em.Options = jsontypes.NewNormalizedValue(string(b))
			} else {
				em.Options = jsontypes.NewNormalizedNull()
			}
		} else {
			em.Options = jsontypes.NewNormalizedNull()
		}
		evs = append(evs, em)
	}
	m.Evaluators = evs
}

func (r *policyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan policyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	evs, evDiags := policyEvaluatorsFromModel(plan.Evaluators)
	resp.Diagnostics.Append(evDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.policies.Create(ctx, client.PolicyCreateInput{
		DisplayName:  plan.DisplayName.ValueString(),
		Description:  strPtr(plan.Description),
		Enabled:      boolPtr(plan.Enabled),
		ProjectID:    strPtr(plan.ProjectID),
		Timeout:      int64Ptr(plan.Timeout),
		Evaluators:   evs,
		Limits:       normalizedToRaw(plan.Limits),
		ModelsConfig: normalizedToRaw(plan.ModelsConfig),
		RetryConfig:  normalizedToRaw(plan.RetryConfig),
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to create policy", errDetail(err))
		return
	}
	r.apply(p, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *policyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state policyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.policies.Get(ctx, state.ID.ValueString())
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read policy", errDetail(err))
		return
	}
	r.apply(p, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *policyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan policyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	evs, evDiags := policyEvaluatorsFromModel(plan.Evaluators)
	resp.Diagnostics.Append(evDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Removing every evaluator block must clear them server-side. The server
	// fully replaces evaluators when the field is present (policies/routes.go),
	// so send an explicit empty (non-nil) slice — a nil would omit the field and
	// leave the prior evaluators in place, drifting forever.
	if evs == nil {
		evs = []client.EvaluatorRef{}
	}
	name := plan.DisplayName.ValueString()
	// project_id is mutable and clearable: send it explicitly (empty when the
	// config omits it) so dropping project_id reverts the policy to
	// workspace-global instead of retaining the server's prior value.
	projectID := plan.ProjectID.ValueString()
	p, err := r.policies.Update(ctx, client.PolicyUpdateInput{
		ID:           plan.ID.ValueString(),
		DisplayName:  &name,
		Description:  strPtr(plan.Description),
		Enabled:      boolPtr(plan.Enabled),
		ProjectID:    &projectID,
		Timeout:      int64Ptr(plan.Timeout),
		Evaluators:   evs,
		Limits:       normalizedToRaw(plan.Limits),
		ModelsConfig: normalizedToRaw(plan.ModelsConfig),
		RetryConfig:  normalizedToRaw(plan.RetryConfig),
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to update policy", errDetail(err))
		return
	}
	r.apply(p, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *policyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state policyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.policies.Delete(ctx, state.ID.ValueString()); err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to delete policy", errDetail(err))
	}
}

func (r *policyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
