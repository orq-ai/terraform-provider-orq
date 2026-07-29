package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// modelDocJSON is the ModelDocument shape every model read/write returns. api_key
// is absent because the server never echoes the secret.
func modelDocJSON(id, displayName string) map[string]any {
	return map[string]any{
		"id":           id,
		"display_name": displayName,
		"model_id":     "liquid/lfm2.5-1.2b",
		"model_type":   "chat",
		"description":  "a custom model",
		// A custom openai-like model: provider "openailike", owner = workspace id.
		"provider": "openailike",
		"owner":    "ws_1",
		"enabled":  true,
		"configuration": map[string]any{
			"provider":             "openailike",
			"base_url":             "http://host.docker.internal:1234/v1",
			"is_openai_compatible": true,
			"api_key_env":          nil,
			// No "region": openai-like models leave configuration.region unset.
		},
		"metadata": map[string]any{
			"is_private":            true,
			"region":                "europe", // authoritative region location
			"supports_vision":       true,
			"supports_tool_calling": false,
		},
		"created":     "2020-01-01T00:00:00Z",
		"updated":     "2020-01-02T00:00:00Z",
		"input_cost":  0.5,
		"output_cost": 1.5,
	}
}

func newModelServer(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := New(Config{URL: srv.URL, Token: sentinelToken})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestModels_CreateSendsAPIKey(t *testing.T) {
	var gotBody map[string]any
	var gotPath, gotMethod string
	c := newModelServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(modelDocJSON("mdl_uuid_1", "tf model"))
	})

	m, err := c.Models().Create(context.Background(), ModelCreateInput{
		APIKey:      "sk-secret-123",
		BaseURL:     "http://host.docker.internal:1234/v1",
		DisplayName: "tf model",
		ModelID:     "liquid/lfm2.5-1.2b",
		ModelType:   "chat",
		Region:      "europe",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v2/models/openai-like" {
		t.Errorf("wrong request: %s %s", gotMethod, gotPath)
	}
	if gotBody["api_key"] != "sk-secret-123" {
		t.Errorf("api_key must be sent on create, got %v", gotBody["api_key"])
	}
	if gotBody["base_url"] != "http://host.docker.internal:1234/v1" || gotBody["region"] != "europe" {
		t.Errorf("required fields not sent: %+v", gotBody)
	}
	if m.ID != "mdl_uuid_1" || m.DisplayName != "tf model" || m.ModelID != "liquid/lfm2.5-1.2b" || m.ModelType != "chat" {
		t.Errorf("read-back wrong: %+v", m)
	}
	if m.BaseURL != "http://host.docker.internal:1234/v1" || m.Region != "europe" {
		t.Errorf("base_url/region not mapped (base from configuration, region from metadata): base=%q region=%q", m.BaseURL, m.Region)
	}
	if m.Created == "" || m.Updated == "" {
		t.Errorf("timestamps not mapped: created=%q updated=%q", m.Created, m.Updated)
	}
}

// There is no single-GET route, so the read path lists and filters.
func TestModels_GetFiltersByID(t *testing.T) {
	var gotPath, gotMethod string
	c := newModelServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			modelDocJSON("other", "other model"),
			modelDocJSON("mdl_uuid_1", "tf model"),
		})
	})
	m, err := c.Models().Get(context.Background(), "mdl_uuid_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/v2/models" {
		t.Errorf("wrong request: %s %s", gotMethod, gotPath)
	}
	if m.ID != "mdl_uuid_1" || m.DisplayName != "tf model" || m.BaseURL == "" || m.Region != "europe" {
		t.Errorf("filtered read-back wrong: %+v", m)
	}
}

// The list applies scope + visibility filters, so a miss may mean the model is
// invisible to this credential rather than gone. Dropping state on a miss would
// make the next apply create a DUPLICATE.
func TestModels_GetListMissIsError(t *testing.T) {
	c := newModelServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{modelDocJSON("someone_else", "x")})
	})
	_, err := c.Models().Get(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected an error for a list-miss, got nil")
	}
	if CodeOf(err) == CodeNotFound {
		t.Fatalf("a list-miss must NOT be not_found (Read would drop state → duplicate on apply), got %v", err)
	}
	if strings.Contains(err.Error(), "/v2/models") {
		t.Errorf("error leaks REST route: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "terraform state rm") {
		t.Errorf("error should guide the operator on genuine deletion, got %q", err.Error())
	}
}

// A 404 from the COLLECTION endpoint cannot say one id is gone; only Delete's own
// /:id route is authoritative for not_found.
func TestModels_GetList404NotAuthoritative(t *testing.T) {
	c := newModelServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := c.Models().Get(context.Background(), "mdl_1")
	if err == nil {
		t.Fatal("expected an error for a list-endpoint 404, got nil")
	}
	if CodeOf(err) == CodeNotFound {
		t.Fatalf("a list-endpoint 404 must NOT be not_found (Read would drop state → duplicate on apply), got %v", err)
	}
	if strings.Contains(err.Error(), "/v2/models") {
		t.Errorf("error leaks REST route: %q", err.Error())
	}
}

