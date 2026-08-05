package client

import (
	"context"
	"encoding/json"
	"math"
	"net/http"

	"github.com/orq-ai/terraform-provider-orq/internal/restgen"
)

// ModelProviderAWS is the `provider` discriminator the server stamps on a model
// created through the AWS Bedrock endpoint. It is shared with legacy AWS models
// registered through the generic create endpoint, so IsBedrock narrows it
// further before anything is PATCHed or DELETEd.
const ModelProviderAWS = "aws"

// The two credential-resolution modes the Bedrock endpoints accept.
const (
	BedrockAuthModeIntegration = "integration"
	BedrockAuthModePodIdentity = "pod-identity"
)

// The server encodes max_tokens / temperature as slider entries in the model's
// `parameters` array, keyed by these names and carrying the configured value in
// config.max (apps/platform-api/models/openai_like.go).
const (
	bedrockParamTemperature = "temperature"
	bedrockParamMaxTokens   = "maxTokens"
	bedrockParamConfigMax   = "max"
)

// BedrockModel is the projection of a custom AWS Bedrock model.
//
// No credentials are ever stored on the model, and the server strips
// assume_role_arn / assume_role_external_id from every response
// (ModelConfigurationResponse omits them), so those two are absent here and stay
// config-authoritative in the resource.
type BedrockModel struct {
	ID          string
	RefID       string
	DisplayName string
	ModelID     string // the inference-profile ARN
	ModelType   string
	Description string
	Provider    string // configuration/provider discriminator ("aws" for Bedrock)
	Owner       string // "system" for system models; workspace id for custom ones
	Created     string // RFC 3339 (UTC)
	Updated     string // RFC 3339 (UTC)

	Region              string
	AuthMode            string
	IntegrationID       string
	InferenceProfileArn string
	ModelDeveloper      string
	ModelFamily         string

	InputCost  *float64
	OutputCost *float64

	// Decoded from the `parameters` array; nil when the model carries no such
	// slider, which is the server's encoding of "unset".
	MaxTokens   *int64
	Temperature *float64

	// HasFunctions is the top-level tool-calling flag; unlike its metadata twin
	// it is always serialized, so a flip to false is visible.
	HasFunctions bool

	// Nil distinguishes "the server omitted this metadata field (its value is
	// false)" from a value.
	SupportsToolCalling       *bool
	SupportsVision            *bool
	SupportsStrictTool        *bool
	SupportsJSONMode          *bool
	SupportsJSONSchema        *bool
	HasReasoning              *bool
	SupportsAdaptiveReasoning *bool
	SupportsExtendedThinking  *bool
}

// IsBedrock reports whether the catalog document is a workspace model created
// through the Bedrock endpoint: provider "aws" plus an inference-profile ARN and
// one of the two Bedrock auth modes in its configuration.
func (m *BedrockModel) IsBedrock() bool {
	if m.Provider != ModelProviderAWS || m.InferenceProfileArn == "" {
		return false
	}
	return m.AuthMode == BedrockAuthModeIntegration || m.AuthMode == BedrockAuthModePodIdentity
}

// BedrockModelCreateInput's leading fields are required by the API; the rest are
// omitted from the write when nil.
type BedrockModelCreateInput struct {
	DisplayName    string
	ModelID        string
	Region         string
	ModelDeveloper string
	AuthMode       string

	ModelType     *string
	ModelFamily   *string
	Description   *string
	IntegrationID *string

	AssumeRoleArn        *string
	AssumeRoleExternalID *string

	MaxTokens   *int64
	Temperature *float64
	InputCost   *float64
	OutputCost  *float64

	SupportsToolCalling       *bool
	SupportsVision            *bool
	SupportsStrictTool        *bool
	SupportsJSONMode          *bool
	SupportsJSONSchema        *bool
	HasReasoning              *bool
	SupportsAdaptiveReasoning *bool
	SupportsExtendedThinking  *bool
}

