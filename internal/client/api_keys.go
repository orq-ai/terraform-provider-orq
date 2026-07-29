package client

import (
	"context"
	"fmt"

	apikeysv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/apikeys/v1"
	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
)

// API-key permission presets are surfaced as the FULL proto enum names (the wire
// form the server (un)marshals). Access-level values reuse the shared
// ACCESS_LEVEL_* names (see management_keys.go); the api-keys catalog enum has
// the same value set.
const (
	PermissionModeAll        = "PERMISSION_MODE_ALL"
	PermissionModeRestricted = "PERMISSION_MODE_RESTRICTED"
	PermissionModeReadOnly   = "PERMISSION_MODE_READ_ONLY"
)

// APIKey is the transport-agnostic projection of an API key.
type APIKey struct {
	ID             string
	Name           string
	PermissionMode string
	// AllProjects is true when the key is scoped to all projects; ProjectID is
	// the single project id otherwise ("" when AllProjects).
	AllProjects bool
	ProjectID   string
	// Access maps a catalog domain id to a full ACCESS_LEVEL_* name. Populated
	// only when PermissionMode is RESTRICTED.
	Access      map[string]string
	TokenPrefix string
	Status      string
	ExpiresAt   string
	CreatedAt   string
	UpdatedAt   string
	LastUsedAt  string
}

// APIKeyPage is one page of a cursor-paginated list.
type APIKeyPage struct {
	Keys    []APIKey
	HasMore bool
}

// APIKeyCreateInput carries the fields for a create. ProjectID selects a
// single-project scope; empty means all-projects (the server default). Owner is
// always service_account (workspace-owned) — the provider does not surface the
// user-owner path.
type APIKeyCreateInput struct {
	Name           string
	ProjectID      string
	PermissionMode string
	Access         map[string]string
	ExpiresAt      string
}

// APIKeyUpdateInput is a sparse patch. ProjectScope is mutable, so ProjectID is
// reconciled: SetProjectScope drives whether project_scope is sent at all.
type APIKeyUpdateInput struct {
	ID             string
	Name           *string
	PermissionMode *string
	Access         map[string]string
	// SetProjectScope reconciles the scope: when true, ProjectID=="" sends
	// all-projects and a non-empty ProjectID sends single-project.
	SetProjectScope bool
	ProjectID       string
	ExpiresAt       string
	ClearExpiresAt  bool
}

// APIKeyCreateResult pairs the created key with its one-time raw token.
type APIKeyCreateResult struct {
	Key APIKey
	// Token is the raw sk-orq-... secret, returned ONCE on create. The resource
	// stores it as a sensitive attribute.
	Token string
}

// APIKeysAPI is the per-resource seam for the api-keys domain (Connect-backed).
type APIKeysAPI interface {
	List(ctx context.Context, params ListParams) (*APIKeyPage, error)
	Get(ctx context.Context, id string) (*APIKey, error)
	Create(ctx context.Context, in APIKeyCreateInput) (*APIKeyCreateResult, error)
	Update(ctx context.Context, in APIKeyUpdateInput) (*APIKey, error)
	Delete(ctx context.Context, id string) error
}

type connectAPIKeys struct {
	c platformv1connect.ApiKeysServiceClient
}

func apiKeyPermissionModeToProto(name string) (platformv1.PermissionMode, error) {
	if name == "" {
		return platformv1.PermissionMode_PERMISSION_MODE_UNSPECIFIED, nil
	}
	v, ok := platformv1.PermissionMode_value[name]
	if !ok {
		return 0, &Error{Code: CodeInvalid, Message: fmt.Sprintf("unknown permission mode %q", name)}
	}
	return platformv1.PermissionMode(v), nil
}

func apiKeyPermissionModeFromProto(m platformv1.PermissionMode) string {
	if m == platformv1.PermissionMode_PERMISSION_MODE_UNSPECIFIED {
		return ""
	}
	return m.String()
}

func apiKeyStatusFromProto(s platformv1.ApiKeyStatus) string {
	if s == platformv1.ApiKeyStatus_API_KEY_STATUS_UNSPECIFIED {
		return ""
	}
	return s.String()
}

func apiKeyAccessToProto(m map[string]string) (map[string]apikeysv1.AccessLevel, error) {
	if len(m) == 0 {
		return nil, nil
	}
	out := make(map[string]apikeysv1.AccessLevel, len(m))
	for k, v := range m {
		lv, ok := apikeysv1.AccessLevel_value[v]
		if !ok {
			return nil, &Error{Code: CodeInvalid, Message: fmt.Sprintf("unknown access level %q for domain %q", v, k)}
		}
		out[k] = apikeysv1.AccessLevel(lv)
	}
	return out, nil
}

func apiKeyAccessFromProto(m map[string]apikeysv1.AccessLevel) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v.String()
	}
	return out
}

// projectScopeToProto builds the ProjectScope oneof. An empty projectID yields
// the all-projects case; a non-empty one the single-project case.
func projectScopeToProto(projectID string) *platformv1.ProjectScope {
	if projectID == "" {
		return &platformv1.ProjectScope{Kind: &platformv1.ProjectScope_All{All: &platformv1.AllProjects{}}}
	}
	return &platformv1.ProjectScope{Kind: &platformv1.ProjectScope_Single{Single: &platformv1.SingleProject{ProjectId: projectID}}}
}