func TestModels_RegionFromMetadata(t *testing.T) {
	t.Run("metadata.region wins over configuration.region", func(t *testing.T) {
		doc := modelDocJSON("mdl_1", "m")
		doc["metadata"].(map[string]any)["region"] = "us"
		doc["configuration"].(map[string]any)["region"] = "should-be-ignored"
		c := newModelServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{doc})
		})
		m, err := c.Models().Get(context.Background(), "mdl_1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if m.Region != "us" {
			t.Errorf("region must come from metadata.region, got %q", m.Region)
		}
	})
	t.Run("configuration.region fallback when metadata omits region", func(t *testing.T) {
		doc := modelDocJSON("mdl_1", "m")
		delete(doc["metadata"].(map[string]any), "region")
		doc["configuration"].(map[string]any)["region"] = "apac"
		c := newModelServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{doc})
		})
		m, err := c.Models().Get(context.Background(), "mdl_1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if m.Region != "apac" {
			t.Errorf("region must fall back to configuration.region, got %q", m.Region)
		}
	})
}

// The update endpoint cannot rotate the secret.
func TestModels_UpdateOmitsAPIKey(t *testing.T) {
	var gotBody map[string]any
	var gotPath, gotMethod string
	c := newModelServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(modelDocJSON("mdl_uuid_1", "renamed"))
	})
	m, err := c.Models().Update(context.Background(), ModelUpdateInput{
		ID:          "mdl_uuid_1",
		BaseURL:     "http://host.docker.internal:1234/v1",
		DisplayName: "renamed",
		ModelID:     "liquid/lfm2.5-1.2b",
		ModelType:   "chat",
		Region:      "europe",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if gotMethod != http.MethodPatch || gotPath != "/v2/models/openai-like/mdl_uuid_1" {
		t.Errorf("wrong request: %s %s", gotMethod, gotPath)
	}
	if _, present := gotBody["api_key"]; present {
		t.Errorf("update body must NOT contain api_key, got %v", gotBody["api_key"])
	}
	if gotBody["display_name"] != "renamed" || gotBody["model_type"] != "chat" || gotBody["region"] != "europe" {
		t.Errorf("required update fields missing: %+v", gotBody)
	}
	if gotBody["model_id"] != "liquid/lfm2.5-1.2b" || gotBody["base_url"] != "http://host.docker.internal:1234/v1" {
		t.Errorf("model_id/base_url must be sent on update (mutable in place): %+v", gotBody)
	}
	if m.DisplayName != "renamed" {
		t.Errorf("update read-back wrong: %+v", m)
	}
}

func TestModels_DecodesMarkerAndRefreshFields(t *testing.T) {
	c := newModelServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{modelDocJSON("mdl_uuid_1", "tf model")})
	})
	m, err := c.Models().Get(context.Background(), "mdl_uuid_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.Provider != ModelProviderOpenAILike {
		t.Errorf("provider marker not decoded: %q", m.Provider)
	}
	if m.Owner != "ws_1" {
		t.Errorf("owner not decoded: %q", m.Owner)
	}
	if m.InputCost == nil || *m.InputCost != 0.5 || m.OutputCost == nil || *m.OutputCost != 1.5 {
		t.Errorf("costs not decoded: in=%v out=%v", m.InputCost, m.OutputCost)
	}
	if m.SupportsVision == nil || !*m.SupportsVision {
		t.Errorf("supports_vision not decoded: %v", m.SupportsVision)
	}
	if m.SupportsToolCalling == nil || *m.SupportsToolCalling {
		t.Errorf("supports_tool_calling not decoded as false: %v", m.SupportsToolCalling)
	}
	// A field the server omits stays nil, and stays config-authoritative.
	if m.CostPerImage != nil || m.SupportsImageEdit != nil {
		t.Errorf("omitted fields must stay nil: cost_per_image=%v image_edit=%v", m.CostPerImage, m.SupportsImageEdit)
	}
}

// modelDocRef is a minimal list item for exercising Resolve's id-vs-ref rules.
func modelDocRef(id, refID string) map[string]any {
	return map[string]any{
		"id":           id,
		"refId":        refID,
		"display_name": "M",
		"model_id":     "gpt-4o",
		"model_type":   "chat",
		"provider":     "openai",
		"owner":        "system",
		"enabled":      true,
		"created":      "2020-01-01T00:00:00Z",
		"updated":      "2020-01-02T00:00:00Z",
	}
}

func newModelListServer(t *testing.T, docs []map[string]any) *Client {
	t.Helper()
	return newModelServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(docs)
	})
}

