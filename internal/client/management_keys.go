package client

import (
	"context"
	"fmt"

	managementkeysv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/managementkeys/v1"
	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
)

// Management-key permission presets and access levels are surfaced as the FULL
// proto enum names (the wire form the server (un)marshals and fails closed on).
// Unlike budget/notifier — whose schema uses prefix-stripped names — these keep
// the full name because plan §5 pins the contract to the full string set and the
// Connect proto enum maps key directly on them, making conversion exact.
const (
	ManagementPermissionModeAll        = "MANAGEMENT_PERMISSION_MODE_ALL"
	ManagementPermissionModeRestricted = "MANAGEMENT_PERMISSION_MODE_RESTRICTED"
	ManagementPermissionModeReadOnly   = "MANAGEMENT_PERMISSION_MODE_READ_ONLY"

	AccessLevelNone  = "ACCESS_LEVEL_NONE"
	AccessLevelRead  = "ACCESS_LEVEL_READ"
	AccessLevelWrite = "ACCESS_LEVEL_WRITE"
)

// ManagementKey is the transport-agnostic projection of a management key.
type ManagementKey struct {
	ID             string
	Name           string
	PermissionMode string
	// Access maps a catalog domain id to a full ACCESS_LEVEL_* name. Populated
	// only when PermissionMode is RESTRICTED.
	Access      map[string]string
	TokenPrefix string
	Status      string
	ExpiresAt   string // RFC 3339; "" when the key never expires
	CreatedAt   string
	UpdatedAt   string
	LastUsedAt  string
}

// ManagementKeyPage is one page of a cursor-paginated list.
type ManagementKeyPage struct {
	Keys    []ManagementKey
	HasMore bool
}

// ManagementKeyCreateInput carries the fields for a create. An empty
// PermissionMode is coerced to ALL by Create (the server rejects UNSPECIFIED on
// create rather than defaulting it).
type ManagementKeyCreateInput struct {
	Name           string
	PermissionMode string
	Access         map[string]string
	ExpiresAt      string
}

// ManagementKeyUpdateInput is a sparse patch: nil pointers leave a field
// unchanged. Access is always reconciled to the supplied map (empty clears it).
type ManagementKeyUpdateInput struct {
	ID             string
	Name           *string
	PermissionMode *string
	Access         map[string]string
	ExpiresAt      string
	ClearExpiresAt bool
}

// ManagementKeyCreateResult pairs the created key with its one-time raw token.
type ManagementKeyCreateResult struct {
	Key ManagementKey
	// Token is the raw sk-orq-... secret, returned ONCE on create and never
	// again. The resource stores it as a sensitive attribute.
	Token string
}

// ManagementKeysAPI is the per-resource seam for the management-keys domain
// (Connect-backed).
type ManagementKeysAPI interface {
	List(ctx context.Context, params ListParams) (*ManagementKeyPage, error)
	Get(ctx context.Context, id string) (*ManagementKey, error)
	Create(ctx context.Context, in ManagementKeyCreateInput) (*ManagementKeyCreateResult, error)
	Update(ctx context.Context, in ManagementKeyUpdateInput) (*ManagementKey, error)
	Delete(ctx context.Context, id string) error
}

type connectManagementKeys struct {
	c platformv1connect.ManagementKeysServiceClient
}

func mgmtPermissionModeToProto(name string) (platformv1.ManagementPermissionMode, error) {
	if name == "" {
		return platformv1.ManagementPermissionMode_MANAGEMENT_PERMISSION_MODE_UNSPECIFIED, nil
	}
	v, ok := platformv1.ManagementPermissionMode_value[name]
	if !ok {
		return 0, &Error{Code: CodeInvalid, Message: fmt.Sprintf("unknown management permission mode %q", name)}
	}
	return platformv1.ManagementPermissionMode(v), nil
}

func mgmtPermissionModeFromProto(m platformv1.ManagementPermissionMode) string {
	if m == platformv1.ManagementPermissionMode_MANAGEMENT_PERMISSION_MODE_UNSPECIFIED {
		return ""
	}
	return m.String()
}

func mgmtStatusFromProto(s platformv1.ManagementKeyStatus) string {
	if s == platformv1.ManagementKeyStatus_MANAGEMENT_KEY_STATUS_UNSPECIFIED {
		return ""
	}
	return s.String()
}

func mgmtAccessToProto(m map[string]string) (map[string]managementkeysv1.AccessLevel, error) {
	if len(m) == 0 {
		return nil, nil
	}
	out := make(map[string]managementkeysv1.AccessLevel, len(m))
	for k, v := range m {
		lv, ok := managementkeysv1.AccessLevel_value[v]
		if !ok {
			return nil, &Error{Code: CodeInvalid, Message: fmt.Sprintf("unknown access level %q for domain %q", v, k)}
		}
		out[k] = managementkeysv1.AccessLevel(lv)
	}
	return out, nil
}

func mgmtAccessFromProto(m map[string]managementkeysv1.AccessLevel) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v.String()
	}
	return out
}

