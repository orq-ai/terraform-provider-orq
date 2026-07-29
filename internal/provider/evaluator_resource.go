package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

var (
	_ resource.Resource                   = &evaluatorResource{}
	_ resource.ResourceWithConfigure      = &evaluatorResource{}
	_ resource.ResourceWithImportState    = &evaluatorResource{}
	_ resource.ResourceWithValidateConfig = &evaluatorResource{}
)

// evaluatorKeyPattern mirrors EvaluatorKeySchema in the platform monorepo
// (libs/models/evaluators/src/schemas/evaluator.schemas.ts): letters, digits,
// dashes and underscores, never leading or trailing with a dash/underscore.
var evaluatorKeyPattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9_-]*[a-zA-Z0-9])?$`)

// ulidPattern is Crockford base32 (I, L, O and U excluded), 26 characters. Used
// to refuse an import id that cannot be an evaluator record id.
var ulidPattern = regexp.MustCompile(`^[0-9ABCDEFGHJKMNPQRSTVWXYZabcdefghjkmnpqrstvwxyz]{26}$`)

// reservedEvaluatorKeys are rejected by createEvalHandler with a 400. They are
// checked at PLAN time so the failure names the attribute instead of surfacing
// as an opaque apply-time error.
var reservedEvaluatorKeys = []string{"orq_pii_detection", "orq_secret_detection"}

// Evaluator output types, per evaluator type. python_eval accepts only a subset
// (PythonEvaluatorBaseSchema), llm_eval accepts all four.
var (
	evaluatorTypes = []string{client.EvaluatorTypePython, client.EvaluatorTypeLLM}
	// llmOutputTypes is also the schema-level enum: the attribute validator has to
	// accept the union of both types' values, and ValidateConfig then narrows it
	// to pythonOutputTypes for a python_eval.
	llmOutputTypes    = []string{"boolean", "number", "categorical", "string"}
	pythonOutputTypes = []string{"boolean", "number"}
)

// NewEvaluatorResource is the factory registered on the provider.
func NewEvaluatorResource() resource.Resource { return &evaluatorResource{} }

type evaluatorResource struct {
	evaluators client.EvaluatorsAPI
	// models resolves a stored model DOCUMENT ID back to its provider-qualified
	// ref. Needed because the by-id GET returns `model` as {id: …} while the
	// operator writes (and every write response echoes) "provider/model".
	models client.ModelsAPI
}

type evaluatorResourceModel struct {
	ID          types.String `tfsdk:"id"`
	Key         types.String `tfsdk:"key"`
	Type        types.String `tfsdk:"type"`
	Path        types.String `tfsdk:"path"`
	Description types.String `tfsdk:"description"`
	OutputType  types.String `tfsdk:"output_type"`
	Enabled     types.Bool   `tfsdk:"enabled"`
	ProjectID   types.String `tfsdk:"project_id"`
	CreatedAt   types.String `tfsdk:"created_at"`
	UpdatedAt   types.String `tfsdk:"updated_at"`

	Code types.String `tfsdk:"code"`

	Prompt      types.String `tfsdk:"prompt"`
	Mode        types.String `tfsdk:"mode"`
	Model       types.String `tfsdk:"model"`
	ModelID     types.String `tfsdk:"model_id"`
	Repetitions types.Int64  `tfsdk:"repetitions"`

	CategoricalLabels []evaluatorLabelModel `tfsdk:"categorical_labels"`
	Jury              *evaluatorJuryModel   `tfsdk:"jury"`
}

type evaluatorLabelModel struct {
	Value       types.String `tfsdk:"value"`
	Description types.String `tfsdk:"description"`
}

type evaluatorJuryModel struct {
	Judges              []evaluatorJudgeModel `tfsdk:"judges"`
	ReplacementJudges   []evaluatorJudgeModel `tfsdk:"replacement_judges"`
	MinSuccessfulJudges types.Int64           `tfsdk:"min_successful_judges"`
}

type evaluatorJudgeModel struct {
	Model     types.String         `tfsdk:"model"`
	Retry     *evaluatorRetryModel `tfsdk:"retry"`
	Fallbacks []types.String       `tfsdk:"fallbacks"`
}

type evaluatorRetryModel struct {
	Count   types.Int64   `tfsdk:"count"`
	OnCodes []types.Int64 `tfsdk:"on_codes"`
}

func (r *evaluatorResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_evaluator"
}

func judgeAttributes(what string) map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"model": schema.StringAttribute{
			Required:            true,
			MarkdownDescription: "Provider-qualified model ref for this " + what + ", e.g. `openai/gpt-4o`. It must support tool calling — the server rejects a model that does not.",
		},
		"retry": schema.SingleNestedAttribute{
			Optional:            true,
			MarkdownDescription: "Retry policy for this " + what + ". Omit for the server defaults (2 attempts on 429/500/502/503/504).",
			Attributes: map[string]schema.Attribute{
				"count": schema.Int64Attribute{
					Optional:            true,
					MarkdownDescription: "Number of attempts, 1-5.",
					Validators:          []validator.Int64{int64validator.Between(1, 5)},
				},
				"on_codes": schema.ListAttribute{
					Optional:            true,
					ElementType:         types.Int64Type,
					MarkdownDescription: "HTTP status codes that trigger a retry (100-599).",
					Validators: []validator.List{
						listvalidator.SizeAtLeast(1),
						listvalidator.ValueInt64sAre(int64validator.Between(100, 599)),
					},
				},
			},
		},
		"fallbacks": schema.ListAttribute{
			Optional:            true,
			ElementType:         types.StringType,
			MarkdownDescription: "Ordered fallback model refs tried when this " + what + " fails. Written as a plain list of `provider/model` strings; the API spells each as an object, which this provider builds for you.",
			Validators: []validator.List{
				listvalidator.ValueStringsAre(stringvalidator.LengthAtLeast(1)),
			},
		},
	}
}

func (r *evaluatorResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An orq evaluator of type `python_eval` or `llm_eval`.\n\n" +
			"**Scope:** only those two types are managed. The API also serves `function_eval`, `ragas`, " +
			"`json_schema`, `http_eval`, `typescript_eval` and `bedrock_eval`; this resource refuses to adopt " +
			"them (read and import both error out rather than mis-manage a record).\n\n" +
			"**Two response shapes.** `POST`/`PATCH /v2/evaluators` answer with the EXTERNAL representation " +
			"(`key`, `model` as a `provider/model` string, no `output_type`/`enabled`/`domain_id`), while " +
			"`GET /v2/evaluators/{id}` answers with the STORED record (`display_name`, `model` as an object " +
			"holding a model DOCUMENT ID, plus `output_type`, `enabled` and `domain_id`). Every create and " +
			"update therefore performs the write and then re-reads the evaluator by id, and state is assembled " +
			"from both halves — see `model` and `output_type`.\n\n" +
			"**Update is a field merge.** The API applies the patch with a mongo `$set`, so a field this " +
			"resource does not send keeps its stored value. Attributes the resource does not model at all " +
			"(`guardrail_config`, `categories`, `dataset_id`) are consequently never sent and never read: they " +
			"are left entirely to the UI/API and produce no diff here.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Evaluator id (a ULID) assigned by orq.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"key": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Unique evaluator key within the workspace. Letters, digits, `-` and `_`, " +
					"never starting or ending with `-`/`_`. It is stored as `display_name` and returned as `key` " +
					"by the write endpoints; the two are the same value. Renaming is allowed (the API patches it) " +
					"but the server matches uniqueness case-INSENSITIVELY, so `MyEval` and `myeval` collide.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					stringvalidator.RegexMatches(evaluatorKeyPattern,
						"must contain only letters, numbers, dashes (-) and underscores (_), and must not start or end with a dash or underscore"),
					reservedEvaluatorKeyValidator{},
				},
			},
			"type": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Evaluator type: `python_eval` or `llm_eval`. IMMUTABLE — the API rejects a " +
					"type change with a 400, so changing it forces replacement.",
				Validators:    []validator.String{stringvalidator.OneOf(evaluatorTypes...)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"path": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Storage path, e.g. `Default` or `Default/evaluators`. The first element is " +
					"the project; nested folders are auto-created.\n\n" +
					"WRITE-ONLY: the server resolves it to a project/folder, stores the result as `project_id` and " +
					"then discards the path — no endpoint returns it. It is therefore config-authoritative, an " +
					"out-of-band move is invisible here (watch `project_id` instead), and after `terraform import` " +
					"it is unset until the first apply re-asserts it.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"description": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Free-text description. Defaults to an empty string server-side.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"output_type": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Type of the value the evaluator produces. `python_eval` accepts `boolean` " +
					"or `number`; `llm_eval` accepts those plus `categorical` and `string`. Defaults to `number` " +
					"server-side.\n\n" +
					"Only the by-id GET reports it — the create/update responses omit it — which is why every " +
					"write is followed by a read-back.",
				Validators:    []validator.String{stringvalidator.OneOf(llmOutputTypes...)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"enabled": schema.BoolAttribute{
				Computed: true,
				MarkdownDescription: "Whether the evaluator is enabled. Read-only: the public create/update " +
					"bodies do not accept it (it is toggled in the UI).",
			},
			"project_id": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Id of the project the evaluator lives in, resolved by the server from " +
					"`path` (stored as `domain_id`).",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Creation time as returned by the API.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Last update time as returned by the API.",
			},
			"code": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Python source for a `python_eval`, required for that type and rejected for " +
					"any other. Keep it in its own file and load it with " +
					"`file(\"${path.module}/eval.py\")` so it stays lintable and diffable.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"prompt": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Judge prompt for an `llm_eval`, required for that type and rejected for any other.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"mode": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "`llm_eval` judging mode: `single` (one `model`) or `jury` (a `jury` block). " +
					"Required for `llm_eval`, rejected otherwise.\n\n" +
					"Changing it forces replacement. The API update is a `$set` FIELD MERGE with no `$unset`, so " +
					"switching jury→single would leave the abandoned `jury` sub-document in the stored record — " +
					"and the server's own serializer prefers a present `jury` over `mode`, so the evaluator would " +
					"keep running as a jury while reporting `single`. Replacement is the only clean transition.",
				Validators:    []validator.String{stringvalidator.OneOf("single", "jury")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"model": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Provider-qualified judge model for `mode = \"single\"`, e.g. " +
					"`openai/gpt-4o`. It must support tool calling.\n\n" +
					"The stored record keeps a model DOCUMENT ID, not this string, so a refresh cannot read the " +
					"string back directly. Resolution: the create/update response echoes the string verbatim and " +
					"is used as-is; on refresh the stored document id is compared with `model_id` from state and " +
					"the string is kept when they match. When they differ (the model was changed out of band) the " +
					"id is re-resolved through the model catalog and the resulting `provider/model` ref is written " +
					"— a raw document id is NEVER written into this attribute. If the catalog cannot resolve it, " +
					"the previous string is kept and a warning is raised.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"model_id": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Model document id the server stored for `model`. Exposed because it is the " +
					"only model identity the by-id GET returns, and it is what refresh compares against to detect " +
					"an out-of-band model change. Null for `python_eval` and for `mode = \"jury\"`.",
			},
			"repetitions": schema.Int64Attribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "How many times an `llm_eval` runs per evaluation, 1-3. Defaults to 1 server-side.",
				Validators:          []validator.Int64{int64validator.Between(1, 3)},
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"categorical_labels": schema.ListNestedAttribute{
				Optional: true,
				MarkdownDescription: "Allowed output values when `output_type = \"categorical\"` (at least two, " +
					"and no two may differ only by case or surrounding whitespace — the server compares them " +
					"trimmed and lower-cased). Only meaningful for `llm_eval`.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"value": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "The label the judge must emit.",
							Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
						},
						"description": schema.StringAttribute{
							Optional:            true,
							MarkdownDescription: "What this label means; shown to the judge.",
						},
					},
				},
				Validators: []validator.List{listvalidator.SizeAtLeast(1)},
			},
			"jury": schema.SingleNestedAttribute{
				Optional: true,
				MarkdownDescription: "Jury configuration for `mode = \"jury\"`. Required for that mode and " +
					"rejected otherwise.\n\n" +
					"CONFIG-AUTHORITATIVE: the stored record holds each judge as a model DOCUMENT ID, so reading " +
					"the block back would cost one catalog lookup per judge on every refresh. This resource sends " +
					"the block on every apply and keeps the configured value in state instead, which means an " +
					"out-of-band jury edit is NOT detected here (the next apply overwrites it).",
				Attributes: map[string]schema.Attribute{
					"judges": schema.ListNestedAttribute{
						Required:            true,
						MarkdownDescription: "The judges. At least two are required.",
						NestedObject:        schema.NestedAttributeObject{Attributes: judgeAttributes("judge")},
						Validators:          []validator.List{listvalidator.SizeAtLeast(2)},
					},
					"replacement_judges": schema.ListNestedAttribute{
						Optional:            true,
						MarkdownDescription: "Stand-ins used when a judge fails outright. They count towards `min_successful_judges`.",
						NestedObject:        schema.NestedAttributeObject{Attributes: judgeAttributes("replacement judge")},
					},
					"min_successful_judges": schema.Int64Attribute{
						Optional: true,
						MarkdownDescription: "How many judges must return a verdict for the evaluation to count. " +
							"At least 2, and never more than `judges` + `replacement_judges`. Defaults to 2 server-side.",
						Validators: []validator.Int64{int64validator.AtLeast(2)},
					},
				},
			},
		},
	}
}

