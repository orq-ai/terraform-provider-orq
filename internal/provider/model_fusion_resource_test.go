package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

func TestModelFusionConfigInputDerivesWebToolsWhenUnknown(t *testing.T) {
	ctx := context.Background()

	// Omitted web_tools left Unknown with quality preset -> derives true.
	mQuality := modelFusionResourceModel{
		PanelModels:    stringList("openai/gpt-4o"),
		JudgeModel:     types.StringValue("openai/gpt-4o"),
		SynthesisMode:  types.StringValue("judge_writes_final"),
		Preset:         types.StringValue("quality"),
		WebTools:       types.BoolUnknown(),
		MaxToolCalls:   types.Int64Value(8),
		FeedRawToSynth: types.BoolValue(true),
	}
	in, diags := mQuality.configInput(ctx)
	if diags.HasError() {
		t.Fatalf("configInput: %v", diags)
	}
	if in.WebTools == nil || !*in.WebTools {
		t.Errorf("expected derived web_tools=true for quality preset, got %v", in.WebTools)
	}

	// Omitted web_tools left Unknown with budget preset -> derives false.
	mBudget := mQuality
	mBudget.Preset = types.StringValue(client.ModelFusionPresetBudget)
	inBudget, diags := mBudget.configInput(ctx)
	if diags.HasError() {
		t.Fatalf("configInput: %v", diags)
	}
	if inBudget.WebTools == nil || *inBudget.WebTools {
		t.Errorf("expected derived web_tools=false for budget preset, got %v", inBudget.WebTools)
	}
}

func TestModelFusionRejectsNullPanelModel(t *testing.T) {
	r := &modelFusionResource{}
	diags := revalidatePlan(context.Background(), planFor(t, r, modelFusionResourceModel{
		Key:            types.StringValue("fusion-1"),
		PanelModels:    types.ListValueMust(types.StringType, []attr.Value{types.StringNull()}),
		JudgeModel:     types.StringValue("openai/gpt-4o"),
		SynthesisMode:  types.StringValue("judge_writes_final"),
		Preset:         types.StringValue(client.ModelFusionPresetBudget),
		MaxToolCalls:   types.Int64Value(0),
		FeedRawToSynth: types.BoolValue(true),
		Enabled:        types.BoolValue(true),
	}))
	if !diags.HasError() {
		t.Error("expected null panel_models element to be rejected")
	}
}
