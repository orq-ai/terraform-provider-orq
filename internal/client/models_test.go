package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// modelDocJSON is the ModelDocument shape the server returns for a custom
// openai-like model (create/update body and list item share it). base_url lives
// under `configuration`; region lives under `metadata` (an openai-like model does
// NOT populate configuration.region — buildOpenAILikeMetadata stores it on
// metadata.region). api_key is deliberately absent (the server never echoes the
// secret). input_cost/output_cost are always present (no omitempty); the metadata
// capability/cost fields are omitempty and drop when false/0.
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
			// NOTE: no "region" here — openai-like models leave configuration.region unset.
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

// TestModels_CreateSendsAPIKey proves the create hits POST
// /v2/models/openai-like, sends the secret api_key, and maps the (api-key-less)
// response — including configuration.base_url + metadata.region — back out.
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

// TestModels_GetFiltersByID proves the read path lists /v2/models and returns
// the matching id (there is no single-GET route).
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

// TestModels_GetListMissIsError proves an id ABSENT from the list is NOT treated
// as deleted. There is no GET-by-id route (only the LIST endpoint) and the list
// applies workspace/project scope + visibility filters, so a miss may mean the
// model is invisible to this credential rather than gone. Get must therefore
// return a NON-not_found error so Read surfaces it instead of dropping state
// (which would make the next apply create a DUPLICATE); the message must guide the
// operator and must not leak the REST route.
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

// TestModels_GetAuthoritative404 proves that — in contrast to a list-miss — an
// authoritative 404 from the endpoint IS normalized to not_found. That is the ONLY
// signal treated as "genuinely gone" (so Read may drop the resource / Delete treats
// it as done).
func TestModels_GetAuthoritative404(t *testing.T) {
	c := newModelServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := c.Models().Get(context.Background(), "mdl_1")
	if err == nil || CodeOf(err) != CodeNotFound {
		t.Fatalf("an authoritative 404 must normalize to not_found, got %v", err)
	}
}

// TestModels_RegionFromMetadata proves region is read from metadata.region — the
// authoritative location for a custom openai-like model — with configuration.region
// only as a fallback (openai-like leaves configuration.region unset).
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

// TestModels_UpdateOmitsAPIKey is the mutability proof: the update hits PATCH
// /v2/models/openai-like/{id}, carries the mutable fields (including model_id),
// and NEVER sends api_key (the endpoint cannot rotate it).
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

// TestModels_DecodesMarkerAndRefreshFields proves the client projects the
// custom-vs-system marker (provider/owner) and the refreshed list fields
// (input/output cost + supports_*) so the resource can guard imports and detect
// drift. A field the server omits stays nil (distinguishable from a real value).
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
	// A field the server omits stays nil (kept config-authoritative by the resource).
	if m.CostPerImage != nil || m.SupportsImageEdit != nil {
		t.Errorf("omitted fields must stay nil: cost_per_image=%v image_edit=%v", m.CostPerImage, m.SupportsImageEdit)
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

// TestModels_DeleteRefusedByServer proves the delete client distinguishes a
// genuine delete (200, empty body) from a REFUSED delete (200 with a JSON
// `{"message": ...}` body — the model is left intact, see
// apps/platform-api/models/delete.go:45,53-54,99-100). A refusal must surface as
// a typed conflict so the resource keeps the still-live model in state rather
// than silently orphaning it; a genuine (empty-body) delete must still succeed.
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