// reservedEvaluatorKeyValidator rejects the two system-owned keys at plan time.
// The server answers a create carrying one with a bare 400 ("The key is reserved
// for the system."), which is a poor apply-time surprise for a value that is
// knowable from config alone.
type reservedEvaluatorKeyValidator struct{}

func (reservedEvaluatorKeyValidator) Description(context.Context) string {
	return "must not be one of the reserved system evaluator keys (" + strings.Join(reservedEvaluatorKeys, ", ") + ")"
}

func (v reservedEvaluatorKeyValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (reservedEvaluatorKeyValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	value := req.ConfigValue.ValueString()
	for _, reserved := range reservedEvaluatorKeys {
		if value == reserved {
			resp.Diagnostics.AddAttributeError(req.Path, "Reserved evaluator key",
				fmt.Sprintf("%q is reserved for orq's built-in evaluators and cannot be used for a managed one. "+
					"Pick a different key.", value))
			return
		}
	}
}

func (r *evaluatorResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	r.evaluators = c.Evaluators()
	r.models = c.Models()
}

// --- plan-time validation ----------------------------------------------------

// ValidateConfig mirrors the cross-field invariants that
// libs/models/evaluators/src/schemas/evaluator.schemas.ts enforces with zod
// refinements, so a violation is a `terraform validate` error naming the
// attribute rather than an opaque 400 halfway through an apply.
//
// It reads the config attribute-by-attribute instead of decoding the whole
// model: a config value may still be UNKNOWN at validate time (it references
// another resource), and unknown collection values cannot be reflected into the
// Go slice fields of evaluatorResourceModel. Every check below is skipped when
// the values it needs are unknown; Create/Update re-run the same checks once
// everything is resolved (the framework never re-runs ValidateConfig), and the
// server remains the final backstop.
func (r *evaluatorResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	resp.Diagnostics.Append(validateEvaluatorConfig(ctx, req.Config)...)
}

// validateEvaluatorConfig is the shared body of ValidateConfig and the
// Create/Update recheck.
func validateEvaluatorConfig(ctx context.Context, cfg tfsdk.Config) diag.Diagnostics {
	var diags diag.Diagnostics
	var typ, mode, code, prompt, model, outputType types.String
	var repetitions types.Int64
	var jury types.Object
	var labels types.List

	diags.Append(cfg.GetAttribute(ctx, path.Root("type"), &typ)...)
	diags.Append(cfg.GetAttribute(ctx, path.Root("mode"), &mode)...)
	diags.Append(cfg.GetAttribute(ctx, path.Root("code"), &code)...)
	diags.Append(cfg.GetAttribute(ctx, path.Root("prompt"), &prompt)...)
	diags.Append(cfg.GetAttribute(ctx, path.Root("model"), &model)...)
	diags.Append(cfg.GetAttribute(ctx, path.Root("output_type"), &outputType)...)
	diags.Append(cfg.GetAttribute(ctx, path.Root("repetitions"), &repetitions)...)
	diags.Append(cfg.GetAttribute(ctx, path.Root("jury"), &jury)...)
	diags.Append(cfg.GetAttribute(ctx, path.Root("categorical_labels"), &labels)...)
	if diags.HasError() {
		return diags
	}

	// An unknown `type` cannot select a branch; the per-attribute validators and
	// the server still apply.
	if typ.IsUnknown() || typ.IsNull() {
		return diags
	}

	switch typ.ValueString() {
	case client.EvaluatorTypePython:
		validatePythonEvaluator(code, prompt, mode, model, outputType, repetitions, jury, labels, &diags)
	case client.EvaluatorTypeLLM:
		validateLLMEvaluator(code, prompt, mode, model, outputType, jury, labels, &diags)
	}
	return diags
}

// present reports whether an attribute is set in config. An UNKNOWN value counts
// as present: the operator wrote something, its value is just not resolved yet.
func present(v attrValue) bool { return !v.IsNull() }

// attrValue is the minimal surface the presence checks need.
type attrValue interface{ IsNull() bool }

func rejectAttr(diags *diag.Diagnostics, p path.Path, name, typ string) {
	diags.AddAttributeError(p, "Attribute not supported for this evaluator type",
		fmt.Sprintf("`%s` is not part of a %s evaluator and must be removed.", name, typ))
}

func validatePythonEvaluator(code, prompt, mode, model, outputType types.String, repetitions types.Int64, jury types.Object, labels types.List, diags *diag.Diagnostics) {
	if !present(code) {
		diags.AddAttributeError(path.Root("code"), "Missing evaluator code",
			"`code` is required for a python_eval evaluator. Load it from a file, e.g. "+
				"`code = file(\"${path.module}/eval.py\")`.")
	}
	if !outputType.IsNull() && !outputType.IsUnknown() {
		if !contains(pythonOutputTypes, outputType.ValueString()) {
			diags.AddAttributeError(path.Root("output_type"), "Unsupported output_type for python_eval",
				fmt.Sprintf("python_eval supports only %s (got %q).",
					strings.Join(quoteAll(pythonOutputTypes), " or "), outputType.ValueString()))
		}
	}
	if present(prompt) {
		rejectAttr(diags, path.Root("prompt"), "prompt", "python_eval")
	}
	if present(mode) {
		rejectAttr(diags, path.Root("mode"), "mode", "python_eval")
	}
	if present(model) {
		rejectAttr(diags, path.Root("model"), "model", "python_eval")
	}
	if present(repetitions) {
		rejectAttr(diags, path.Root("repetitions"), "repetitions", "python_eval")
	}
	if present(jury) {
		rejectAttr(diags, path.Root("jury"), "jury", "python_eval")
	}
	if present(labels) {
		rejectAttr(diags, path.Root("categorical_labels"), "categorical_labels", "python_eval")
	}
}

func validateLLMEvaluator(code, prompt, mode, model, outputType types.String, jury types.Object, labels types.List, diags *diag.Diagnostics) {
	if !present(prompt) {
		diags.AddAttributeError(path.Root("prompt"), "Missing judge prompt",
			"`prompt` is required for an llm_eval evaluator.")
	}
	if present(code) {
		rejectAttr(diags, path.Root("code"), "code", "llm_eval")
	}
	if !present(mode) {
		diags.AddAttributeError(path.Root("mode"), "Missing llm_eval mode",
			"`mode` is required for an llm_eval evaluator: use `single` with `model`, or `jury` with a `jury` block.")
	}

	// model XOR jury, regardless of what `mode` says (the server refuses both
	// together even when mode picks one of them).
	if present(model) && present(jury) {
		diags.AddAttributeError(path.Root("mode"), "model and jury cannot be combined",
			"An llm_eval evaluator judges either with a single `model` or with a `jury`, never both. "+
				"Set `mode = \"single\"` with `model`, or `mode = \"jury\"` with `jury`.")
	}

	if !mode.IsNull() && !mode.IsUnknown() {
		switch mode.ValueString() {
		case "single":
			if !present(model) {
				diags.AddAttributeError(path.Root("model"), "Missing judge model",
					"`model` is required when `mode = \"single\"`.")
			}
			if present(jury) {
				diags.AddAttributeError(path.Root("jury"), "jury is not allowed for mode = \"single\"",
					"Remove the `jury` block or set `mode = \"jury\"`.")
			}
		case "jury":
			if !present(jury) {
				diags.AddAttributeError(path.Root("jury"), "Missing jury configuration",
					"`jury` is required when `mode = \"jury\"`.")
			}
			if present(model) {
				diags.AddAttributeError(path.Root("model"), "model is not allowed for mode = \"jury\"",
					"Remove `model` or set `mode = \"single\"`.")
			}
			if !outputType.IsNull() && !outputType.IsUnknown() && outputType.ValueString() == "string" {
				diags.AddAttributeError(path.Root("output_type"), "jury does not support output_type = \"string\"",
					"A jury has to compare verdicts, which free-form strings do not allow. Use `boolean`, "+
						"`number` or `categorical`, or switch to `mode = \"single\"`.")
			}
		}
	}

	validateJuryObject(jury, diags)
	validateCategoricalLabels(outputType, labels, diags)
}

// validateJuryObject checks the sizing invariant LlmEvaluatorJuryExternalSchema
// enforces: min_successful_judges may never exceed judges + replacement_judges.
// (`judges` having at least 2 entries is enforced by the list validator.)
func validateJuryObject(jury types.Object, diags *diag.Diagnostics) {
	if jury.IsNull() || jury.IsUnknown() {
		return
	}
	attrs := jury.Attributes()
	minSuccessful, _ := attrs["min_successful_judges"].(types.Int64)
	if minSuccessful.IsNull() || minSuccessful.IsUnknown() {
		return
	}
	judges, ok := attrs["judges"].(types.List)
	if !ok || judges.IsNull() || judges.IsUnknown() {
		return
	}
	total := len(judges.Elements())
	if replacements, ok := attrs["replacement_judges"].(types.List); ok {
		if replacements.IsUnknown() {
			return
		}
		if !replacements.IsNull() {
			total += len(replacements.Elements())
		}
	}
	if minSuccessful.ValueInt64() > int64(total) {
		diags.AddAttributeError(path.Root("jury").AtName("min_successful_judges"),
			"min_successful_judges exceeds the number of judges",
			fmt.Sprintf("min_successful_judges is %d but only %d judge(s) are configured "+
				"(judges + replacement_judges). The evaluation could never succeed.",
				minSuccessful.ValueInt64(), total))
	}
}

// validateCategoricalLabels enforces the two categorical invariants
// validateCategoricalFields applies: at least two labels when
// output_type = "categorical", and no two labels that collide once trimmed and
// lower-cased (the server's own comparison).
func validateCategoricalLabels(outputType types.String, labels types.List, diags *diag.Diagnostics) {
	isCategorical := !outputType.IsNull() && !outputType.IsUnknown() && outputType.ValueString() == "categorical"
	if labels.IsUnknown() {
		return
	}
	if labels.IsNull() {
		if isCategorical {
			diags.AddAttributeError(path.Root("categorical_labels"), "Missing categorical labels",
				"`categorical_labels` is required when `output_type = \"categorical\"`, with at least two labels.")
		}
		return
	}

	elements := labels.Elements()
	if isCategorical && len(elements) < 2 {
		diags.AddAttributeError(path.Root("categorical_labels"), "Too few categorical labels",
			fmt.Sprintf("`output_type = \"categorical\"` needs at least two labels to choose between (got %d).",
				len(elements)))
	}

	seen := make(map[string]int, len(elements))
	for i, element := range elements {
		obj, ok := element.(types.Object)
		if !ok || obj.IsNull() || obj.IsUnknown() {
			continue
		}
		value, ok := obj.Attributes()["value"].(types.String)
		if !ok || value.IsNull() || value.IsUnknown() {
			continue
		}
		normalized := strings.ToLower(strings.TrimSpace(value.ValueString()))
		if first, dup := seen[normalized]; dup {
			diags.AddAttributeError(path.Root("categorical_labels").AtListIndex(i).AtName("value"),
				"Duplicate categorical label",
				fmt.Sprintf("label %d repeats label %d: the server compares label values trimmed and "+
					"case-insensitively, so %q is not distinct.", i, first, value.ValueString()))
			continue
		}
		seen[normalized] = i
	}
}

func contains(haystack []string, needle string) bool {
	for _, candidate := range haystack {
		if candidate == needle {
			return true
		}
	}
	return false
}

func quoteAll(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, `"`+v+`"`)
	}
	return out
}

