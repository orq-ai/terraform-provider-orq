package client

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// bedrockDocJSON is the ModelDocument shape a Bedrock read/write returns. Note
// the ABSENT assume_role_arn / assume_role_external_id: the server strips both
// from every response (ModelConfigurationResponse omits them).
func bedrockDocJSON(id, authMode string) map[string]any {
	cfg := map[string]any{
		"provider":              "aws",
		"region":                "eu-central-1",
		"inference_profile_arn": "arn:aws:bedrock:eu-central-1:123456789012:application-inference-profile/abc123",
		"auth_mode":             authMode,
	}
	if authMode == BedrockAuthModeIntegration {
		cfg["integration_id"] = "int_1"
	}
	return map[string]any{
		"id":              id,
		"display_name":    "tf bedrock",
		"model_id":        "arn:aws:bedrock:eu-central-1:123456789012:application-inference-profile/abc123",
		"model_type":      "chat",
		"model_developer": "anthropic",
		"model_family":    "claude",
		"description":     "a bedrock model",
		"provider":        "aws",
		"owner":           "ws_1",
		"enabled":         true,
		"has_functions":   true,
		"configuration":   cfg,
		"metadata": map[string]any{
			"is_private":            true,
			"supports_tool_calling": true,
			"supports_vision":       true,
			// supports_strict_tool / json mode / reasoning omitted: they are false.
		},
		"created":     "2020-01-01T00:00:00Z",
		"updated":     "2020-01-02T00:00:00Z",
		"input_cost":  0.003,
		"output_cost": 0.015,
	}
}

func TestBedrockModels_CreatePodIdentity(t *testing.T) {
	var gotBody map[string]any
	var gotPath, gotMethod string
	c := newModelServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(bedrockDocJSON("mdl_1", BedrockAuthModePodIdentity))
	})

	roleArn := "arn:aws:iam::123456789012:role/bedrock-access"
	externalID := "ext-1"
	m, err := c.BedrockModels().Create(context.Background(), BedrockModelCreateInput{
		DisplayName:          "tf bedrock",
		ModelID:              "arn:aws:bedrock:eu-central-1:123456789012:application-inference-profile/abc123",
		Region:               "eu-central-1",
		ModelDeveloper:       "anthropic",
		AuthMode:             BedrockAuthModePodIdentity,
		AssumeRoleArn:        &roleArn,
		AssumeRoleExternalID: &externalID,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v2/models/aws-bedrock" {
		t.Errorf("wrong request: %s %s", gotMethod, gotPath)
	}
	if gotBody["auth_mode"] != BedrockAuthModePodIdentity {
		t.Errorf("auth_mode not sent: %v", gotBody["auth_mode"])
	}
	if gotBody["assume_role_arn"] != roleArn || gotBody["assume_role_external_id"] != externalID {
		t.Errorf("assume-role fields not sent: %+v", gotBody)
	}
	if _, present := gotBody["integration_id"]; present {
		t.Errorf("integration_id must be omitted when unset, got %+v", gotBody)
	}
	if m.AuthMode != BedrockAuthModePodIdentity || m.IntegrationID != "" {
		t.Errorf("auth mode read-back wrong: %+v", m)
	}
	if !m.IsBedrock() {
		t.Errorf("a pod-identity bedrock model must satisfy IsBedrock: %+v", m)
	}
}

func TestBedrockModels_CreateIntegration(t *testing.T) {
	var gotBody map[string]any
	c := newModelServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(bedrockDocJSON("mdl_1", BedrockAuthModeIntegration))
	})

	integrationID := "int_1"
	m, err := c.BedrockModels().Create(context.Background(), BedrockModelCreateInput{
		DisplayName:    "tf bedrock",
		ModelID:        "arn:aws:bedrock:eu-central-1:123456789012:application-inference-profile/abc123",
		Region:         "eu-central-1",
		ModelDeveloper: "anthropic",
		AuthMode:       BedrockAuthModeIntegration,
		IntegrationID:  &integrationID,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if gotBody["integration_id"] != integrationID {
		t.Errorf("integration_id not sent: %+v", gotBody)
	}
	if _, present := gotBody["assume_role_arn"]; present {
		t.Errorf("assume_role_arn must be omitted when unset, got %+v", gotBody)
	}
	if m.IntegrationID != integrationID || m.AuthMode != BedrockAuthModeIntegration {
		t.Errorf("integration read-back wrong: %+v", m)
	}
}

func TestBedrockModels_GetFiltersAndMaps(t *testing.T) {
	var gotPath, gotMethod string
	c := newModelServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			modelDocJSON("other", "an openai-like model"),
			bedrockDocJSON("mdl_1", BedrockAuthModeIntegration),
		})
	})
	m, err := c.BedrockModels().Get(context.Background(), "mdl_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/v2/models" {
		t.Errorf("wrong request: %s %s", gotMethod, gotPath)
	}
	if m.Region != "eu-central-1" || m.ModelDeveloper != "anthropic" || m.ModelFamily != "claude" {
		t.Errorf("identity fields not mapped: %+v", m)
	}
	if m.InferenceProfileArn == "" || m.AuthMode != BedrockAuthModeIntegration || m.IntegrationID != "int_1" {
		t.Errorf("configuration not mapped: %+v", m)
	}
	if m.InputCost == nil || *m.InputCost != 0.003 || m.OutputCost == nil || *m.OutputCost != 0.015 {
		t.Errorf("costs not mapped: %+v", m)
	}
	if m.SupportsToolCalling == nil || !*m.SupportsToolCalling || !m.HasFunctions {
		t.Errorf("tool-calling flags not mapped: %+v", m)
	}
	if m.SupportsStrictTool != nil || m.HasReasoning != nil {
		t.Error("a metadata field the server OMITS must stay nil so the resource can retain the prior value")
	}
}