func managementKeyFromProto(k *platformv1.ManagementKey) ManagementKey {
	return ManagementKey{
		ID:             k.GetManagementKeyId(),
		Name:           k.GetName(),
		PermissionMode: mgmtPermissionModeFromProto(k.GetPermissionMode()),
		Access:         mgmtAccessFromProto(k.GetAccess()),
		TokenPrefix:    k.GetTokenPrefix(),
		Status:         mgmtStatusFromProto(k.GetStatus()),
		ExpiresAt:      formatTimestamp(k.GetExpiresAt()),
		CreatedAt:      formatTimestamp(k.GetCreatedAt()),
		UpdatedAt:      formatTimestamp(k.GetUpdatedAt()),
		LastUsedAt:     formatTimestamp(k.GetLastUsedAt()),
	}
}

func (c *connectManagementKeys) List(ctx context.Context, params ListParams) (*ManagementKeyPage, error) {
	req := &platformv1.ListManagementKeysRequest{}
	if params.Limit > 0 {
		req.Limit = &params.Limit
	}
	if params.StartingAfter != "" {
		req.StartingAfter = params.StartingAfter
	}
	resp, err := c.c.ListManagementKeys(ctx, req)
	if err != nil {
		return nil, mapConnectError("management key", err)
	}
	out := &ManagementKeyPage{HasMore: resp.GetHasMore()}
	for _, k := range resp.GetData() {
		out.Keys = append(out.Keys, managementKeyFromProto(k))
	}
	return out, nil
}

func (c *connectManagementKeys) Get(ctx context.Context, id string) (*ManagementKey, error) {
	resp, err := c.c.GetManagementKey(ctx, &platformv1.GetManagementKeyRequest{ManagementKeyId: id})
	if err != nil {
		return nil, mapConnectError("management key", err)
	}
	k := managementKeyFromProto(resp.GetManagementKey())
	return &k, nil
}

func (c *connectManagementKeys) Create(ctx context.Context, in ManagementKeyCreateInput) (*ManagementKeyCreateResult, error) {
	// Defensively coerce an omitted permission mode to ALL. The server rejects
	// the zero UNSPECIFIED enum on create (management-keys/connect_routes.go —
	// fails closed rather than defaulting), so an empty mode must never reach the
	// wire as UNSPECIFIED. The schema also defaults this to ALL, so this is a
	// belt-and-suspenders guard for any non-schema caller.
	if in.PermissionMode == "" {
		in.PermissionMode = ManagementPermissionModeAll
	}
	mode, err := mgmtPermissionModeToProto(in.PermissionMode)
	if err != nil {
		return nil, err
	}
	access, err := mgmtAccessToProto(in.Access)
	if err != nil {
		return nil, err
	}
	req := &platformv1.CreateManagementKeyRequest{
		Name:           in.Name,
		PermissionMode: mode,
		Access:         access,
	}
	if in.ExpiresAt != "" {
		ts, err := parseTimestamp(in.ExpiresAt)
		if err != nil {
			return nil, &Error{Code: CodeInvalid, Message: "invalid expires_at: " + err.Error()}
		}
		req.ExpiresAt = ts
	}
	resp, err := c.c.CreateManagementKey(ctx, req)
	if err != nil {
		return nil, mapConnectError("management key", err)
	}
	return &ManagementKeyCreateResult{
		Key:   managementKeyFromProto(resp.GetManagementKey()),
		Token: resp.GetToken(),
	}, nil
}

func (c *connectManagementKeys) Update(ctx context.Context, in ManagementKeyUpdateInput) (*ManagementKey, error) {
	req := &platformv1.UpdateManagementKeyRequest{ManagementKeyId: in.ID}
	if in.Name != nil {
		req.Name = in.Name
	}
	if in.PermissionMode != nil {
		mode, err := mgmtPermissionModeToProto(*in.PermissionMode)
		if err != nil {
			return nil, err
		}
		req.PermissionMode = &mode
	}
	access, err := mgmtAccessToProto(in.Access)
	if err != nil {
		return nil, err
	}
	req.Access = access
	if in.ClearExpiresAt {
		req.ClearExpiresAt = true
	} else if in.ExpiresAt != "" {
		ts, err := parseTimestamp(in.ExpiresAt)
		if err != nil {
			return nil, &Error{Code: CodeInvalid, Message: "invalid expires_at: " + err.Error()}
		}
		req.ExpiresAt = ts
	}
	resp, err := c.c.UpdateManagementKey(ctx, req)
	if err != nil {
		return nil, mapConnectError("management key", err)
	}
	k := managementKeyFromProto(resp.GetManagementKey())
	return &k, nil
}

func (c *connectManagementKeys) Delete(ctx context.Context, id string) error {
	_, err := c.c.DeleteManagementKey(ctx, &platformv1.DeleteManagementKeyRequest{ManagementKeyId: id})
	return mapConnectError("management key", err)
}