// --- config -> request -------------------------------------------------------

func labelsFromModel(models []evaluatorLabelModel) []client.CategoricalLabel {
	if len(models) == 0 {
		return nil
	}
	out := make([]client.CategoricalLabel, 0, len(models))
	for _, l := range models {
		out = append(out, client.CategoricalLabel{Value: l.Value.ValueString(), Description: strPtr(l.Description)})
	}
	return out
}

func judgesFromModel(models []evaluatorJudgeModel) []client.JuryJudge {
	if len(models) == 0 {
		return nil
	}
	out := make([]client.JuryJudge, 0, len(models))
	for _, j := range models {
		judge := client.JuryJudge{Model: j.Model.ValueString()}
		if j.Retry != nil {
			retry := &client.JuryRetry{Count: int64Ptr(j.Retry.Count)}
			for _, c := range j.Retry.OnCodes {
				if !c.IsNull() && !c.IsUnknown() {
					retry.OnCodes = append(retry.OnCodes, c.ValueInt64())
				}
			}
			judge.Retry = retry
		}
		for _, f := range j.Fallbacks {
			if !f.IsNull() && !f.IsUnknown() {
				judge.Fallbacks = append(judge.Fallbacks, f.ValueString())
			}
		}
		out = append(out, judge)
	}
	return out
}

