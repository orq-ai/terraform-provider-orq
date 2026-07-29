package provider

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

var evaluatorKeyPattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9_-]*[a-zA-Z0-9])?$`)

// ulidPattern is Crockford base32 (I, L, O and U excluded), 26 characters.
var ulidPattern = regexp.MustCompile(`^[0-9ABCDEFGHJKMNPQRSTVWXYZabcdefghjkmnpqrstvwxyz]{26}$`)

var reservedEvaluatorKeys = []string{"orq_pii_detection", "orq_secret_detection"}

var (
	evaluatorTypes = []string{client.EvaluatorTypePython, client.EvaluatorTypeLLM}
	// llmOutputTypes doubles as the schema-level enum: the attribute validator
	// accepts the union of both types' values and the cross-field rules below
	// narrow it to pythonOutputTypes for a python_eval.
	llmOutputTypes    = []string{"boolean", "number", "categorical", "string"}
	pythonOutputTypes = []string{"boolean", "number"}
)

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
	if slices.Contains(reservedEvaluatorKeys, value) {
		resp.Diagnostics.AddAttributeError(req.Path, "Reserved evaluator key",
			fmt.Sprintf("%q is reserved for orq's built-in evaluators and cannot be used for a managed one. "+
				"Pick a different key.", value))
	}
}

func (r *evaluatorResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	resp.Diagnostics.Append(validateEvaluatorConfig(ctx, req.Config)...)
}

// evaluatorConfig is the subset of the config the cross-field rules inspect. It
// is read attribute-by-attribute rather than through evaluatorResourceModel
// because a value may still be UNKNOWN at validate time, and an unknown
// collection cannot be reflected into the model's Go slice fields.
type evaluatorConfig struct {
	Type        types.String
	Mode        types.String
	Code        types.String
	Prompt      types.String
	Model       types.String
	OutputType  types.String
	Repetitions types.Int64
	Jury        types.Object
	Labels      types.List
}

func readEvaluatorConfig(ctx context.Context, cfg tfsdk.Config) (evaluatorConfig, diag.Diagnostics) {
	var c evaluatorConfig
	var diags diag.Diagnostics
	diags.Append(cfg.GetAttribute(ctx, path.Root("type"), &c.Type)...)
	diags.Append(cfg.GetAttribute(ctx, path.Root("mode"), &c.Mode)...)
	diags.Append(cfg.GetAttribute(ctx, path.Root("code"), &c.Code)...)
	diags.Append(cfg.GetAttribute(ctx, path.Root("prompt"), &c.Prompt)...)
	diags.Append(cfg.GetAttribute(ctx, path.Root("model"), &c.Model)...)
	diags.Append(cfg.GetAttribute(ctx, path.Root("output_type"), &c.OutputType)...)
	diags.Append(cfg.GetAttribute(ctx, path.Root("repetitions"), &c.Repetitions)...)
	diags.Append(cfg.GetAttribute(ctx, path.Root("jury"), &c.Jury)...)
	diags.Append(cfg.GetAttribute(ctx, path.Root("categorical_labels"), &c.Labels)...)
	return c, diags
}

// validateEvaluatorConfig mirrors the zod refinements the server applies, so a
// violation is a `terraform validate` error naming the attribute rather than an
// opaque 400 halfway through an apply. Every check is skipped when the values it
// needs are unknown; Create/Update re-run it once everything is resolved,
// because the framework never re-runs ValidateConfig.
func validateEvaluatorConfig(ctx context.Context, cfg tfsdk.Config) diag.Diagnostics {
	c, diags := readEvaluatorConfig(ctx, cfg)
	if diags.HasError() {
		return diags
	}
	if c.Type.IsUnknown() || c.Type.IsNull() {
		return diags
	}
	switch c.Type.ValueString() {
	case client.EvaluatorTypePython:
		c.validatePython(&diags)
	case client.EvaluatorTypeLLM:
		c.validateLLM(&diags)
	}
	return diags
}

// attrValue is the minimal surface the presence checks need.
type attrValue interface{ IsNull() bool }

// present reports whether an attribute is set in config. An UNKNOWN value counts
// as present: the operator wrote something, its value is just not resolved yet.
func present(v attrValue) bool { return !v.IsNull() }

// known reports whether a value can be branched on right now.
func known(v types.String) bool { return !v.IsNull() && !v.IsUnknown() }

func rejectAttr(diags *diag.Diagnostics, name, evaluatorType string) {
	diags.AddAttributeError(path.Root(name), "Attribute not supported for this evaluator type",
		fmt.Sprintf("`%s` is not part of a %s evaluator and must be removed.", name, evaluatorType))
}

func (c evaluatorConfig) validatePython(diags *diag.Diagnostics) {
	if !present(c.Code) {
		diags.AddAttributeError(path.Root("code"), "Missing evaluator code",
			"`code` is required for a python_eval evaluator. Load it from a file, e.g. "+
				"`code = file(\"${path.module}/eval.py\")`.")
	}
	if known(c.OutputType) && !slices.Contains(pythonOutputTypes, c.OutputType.ValueString()) {
		diags.AddAttributeError(path.Root("output_type"), "Unsupported output_type for python_eval",
			fmt.Sprintf("python_eval supports only %s (got %q).",
				strings.Join(quoteAll(pythonOutputTypes), " or "), c.OutputType.ValueString()))
	}
	for _, unsupported := range []struct {
		name  string
		value attrValue
	}{
		{"prompt", c.Prompt},
		{"mode", c.Mode},
		{"model", c.Model},
		{"repetitions", c.Repetitions},
		{"jury", c.Jury},
		{"categorical_labels", c.Labels},
	} {
		if present(unsupported.value) {
			rejectAttr(diags, unsupported.name, client.EvaluatorTypePython)
		}
	}
}