// The adversarial case: one document's slug-shaped id equals another's ref_id, so
// "contains a slash" must never discriminate a ref from an id.
func TestModels_ResolveExactIDWins(t *testing.T) {
	// docA's id is slug-shaped and equals docB's ref_id.
	c := newModelListServer(t, []map[string]any{
		modelDocRef("openai/gpt-4o", "some/other-ref"), // id == the value we resolve
		modelDocRef("uuid-B", "openai/gpt-4o"),         // ref_id == the value we resolve
	})
	m, err := c.Models().Resolve(context.Background(), "openai/gpt-4o")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if m.ID != "openai/gpt-4o" {
		t.Errorf("id match must win over a ref_id match, got id=%q", m.ID)
	}
}

func TestModels_ResolveUniqueRef(t *testing.T) {
	c := newModelListServer(t, []map[string]any{
		modelDocRef("uuid-1", "anthropic/claude"),
		modelDocRef("uuid-2", "openai/gpt-4o"),
	})
	m, err := c.Models().Resolve(context.Background(), "openai/gpt-4o")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if m.ID != "uuid-2" {
		t.Errorf("expected the doc whose ref_id matches, got id=%q", m.ID)
	}
	if m.RefID != "openai/gpt-4o" {
		t.Errorf("RefID not projected: %q", m.RefID)
	}
}

func TestModels_ResolveAmbiguousRef(t *testing.T) {
	c := newModelListServer(t, []map[string]any{
		modelDocRef("uuid-A", "openai/gpt-4o"),
		modelDocRef("uuid-B", "openai/gpt-4o"),
	})
	_, err := c.Models().Resolve(context.Background(), "openai/gpt-4o")
	if err == nil {
		t.Fatal("expected an ambiguity error, got nil")
	}
	if CodeOf(err) == CodeNotFound {
		t.Fatalf("ambiguity must not be not_found, got %v", err)
	}
	if !strings.Contains(err.Error(), "uuid-A") || !strings.Contains(err.Error(), "uuid-B") {
		t.Errorf("ambiguity error must name the candidate ids, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "id") {
		t.Errorf("ambiguity error should tell the caller to use the document id, got %q", err.Error())
	}
}

func TestModels_ResolveZeroMatch(t *testing.T) {
	c := newModelListServer(t, []map[string]any{
		modelDocRef("uuid-1", "openai/gpt-4o"),
	})
	_, err := c.Models().Resolve(context.Background(), "does/not-exist")
	if err == nil {
		t.Fatal("expected a not-found error, got nil")
	}
	if CodeOf(err) != CodeNotFound {
		t.Errorf("zero-match should be not-found-style, got %v (%v)", CodeOf(err), err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "document id") || !strings.Contains(msg, "ref_id") {
		t.Errorf("error must mention BOTH interpretations (document id and ref_id), got %q", msg)
	}
	if !strings.Contains(msg, "GET /v2/models") {
		t.Errorf("error should point at GET /v2/models, got %q", msg)
	}
}

func TestModels_ResolveList404NotAuthoritative(t *testing.T) {
	c := newModelServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := c.Models().Resolve(context.Background(), "openai/gpt-4o")
	if err == nil {
		t.Fatal("expected an error for a list-endpoint 404, got nil")
	}
	if CodeOf(err) == CodeNotFound {
		t.Fatalf("a list-endpoint 404 during Resolve must NOT be not_found, got %v", err)
	}
}

func TestModels_Delete(t *testing.T) {
	var gotPath, gotMethod string
	c := newModelServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		w.WriteHeader(http.StatusOK)
	})
	if err := c.Models().Delete(context.Background(), "mdl_uuid_1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/v2/models/mdl_uuid_1" {
		t.Errorf("wrong request: %s %s", gotMethod, gotPath)
	}
}

// A genuine delete replies 200 with an EMPTY body; a REFUSED one replies 200 with
// a JSON `{"message": ...}` body and leaves the model intact.
func TestModels_DeleteRefusedByServer(t *testing.T) {
	t.Run("200-with-message refusal returns a conflict error", func(t *testing.T) {
		c := newModelServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message": "The model is being used in 3 experiments and cannot be deleted. Try disabling it instead.",
			})
		})
		err := c.Models().Delete(context.Background(), "mdl_uuid_1")
		if err == nil {
			t.Fatal("expected an error for a refused delete, got nil (model would be orphaned)")
		}
		if CodeOf(err) != CodeConflict {
			t.Errorf("expected conflict code, got %q (%v)", CodeOf(err), err)
		}
		if !strings.Contains(err.Error(), "experiments") {
			t.Errorf("error should surface the server's refusal message, got %q", err.Error())
		}
	})

	t.Run("200 with an empty body is a genuine delete", func(t *testing.T) {
		c := newModelServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK) // TS parity: 200 + empty body on success
		})
		if err := c.Models().Delete(context.Background(), "mdl_uuid_1"); err != nil {
			t.Fatalf("genuine delete must succeed, got %v", err)
		}
	})
}