func juryFromModel(m *evaluatorJuryModel) *client.Jury {
	if m == nil {
		return nil
	}
	return &client.Jury{
		Judges:              judgesFromModel(m.Judges),
		ReplacementJudges:   judgesFromModel(m.ReplacementJudges),
		MinSuccessfulJudges: int64Ptr(m.MinSuccessfulJudges),
	}
}

// createInput builds the POST body from the CONFIG.
//
// The config — not the plan — is authoritative for the write, for the same
// reason as orq_workspace_settings: for an Optional+Computed attribute a null
// config plans as the PRIOR STATE, so a plan-derived body would re-assert values
// the operator deliberately left to the server (`description`, `output_type`,
// `repetitions`) and would fight an out-of-band change to them instead of
// adopting it. Every other attribute is Required or Optional-only, where config
// and plan are identical.
func (m *evaluatorResourceModel) createInput() client.EvaluatorCreateInput {
	return client.EvaluatorCreateInput{
		Key:               m.Key.ValueString(),
		Type:              m.Type.ValueString(),
		Path:              m.Path.ValueString(),
		Description:       strPtr(m.Description),
		OutputType:        strPtr(m.OutputType),
		Code:              strPtr(m.Code),
		Prompt:            strPtr(m.Prompt),
		Mode:              strPtr(m.Mode),
		Model:             strPtr(m.Model),
		Repetitions:       int64Ptr(m.Repetitions),
		Jury:              juryFromModel(m.Jury),
		CategoricalLabels: labelsFromModel(m.CategoricalLabels),
	}
}