func (c evaluatorConfig) validateLLM(diags *diag.Diagnostics) {
	if !present(c.Prompt) {
		diags.AddAttributeError(path.Root("prompt"), "Missing judge prompt",
			"`prompt` is required for an llm_eval evaluator.")
	}
	if present(c.Code) {
		rejectAttr(diags, "code", client.EvaluatorTypeLLM)
	}
	if !present(c.Mode) {
		diags.AddAttributeError(path.Root("mode"), "Missing llm_eval mode",
			"`mode` is required for an llm_eval evaluator: use `single` with `model`, or `jury` with a `jury` block.")
	}

	// model XOR jury holds regardless of what `mode` says: the server refuses
	// both together even when mode picks one of them.
	if present(c.Model) && present(c.Jury) {
		diags.AddAttributeError(path.Root("mode"), "model and jury cannot be combined",
			"An llm_eval evaluator judges either with a single `model` or with a `jury`, never both. "+
				"Set `mode = \"single\"` with `model`, or `mode = \"jury\"` with `jury`.")
	}

	if known(c.Mode) {
		switch c.Mode.ValueString() {
		case "single":
			if !present(c.Model) {
				diags.AddAttributeError(path.Root("model"), "Missing judge model",
					"`model` is required when `mode = \"single\"`.")
			}
			if present(c.Jury) {
				diags.AddAttributeError(path.Root("jury"), "jury is not allowed for mode = \"single\"",
					"Remove the `jury` block or set `mode = \"jury\"`.")
			}
		case "jury":
			if !present(c.Jury) {
				diags.AddAttributeError(path.Root("jury"), "Missing jury configuration",
					"`jury` is required when `mode = \"jury\"`.")
			}
			if present(c.Model) {
				diags.AddAttributeError(path.Root("model"), "model is not allowed for mode = \"jury\"",
					"Remove `model` or set `mode = \"single\"`.")
			}
			if known(c.OutputType) && c.OutputType.ValueString() == "string" {
				diags.AddAttributeError(path.Root("output_type"), "jury does not support output_type = \"string\"",
					"A jury has to compare verdicts, which free-form strings do not allow. Use `boolean`, "+
						"`number` or `categorical`, or switch to `mode = \"single\"`.")
			}
		}
	}

	c.validateJurySizing(diags)
	c.validateCategoricalLabels(diags)
}

// validateJurySizing rejects a min_successful_judges no run could ever reach.
// (`judges` having at least 2 entries is enforced by the list validator.)
func (c evaluatorConfig) validateJurySizing(diags *diag.Diagnostics) {
	if c.Jury.IsNull() || c.Jury.IsUnknown() {
		return
	}
	attrs := c.Jury.Attributes()
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

func (c evaluatorConfig) validateCategoricalLabels(diags *diag.Diagnostics) {
	isCategorical := known(c.OutputType) && c.OutputType.ValueString() == "categorical"
	if c.Labels.IsUnknown() {
		return
	}
	if c.Labels.IsNull() {
		if isCategorical {
			diags.AddAttributeError(path.Root("categorical_labels"), "Missing categorical labels",
				"`categorical_labels` is required when `output_type = \"categorical\"`, with at least two labels.")
		}
		return
	}

	elements := c.Labels.Elements()
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
		// The server compares label values trimmed and lower-cased.
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

func quoteAll(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, `"`+v+`"`)
	}
	return out
}