// BedrockModelUpdateInput omits auth_mode, integration_id and model_type: the
// PATCH endpoint does not accept them, so the resource marks all three
// RequiresReplace.
type BedrockModelUpdateInput struct {
	ID string

	DisplayName    string
	ModelID        string
	Region         string
	ModelDeveloper string

	ModelFamily *string
	Description *string

	AssumeRoleArn        *string
	AssumeRoleExternalID *string

	MaxTokens   *int64
	Temperature *float64
	InputCost   *float64
	OutputCost  *float64

	SupportsToolCalling       *bool
	SupportsVision            *bool
	SupportsStrictTool        *bool
	SupportsJSONMode          *bool
	SupportsJSONSchema        *bool
	HasReasoning              *bool
	SupportsAdaptiveReasoning *bool
	SupportsExtendedThinking  *bool
}

// BedrockModelsAPI has no single-GET route, so Get lists /v2/models and filters.
// Delete uses the shared /v2/models/:id route.
type BedrockModelsAPI interface {
	Get(ctx context.Context, id string) (*BedrockModel, error)
	Create(ctx context.Context, in BedrockModelCreateInput) (*BedrockModel, error)
	Update(ctx context.Context, in BedrockModelUpdateInput) (*BedrockModel, error)
	Delete(ctx context.Context, id string) error
}

type restBedrockModels struct {
	c *restgen.ClientWithResponses
}

func bedrockModelFromDocument(d *restgen.ModelDocument) BedrockModel {
	m := BedrockModel{
		ID:           d.Id,
		RefID:        d.RefId,
		DisplayName:  d.DisplayName,
		ModelID:      d.ModelId,
		ModelType:    d.ModelType,
		Provider:     d.Provider,
		Owner:        d.Owner,
		Created:      normalizeInstant(d.Created),
		Updated:      normalizeInstant(d.Updated),
		InputCost:    d.InputCost,
		OutputCost:   d.OutputCost,
		HasFunctions: d.HasFunctions,

		SupportsToolCalling:       d.Metadata.SupportsToolCalling,
		SupportsVision:            d.Metadata.SupportsVision,
		SupportsStrictTool:        d.Metadata.SupportsStrictTool,
		SupportsJSONMode:          d.Metadata.SupportsJsonModeResponseFormat,
		SupportsJSONSchema:        d.Metadata.SupportsJsonSchemaResponseFormat,
		HasReasoning:              d.Metadata.SupportsReasoning,
		SupportsAdaptiveReasoning: d.Metadata.SupportsAdaptiveReasoning,
		SupportsExtendedThinking:  d.Metadata.SupportsExtendedThinking,
	}
	if d.Description != nil {
		m.Description = *d.Description
	}
	if d.ModelDeveloper != nil {
		m.ModelDeveloper = *d.ModelDeveloper
	}
	if d.ModelFamily != nil {
		m.ModelFamily = *d.ModelFamily
	}
	m.Region = firstNonEmpty(d.Configuration.Region, d.Metadata.Region)
	m.AuthMode = firstNonEmpty(d.Configuration.AuthMode)
	m.IntegrationID = firstNonEmpty(d.Configuration.IntegrationId)
	m.InferenceProfileArn = firstNonEmpty(d.Configuration.InferenceProfileArn)
	m.Temperature = bedrockParameterMax(d.Parameters, bedrockParamTemperature)
	if maxTokens := bedrockParameterMax(d.Parameters, bedrockParamMaxTokens); maxTokens != nil {
		rounded := int64(math.Round(*maxTokens))
		m.MaxTokens = &rounded
	}
	return m
}

// bedrockParameterMax reads a slider's configured ceiling — the value the model
// was created/updated with. An absent slider means the value is unset.
func bedrockParameterMax(params *[]restgen.ModelParameterDocument, parameter string) *float64 {
	if params == nil {
		return nil
	}
	for _, p := range *params {
		if p.Parameter != parameter {
			continue
		}
		if v, ok := p.Config[bedrockParamConfigMax].(float64); ok {
			return &v
		}
	}
	return nil
}

// decodeBedrockModelBody decodes a create/update response. Those operations
// return the same ModelDocument shape as the list, but oapi-codegen models them
// as inline anonymous structs, so the body is decoded into the named type.
func decodeBedrockModelBody(body []byte) (*BedrockModel, error) {
	var d restgen.ModelDocument
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, &Error{Code: CodeInternal, Message: "decoding bedrock model response: " + err.Error()}
	}
	m := bedrockModelFromDocument(&d)
	return &m, nil
}