// updateInput builds the PATCH body from the CONFIG (see createInput).
//
// ClearCategoricalLabels is set whenever the config carries no labels: the
// update is a `$set` field merge, so a removed block has to be spelled as an
// explicit null or the stored labels would survive and refresh would keep
// re-reporting them — a diff that never converges.
func (m *evaluatorResourceModel) updateInput(id string) client.EvaluatorUpdateInput {
	labels := labelsFromModel(m.CategoricalLabels)
	return client.EvaluatorUpdateInput{
		ID:                     id,
		Key:                    m.Key.ValueString(),
		Type:                   m.Type.ValueString(),
		Path:                   m.Path.ValueString(),
		Description:            strPtr(m.Description),
		OutputType:             strPtr(m.OutputType),
		Code:                   strPtr(m.Code),
		Prompt:                 strPtr(m.Prompt),
		Mode:                   strPtr(m.Mode),
		Model:                  strPtr(m.Model),
		Repetitions:            int64Ptr(m.Repetitions),
		Jury:                   juryFromModel(m.Jury),
		CategoricalLabels:      labels,
		ClearCategoricalLabels: len(labels) == 0,
	}
}

// --- server -> state ---------------------------------------------------------

// evalPreserveString keeps ANY KNOWN planned value — an explicit null included,
// which for these Optional+Computed attributes means "the config set nothing and
// the prior state was empty" — and consults the read-back only for an UNKNOWN
// one (a create where the config omitted the attribute). Post-apply state must
// equal the plan for every known planned value or Terraform aborts the apply
// with "Provider produced inconsistent result after apply".
func evalPreserveString(planned types.String, srv string) types.String {
	if !planned.IsUnknown() {
		return planned
	}
	return optString(srv)
}