func apiKeyFromProto(k *platformv1.ApiKey) APIKey {
	out := APIKey{
		ID:             k.GetApiKeyId(),
		Name:           k.GetName(),
		PermissionMode: apiKeyPermissionModeFromProto(k.GetPermissionMode()),
		Access:         apiKeyAccessFromProto(k.GetAccess()),
		TokenPrefix:    k.GetTokenPrefix(),
		Status:         apiKeyStatusFromProto(k.GetStatus()),
		ExpiresAt:      formatTimestamp(k.GetExpiresAt()),
		CreatedAt:      formatTimestamp(k.GetCreatedAt()),
		UpdatedAt:      formatTimestamp(k.GetUpdatedAt()),
		LastUsedAt:     formatTimestamp(k.GetLastUsedAt()),
	}
	if s := k.GetProjectScope(); s != nil {
		if single := s.GetSingle(); single != nil {
			out.ProjectID = single.GetProjectId()
		} else {
			out.AllProjects = true
		}
	} else {
		out.AllProjects = true
	}
	return out
}

func (c *connectAPIKeys) List(ctx context.Context, params ListParams) (*APIKeyPage, error) {
	req := &platformv1.ListApiKeysRequest{}
	if params.Limit > 0 {
		req.Limit = &params.Limit
	}
	if params.StartingAfter != "" {
		req.StartingAfter = params.StartingAfter
	}
	resp, err := c.c.ListApiKeys(ctx, req)
	if err != nil {
		return nil, mapConnectError("api key", err)
	}
	out := &APIKeyPage{HasMore: resp.GetHasMore()}
	for _, k := range resp.GetData() {
		out.Keys = append(out.Keys, apiKeyFromProto(k))
	}
	return out, nil
}

func (c *connectAPIKeys) Get(ctx context.Context, id string) (*APIKey, error) {
	resp, err := c.c.GetApiKey(ctx, &platformv1.GetApiKeyRequest{ApiKeyId: id})
	if err != nil {
		return nil, mapConnectError("api key", err)
	}
	k := apiKeyFromProto(resp.GetApiKey())
	return &k, nil
}

func (c *connectAPIKeys) Create(ctx context.Context, in APIKeyCreateInput) (*APIKeyCreateResult, error) {
	// Defensively coerce an omitted permission mode to ALL. The server rejects
	// the zero UNSPECIFIED enum on create (api-keys/connect_routes.go — fails
	// closed rather than defaulting), so an empty mode must never reach the wire
	// as UNSPECIFIED. The schema also defaults this to ALL, so this is a
	// belt-and-suspenders guard for any non-schema caller.
	if in.PermissionMode == "" {
		in.PermissionMode = PermissionModeAll
	}
	mode, err := apiKeyPermissionModeToProto(in.PermissionMode)
	if err != nil {
		return nil, err
	}
	access, err := apiKeyAccessToProto(in.Access)
	if err != nil {
		return nil, err
	}
	req := &platformv1.CreateApiKeyRequest{
		Name:           in.Name,
		PermissionMode: mode,
		Access:         access,
	}
	// Only send a scope for a single-project key; omitting it lets the server
	// default to all-projects (which reads back the same way).
	if in.ProjectID != "" {
		req.ProjectScope = projectScopeToProto(in.ProjectID)
	}
	if in.ExpiresAt != "" {
		ts, err := parseTimestamp(in.ExpiresAt)
		if err != nil {
			return nil, &Error{Code: CodeInvalid, Message: "invalid expires_at: " + err.Error()}
		}
		req.ExpiresAt = ts
	}
	resp, err := c.c.CreateApiKey(ctx, req)
	if err != nil {
		return nil, mapConnectError("api key", err)
	}
	return &APIKeyCreateResult{
		Key:   apiKeyFromProto(resp.GetApiKey()),
		Token: resp.GetToken(),
	}, nil
}

func (c *connectAPIKeys) Update(ctx context.Context, in APIKeyUpdateInput) (*APIKey, error) {
	req := &platformv1.UpdateApiKeyRequest{ApiKeyId: in.ID}
	if in.Name != nil {
		req.Name = in.Name
	}
	if in.PermissionMode != nil {
		mode, err := apiKeyPermissionModeToProto(*in.PermissionMode)
		if err != nil {
			return nil, err
		}
		req.PermissionMode = &mode
	}
	access, err := apiKeyAccessToProto(in.Access)
	if err != nil {
		return nil, err
	}
	req.Access = access
	if in.SetProjectScope {
		req.ProjectScope = projectScopeToProto(in.ProjectID)
	}
	if in.ClearExpiresAt {
		req.ClearExpiresAt = true
	} else if in.ExpiresAt != "" {
		ts, err := parseTimestamp(in.ExpiresAt)
		if err != nil {
			return nil, &Error{Code: CodeInvalid, Message: "invalid expires_at: " + err.Error()}
		}
		req.ExpiresAt = ts
	}
	resp, err := c.c.UpdateApiKey(ctx, req)
	if err != nil {
		return nil, mapConnectError("api key", err)
	}
	k := apiKeyFromProto(resp.GetApiKey())
	return &k, nil
}

func (c *connectAPIKeys) Delete(ctx context.Context, id string) error {
	_, err := c.c.DeleteApiKey(ctx, &platformv1.DeleteApiKeyRequest{ApiKeyId: id})
	return mapConnectError("api key", err)
}