func (r *restBedrockModels) Get(ctx context.Context, id string) (*BedrockModel, error) {
	docs, err := listModelDocuments(ctx, r.c)
	if err != nil {
		return nil, err
	}
	for i := range docs {
		if docs[i].Id != id {
			continue
		}
		m := bedrockModelFromDocument(&docs[i])
		return &m, nil
	}
	return nil, modelNotVisibleError(id)
}

func (r *restBedrockModels) Create(ctx context.Context, in BedrockModelCreateInput) (*BedrockModel, error) {
	body := restgen.ModelCreateAwsBedrockJSONRequestBody{
		DisplayName:    in.DisplayName,
		ModelId:        in.ModelID,
		Region:         in.Region,
		ModelDeveloper: in.ModelDeveloper,
		AuthMode:       in.AuthMode,

		ModelType:     in.ModelType,
		ModelFamily:   in.ModelFamily,
		Description:   in.Description,
		IntegrationId: in.IntegrationID,

		AssumeRoleArn:        in.AssumeRoleArn,
		AssumeRoleExternalId: in.AssumeRoleExternalID,

		MaxTokens:   in.MaxTokens,
		Temperature: in.Temperature,
		InputCost:   in.InputCost,
		OutputCost:  in.OutputCost,

		SupportsToolCalling:       in.SupportsToolCalling,
		SupportsVision:            in.SupportsVision,
		SupportsStrictTool:        in.SupportsStrictTool,
		SupportsJsonMode:          in.SupportsJSONMode,
		SupportsJsonSchema:        in.SupportsJSONSchema,
		HasReasoning:              in.HasReasoning,
		SupportsAdaptiveReasoning: in.SupportsAdaptiveReasoning,
		SupportsExtendedThinking:  in.SupportsExtendedThinking,
	}
	resp, err := r.c.ModelCreateAwsBedrockWithResponse(ctx, body)
	if err != nil {
		return nil, mapRESTTransportError("bedrock model", err)
	}
	if resp.StatusCode() != http.StatusOK && resp.StatusCode() != http.StatusCreated {
		return nil, mapRESTStatus(resp.StatusCode(), resp.Body)
	}
	return decodeBedrockModelBody(resp.Body)
}

func (r *restBedrockModels) Update(ctx context.Context, in BedrockModelUpdateInput) (*BedrockModel, error) {
	displayName := in.DisplayName
	modelID := in.ModelID
	region := in.Region
	modelDeveloper := in.ModelDeveloper
	body := restgen.ModelUpdateAwsBedrockJSONRequestBody{
		DisplayName:    &displayName,
		ModelId:        &modelID,
		Region:         &region,
		ModelDeveloper: &modelDeveloper,

		ModelFamily: in.ModelFamily,
		Description: in.Description,

		AssumeRoleArn:        in.AssumeRoleArn,
		AssumeRoleExternalId: in.AssumeRoleExternalID,

		MaxTokens:   in.MaxTokens,
		Temperature: in.Temperature,
		InputCost:   in.InputCost,
		OutputCost:  in.OutputCost,

		SupportsToolCalling:       in.SupportsToolCalling,
		SupportsVision:            in.SupportsVision,
		SupportsStrictTool:        in.SupportsStrictTool,
		SupportsJsonMode:          in.SupportsJSONMode,
		SupportsJsonSchema:        in.SupportsJSONSchema,
		HasReasoning:              in.HasReasoning,
		SupportsAdaptiveReasoning: in.SupportsAdaptiveReasoning,
		SupportsExtendedThinking:  in.SupportsExtendedThinking,
	}
	resp, err := r.c.ModelUpdateAwsBedrockWithResponse(ctx, in.ID, body)
	if err != nil {
		return nil, mapRESTTransportError("bedrock model", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, mapRESTStatus(resp.StatusCode(), resp.Body)
	}
	return decodeBedrockModelBody(resp.Body)
}

func (r *restBedrockModels) Delete(ctx context.Context, id string) error {
	return deleteModelByID(ctx, r.c, id)
}