// evalPreserveInt64 is evalPreserveString for an optional integer.
func evalPreserveInt64(planned types.Int64, srv *int64) types.Int64 {
	if !planned.IsUnknown() {
		return planned
	}
	if srv == nil {
		return types.Int64Null()
	}
	return types.Int64Value(*srv)
}

// labelsToModel projects the stored categorical labels onto state.
func labelsToModel(labels []client.CategoricalLabel) []evaluatorLabelModel {
	if len(labels) == 0 {
		return nil
	}
	out := make([]evaluatorLabelModel, 0, len(labels))
	for _, l := range labels {
		out = append(out, evaluatorLabelModel{
			Value:       types.StringValue(l.Value),
			Description: optStringPtr(l.Description),
		})
	}
	return out
}

// applyRead writes the STORED record onto m. READ PATH ONLY: the server wins for
// every attribute it can express, so real out-of-band drift surfaces.
//
// Three attributes are deliberately NOT touched here:
//   - `path`, which no endpoint returns (see the schema),
//   - `jury`, which the stored record spells with model document ids,
//   - `model`, resolved separately by resolveModelRef.
func applyRead(e *client.Evaluator, m *evaluatorResourceModel) {
	m.ID = types.StringValue(e.ID)
	m.Key = types.StringValue(e.Key)
	m.Type = types.StringValue(e.Type)
	m.Description = optString(e.Description)
	m.OutputType = optString(e.OutputType)
	m.Enabled = types.BoolValue(e.Enabled)
	m.ProjectID = optString(e.ProjectID)
	m.CreatedAt = optString(e.Created)
	m.UpdatedAt = optString(e.Updated)
	m.Code = optString(e.Code)
	m.Prompt = optString(e.Prompt)
	m.Mode = optString(e.Mode)
	if e.Repetitions != nil {
		m.Repetitions = types.Int64Value(*e.Repetitions)
	} else {
		m.Repetitions = types.Int64Null()
	}
	m.ModelID = evaluatorModelID(e)
	m.CategoricalLabels = labelsToModel(e.CategoricalLabels)
}