// A list-miss must NOT be not_found: Read would drop state and the next apply
// would create a duplicate.
func TestBedrockModels_GetListMissIsNotNotFound(t *testing.T) {
	c := newModelServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{bedrockDocJSON("someone_else", BedrockAuthModePodIdentity)})
	})
	_, err := c.BedrockModels().Get(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected an error for a list-miss, got nil")
	}
	if CodeOf(err) == CodeNotFound {
		t.Fatalf("a list-miss must NOT be not_found, got %v", err)
	}
}

func TestBedrockModels_UpdateOmitsImmutableFields(t *testing.T) {
	var gotBody map[string]any
	var gotPath, gotMethod string
	c := newModelServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(bedrockDocJSON("mdl_1", BedrockAuthModePodIdentity))
	})

	roleArn := "arn:aws:iam::123456789012:role/other"
	if _, err := c.BedrockModels().Update(context.Background(), BedrockModelUpdateInput{
		ID:             "mdl_1",
		DisplayName:    "renamed",
		ModelID:        "arn:aws:bedrock:eu-central-1:123456789012:application-inference-profile/def456",
		Region:         "eu-west-1",
		ModelDeveloper: "anthropic",
		AssumeRoleArn:  &roleArn,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if gotMethod != http.MethodPatch || gotPath != "/v2/models/aws-bedrock/mdl_1" {
		t.Errorf("wrong request: %s %s", gotMethod, gotPath)
	}
	for _, k := range []string{"auth_mode", "integration_id", "model_type"} {
		if _, present := gotBody[k]; present {
			t.Errorf("%s is not patchable and must never be sent, got %+v", k, gotBody)
		}
	}
	if gotBody["display_name"] != "renamed" || gotBody["region"] != "eu-west-1" {
		t.Errorf("patchable fields not sent: %+v", gotBody)
	}
	if gotBody["assume_role_arn"] != roleArn {
		t.Errorf("assume_role_arn not sent: %+v", gotBody)
	}
}

func TestBedrockModels_DeleteUsesSharedRoute(t *testing.T) {
	var gotPath, gotMethod string
	c := newModelServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		w.WriteHeader(http.StatusOK)
	})
	if err := c.BedrockModels().Delete(context.Background(), "mdl_1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/v2/models/mdl_1" {
		t.Errorf("wrong request: %s %s", gotMethod, gotPath)
	}
}

// A 200 carrying a `message` body is a REFUSED delete, not a success.
func TestBedrockModels_DeleteRefusal(t *testing.T) {
	c := newModelServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "used in 3 experiments"})
	})
	err := c.BedrockModels().Delete(context.Background(), "mdl_1")
	if err == nil || CodeOf(err) != CodeConflict {
		t.Fatalf("expected a conflict for a refused delete, got %v", err)
	}
}

func TestBedrockModel_IsBedrock(t *testing.T) {
	arn := "arn:aws:bedrock:eu-central-1:1:inference-profile/x"
	cases := []struct {
		name string
		m    BedrockModel
		want bool
	}{
		{"pod-identity bedrock", BedrockModel{Provider: "aws", InferenceProfileArn: arn, AuthMode: BedrockAuthModePodIdentity}, true},
		{"integration bedrock", BedrockModel{Provider: "aws", InferenceProfileArn: arn, AuthMode: BedrockAuthModeIntegration}, true},
		{"legacy aws access-key model", BedrockModel{Provider: "aws"}, false},
		{"aws model without auth mode", BedrockModel{Provider: "aws", InferenceProfileArn: arn}, false},
		{"openai-like custom model", BedrockModel{Provider: "openailike"}, false},
		{"system model", BedrockModel{Provider: "openai", Owner: "system"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.m.IsBedrock(); got != tc.want {
				t.Errorf("IsBedrock() = %v, want %v", got, tc.want)
			}
		})
	}
}
