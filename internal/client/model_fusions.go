package client

import (
	"context"
	"fmt"
	"strings"

	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
)

const (
	modelFusionSynthesisModePrefix = "MODEL_FUSION_SYNTHESIS_MODE_"
	modelFusionPresetPrefix        = "MODEL_FUSION_PRESET_"

	ModelFusionSynthesisModeAnalysis = "analysis"
	ModelFusionPresetBudget          = "budget"
)

var (
	ModelFusionSynthesisModes = []string{ModelFusionSynthesisModeAnalysis, "judge_writes_final"}
	ModelFusionPresets        = []string{"general", "quality", ModelFusionPresetBudget, "code", "safety"}
)

type ModelFusion struct {
	ID                string
	Key               string
	ModelRef          string
	PanelModels       []string
	JudgeModel        string
	SynthesisMode     string
	Preset            string
	OrchestratorModel *string
	WebTools          bool
	MaxToolCalls      int64
	FeedRawToSynth    bool
	Enabled           bool
	CreatedAt         string
	UpdatedAt         string
}

// Nil WebTools, MaxToolCalls and FeedRawToSynth take the server defaults on
// create; an update replaces the whole configuration and requires all three.
type ModelFusionConfigInput struct {
	PanelModels       []string
	JudgeModel        string
	SynthesisMode     string
	Preset            string
	OrchestratorModel *string
	WebTools          *bool
	MaxToolCalls      *int64
	FeedRawToSynth    *bool
}

type ModelFusionsAPI interface {
	Get(ctx context.Context, id string) (*ModelFusion, error)
	Create(ctx context.Context, key string, in ModelFusionConfigInput) (*ModelFusion, error)
	Update(ctx context.Context, id string, in ModelFusionConfigInput) (*ModelFusion, error)
	SetEnabled(ctx context.Context, id string, enabled bool) (*ModelFusion, error)
	Delete(ctx context.Context, id string) error
}

type connectModelFusions struct {
	c platformv1connect.ModelFusionsServiceClient
}

func modelFusionEnumToProto(values map[string]int32, prefix, kind, short string) (int32, error) {
	value, ok := values[prefix+strings.ToUpper(short)]
	if !ok || value == 0 {
		return 0, &Error{Code: CodeInvalid, Message: fmt.Sprintf("unknown model fusion %s %q", kind, short)}
	}
	return value, nil
}

func modelFusionEnumFromProto(name, prefix string) string {
	return strings.ToLower(strings.TrimPrefix(name, prefix))
}

type modelFusionEnums struct {
	synthesisMode platformv1.ModelFusionSynthesisMode
	preset        platformv1.ModelFusionPreset
}

func (in ModelFusionConfigInput) enumsToProto() (modelFusionEnums, error) {
	synthesisMode, err := modelFusionEnumToProto(platformv1.ModelFusionSynthesisMode_value, modelFusionSynthesisModePrefix, "synthesis mode", in.SynthesisMode)
	if err != nil {
		return modelFusionEnums{}, err
	}
	preset, err := modelFusionEnumToProto(platformv1.ModelFusionPreset_value, modelFusionPresetPrefix, "preset", in.Preset)
	if err != nil {
		return modelFusionEnums{}, err
	}
	return modelFusionEnums{
		synthesisMode: platformv1.ModelFusionSynthesisMode(synthesisMode),
		preset:        platformv1.ModelFusionPreset(preset),
	}, nil
}

func maxToolCallsToProto(maxToolCalls *int64) *int32 {
	if maxToolCalls == nil {
		return nil
	}
	value := int32(*maxToolCalls)
	return &value
}

func modelFusionFromProto(g *platformv1.ModelFusion) *ModelFusion {
	return &ModelFusion{
		ID:                g.GetModelFusionId(),
		Key:               g.GetKey(),
		ModelRef:          g.GetModelRef(),
		PanelModels:       g.GetPanelModels(),
		JudgeModel:        g.GetJudgeModel(),
		SynthesisMode:     modelFusionEnumFromProto(g.GetSynthesisMode().String(), modelFusionSynthesisModePrefix),
		Preset:            modelFusionEnumFromProto(g.GetPreset().String(), modelFusionPresetPrefix),
		OrchestratorModel: g.OrchestratorModel,
		WebTools:          g.GetWebTools(),
		MaxToolCalls:      int64(g.GetMaxToolCalls()),
		FeedRawToSynth:    g.GetFeedRawToSynth(),
		Enabled:           g.GetEnabled(),
		CreatedAt:         formatTimestamp(g.GetCreatedAt()),
		UpdatedAt:         formatTimestamp(g.GetUpdatedAt()),
	}
}

func (c *connectModelFusions) Get(ctx context.Context, id string) (*ModelFusion, error) {
	resp, err := c.c.GetModelFusion(ctx, &platformv1.GetModelFusionRequest{ModelFusionId: id})
	if err != nil {
		return nil, mapConnectError("model fusion", err)
	}
	return modelFusionFromProto(resp.GetModelFusion()), nil
}

func (c *connectModelFusions) Create(ctx context.Context, key string, in ModelFusionConfigInput) (*ModelFusion, error) {
	enums, err := in.enumsToProto()
	if err != nil {
		return nil, err
	}
	resp, err := c.c.CreateModelFusion(ctx, &platformv1.CreateModelFusionRequest{
		Key:               key,
		PanelModels:       in.PanelModels,
		JudgeModel:        in.JudgeModel,
		SynthesisMode:     enums.synthesisMode,
		Preset:            enums.preset,
		OrchestratorModel: in.OrchestratorModel,
		WebTools:          in.WebTools,
		MaxToolCalls:      maxToolCallsToProto(in.MaxToolCalls),
		FeedRawToSynth:    in.FeedRawToSynth,
	})
	if err != nil {
		return nil, mapConnectError("model fusion", err)
	}
	return modelFusionFromProto(resp.GetModelFusion()), nil
}

func (c *connectModelFusions) Update(ctx context.Context, id string, in ModelFusionConfigInput) (*ModelFusion, error) {
	enums, err := in.enumsToProto()
	if err != nil {
		return nil, err
	}
	resp, err := c.c.UpdateModelFusion(ctx, &platformv1.UpdateModelFusionRequest{
		ModelFusionId:     id,
		PanelModels:       in.PanelModels,
		JudgeModel:        in.JudgeModel,
		SynthesisMode:     enums.synthesisMode,
		Preset:            enums.preset,
		OrchestratorModel: in.OrchestratorModel,
		WebTools:          in.WebTools,
		MaxToolCalls:      maxToolCallsToProto(in.MaxToolCalls),
		FeedRawToSynth:    in.FeedRawToSynth,
	})
	if err != nil {
		return nil, mapConnectError("model fusion", err)
	}
	return modelFusionFromProto(resp.GetModelFusion()), nil
}

func (c *connectModelFusions) SetEnabled(ctx context.Context, id string, enabled bool) (*ModelFusion, error) {
	resp, err := c.c.SetModelFusionEnabled(ctx, &platformv1.SetModelFusionEnabledRequest{ModelFusionId: id, Enabled: &enabled})
	if err != nil {
		return nil, mapConnectError("model fusion", err)
	}
	return modelFusionFromProto(resp.GetModelFusion()), nil
}

func (c *connectModelFusions) Delete(ctx context.Context, id string) error {
	_, err := c.c.DeleteModelFusion(ctx, &platformv1.DeleteModelFusionRequest{ModelFusionId: id})
	return mapConnectError("model fusion", err)
}