// evaluatorModelID reports the model document id that belongs in state. A jury
// evaluator has no single judge model, and a record that was once single-mode
// can still carry a stale `model` sub-document (the update is a `$set` merge),
// so mode — not the presence of the field — decides.
func evaluatorModelID(e *client.Evaluator) types.String {
	if e.Mode == "jury" {
		return types.StringNull()
	}
	return optString(e.ModelID)
}

// applyWrite assembles state from BOTH response shapes after a create/update.
//
// internal is the by-id read-back (the only source of output_type, enabled,
// project_id and the model document id); external is the create/update response
// (the only source of the provider-qualified `model` string). The PLAN wins for
// every known planned value; the read-back may only fill what the plan left
// unknown. That is the same authority split as orq_workspace_settings, and it is
// what keeps the two shapes from producing a diff: nothing the write path reads
// back can overwrite a value the operator configured.
func applyWrite(internal, external *client.Evaluator, m *evaluatorResourceModel) {
	m.ID = types.StringValue(external.ID)
	m.Key = evalPreserveString(m.Key, internal.Key)
	m.Type = evalPreserveString(m.Type, internal.Type)
	m.Description = evalPreserveString(m.Description, internal.Description)
	m.OutputType = evalPreserveString(m.OutputType, internal.OutputType)
	m.Repetitions = evalPreserveInt64(m.Repetitions, internal.Repetitions)
	m.Code = evalPreserveString(m.Code, internal.Code)
	m.Prompt = evalPreserveString(m.Prompt, internal.Prompt)
	m.Mode = evalPreserveString(m.Mode, internal.Mode)

	// Computed-only: always the server's value.
	m.Enabled = types.BoolValue(internal.Enabled)
	m.ProjectID = optString(internal.ProjectID)
	m.CreatedAt = optString(internal.Created)
	m.UpdatedAt = optString(internal.Updated)
	m.ModelID = evaluatorModelID(internal)

	// `model`: the external response echoes the string that was sent, so it is
	// the authoritative spelling here — but only for a plan that left it unknown.
	if m.Model.IsUnknown() {
		m.Model = optString(external.Model)
	}

	// categorical_labels / jury / path stay exactly as planned.
}

// nullUnknowns collapses every attribute that may still be unknown to a null, so
// a PARTIAL state can be persisted after a write that succeeded but whose
// read-back failed. Terraform rejects unknown values in post-apply state, and
// dropping the state entirely would orphan the evaluator that was just created.
func nullUnknowns(m *evaluatorResourceModel) {
	for _, s := range []*types.String{
		&m.ID, &m.Key, &m.Type, &m.Path, &m.Description, &m.OutputType,
		&m.ProjectID, &m.CreatedAt, &m.UpdatedAt, &m.Code, &m.Prompt,
		&m.Mode, &m.Model, &m.ModelID,
	} {
		if s.IsUnknown() {
			*s = types.StringNull()
		}
	}
	if m.Enabled.IsUnknown() {
		m.Enabled = types.BoolNull()
	}
	if m.Repetitions.IsUnknown() {
		m.Repetitions = types.Int64Null()
	}
}

// resolveModelRef decides what `model` holds after a refresh.
//
// The stored record only knows the model DOCUMENT ID, so the provider-qualified
// string cannot be read back directly. The recorded model_id is used as the
// drift detector: while it still matches, the string in state is by construction
// the one that produced it and is kept verbatim (no catalog call at all). Once
// it differs — the model was changed out of band, or this is a fresh import with
// no recorded id — the id is resolved through the model catalog and the ref is
// written. A raw document id is NEVER written into `model`.
func (r *evaluatorResource) resolveModelRef(ctx context.Context, internal *client.Evaluator, prior evaluatorResourceModel) (types.String, diag.Diagnostics) {
	var diags diag.Diagnostics
	if internal.Mode == "jury" || internal.ModelID == "" {
		return types.StringNull(), diags
	}
	if !prior.ModelID.IsNull() && prior.ModelID.ValueString() == internal.ModelID &&
		!prior.Model.IsNull() && !prior.Model.IsUnknown() {
		return prior.Model, diags
	}
	m, err := r.models.Get(ctx, internal.ModelID)
	if err != nil {
		diags.AddWarning("Could not resolve the evaluator's judge model",
			"The evaluator references model document "+internal.ModelID+", which could not be resolved to a "+
				"provider/model ref: "+errDetail(err)+"\n\nThe previously known value for `model` was kept, so a "+
				"real change to the judge model may not show up in the next plan.")
		if prior.Model.IsUnknown() {
			return types.StringNull(), diags
		}
		return prior.Model, diags
	}
	if m.RefID != "" {
		return types.StringValue(m.RefID), diags
	}
	if m.Provider != "" && m.ModelID != "" {
		return types.StringValue(m.Provider + "/" + m.ModelID), diags
	}
	diags.AddWarning("Could not resolve the evaluator's judge model",
		"The model catalog returned no provider-qualified ref for document "+internal.ModelID+
			". The previously known value for `model` was kept.")
	if prior.Model.IsUnknown() {
		return types.StringNull(), diags
	}
	return prior.Model, diags
}

// assertManagedType refuses to project an evaluator this resource does not
// manage onto state. Without it an import (or an id typo) of e.g. a `ragas`
// evaluator would be adopted, and the next apply would try to "fix" its type —
// which the API rejects — or destroy and recreate it as something else.
func assertManagedType(kind string) diag.Diagnostic {
	if kind == client.EvaluatorTypePython || kind == client.EvaluatorTypeLLM {
		return nil
	}
	return diag.NewErrorDiagnostic("Unsupported evaluator type",
		fmt.Sprintf("The evaluator is of type %q, which orq_evaluator does not manage (it handles %s only). "+
			"Manage it through the UI or API instead.", kind, strings.Join(quoteAll(evaluatorTypes), " and ")))
}

// --- CRUD --------------------------------------------------------------------

func (r *evaluatorResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, cfg evaluatorResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	// The CONFIG decides what is written; the plan carries what lands in state.
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	// Re-run the cross-field checks: ValidateConfig may have deferred some of
	// them on an unknown value, and the framework never re-runs it at apply.
	resp.Diagnostics.Append(validateEvaluatorConfig(ctx, req.Config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	external, err := r.evaluators.Create(ctx, cfg.createInput())
	if err != nil {
		resp.Diagnostics.AddError("Unable to create evaluator", errDetail(err))
		return
	}

	internal, err := r.evaluators.Get(ctx, external.ID)
	if err != nil {
		// The evaluator EXISTS. Persist what we know (above all its id) so the next
		// apply updates it instead of creating a duplicate, and fail loudly.
		resp.Diagnostics.AddError("Evaluator created but could not be read back",
			"The evaluator was created with id "+external.ID+", but reading it back failed: "+errDetail(err)+
				"\n\nIts id has been written to state so it is not orphaned; run `terraform plan` again to "+
				"finish reconciling it.")
		plan.ID = types.StringValue(external.ID)
		nullUnknowns(&plan)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}

	applyWrite(internal, external, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *evaluatorResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state evaluatorResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	internal, err := r.evaluators.Get(ctx, state.ID.ValueString())
	if err != nil {
		if isNotFound(err) {
			// Deleted out of band: drop it so the next plan re-creates it.
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read evaluator", errDetail(err))
		return
	}
	if d := assertManagedType(internal.Type); d != nil {
		resp.Diagnostics.Append(d)
		return
	}

	prior := state
	applyRead(internal, &state)
	model, diags := r.resolveModelRef(ctx, internal, prior)
	resp.Diagnostics.Append(diags...)
	state.Model = model
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *evaluatorResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, cfg, state evaluatorResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	// See Create: the deferred cross-field checks are re-run here once known.
	resp.Diagnostics.Append(validateEvaluatorConfig(ctx, req.Config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()

	external, err := r.evaluators.Update(ctx, cfg.updateInput(id))
	if err != nil {
		resp.Diagnostics.AddError("Unable to update evaluator", errDetail(err))
		return
	}

	internal, err := r.evaluators.Get(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Evaluator updated but could not be read back",
			"The update of evaluator "+id+" was accepted, but reading it back failed: "+errDetail(err)+
				"\n\nRun `terraform plan` again to reconcile.")
		plan.ID = types.StringValue(id)
		nullUnknowns(&plan)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}

	applyWrite(internal, external, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *evaluatorResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state evaluatorResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.evaluators.Delete(ctx, state.ID.ValueString()); err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to delete evaluator", errDetail(err))
	}
}

// ImportState adopts an existing evaluator by its ULID:
//
//	terraform import orq_evaluator.this 01JMDPA3QW5C1V0NJ1PW34T4E5
//
// A non-ULID id is refused outright. orq's BUILT-IN evaluators are addressed by
// slug (`orq_pii_detection`, …) and have no evaluator record at all, so passing
// one would otherwise produce a confusing 404 at refresh time; and only a record
// id can be adopted.
//
// `path` cannot be imported — no endpoint returns it — so it stays null until
// the first apply after the import re-asserts it from config.
func (r *evaluatorResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !ulidPattern.MatchString(req.ID) {
		resp.Diagnostics.AddError("Invalid evaluator import id",
			fmt.Sprintf("%q is not a ULID. Import an evaluator by the 26-character id the API returns as `_id`, "+
				"e.g. 01JMDPA3QW5C1V0NJ1PW34T4E5.\n\norq's built-in evaluators (orq_pii_detection, "+
				"orq_secret_detection, …) are addressed by slug and have no evaluator record — they cannot be "+
				"imported or managed here.", req.ID))
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
