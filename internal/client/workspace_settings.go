package client

import (
	"context"

	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
)

// WorkspaceSettings is the workspace settings singleton. A workspace IS the
// tenant, so there is no id: the credential selects the workspace.
type WorkspaceSettings struct {
	// Key is read-only: the update request has no key field.
	Key                  string
	DisplayName          string
	EnforceEnabledModels bool
	// PiiRedaction is nil when the workspace never configured the plugin.
	PiiRedaction *PiiRedaction
}

// PiiRedaction is the workspace-default pii_redaction plugin configuration.
type PiiRedaction struct {
	Enabled bool
	// Config is nil when the stored document is an enable flag on its own.
	Config *PiiRedactionConfig
}

// PiiRedactionConfig mirrors the plugin config; a nil pointer/slice means the
// key is absent.
//
// Entities is ALWAYS nil when the server holds no entities: the stored document
// omits the key entirely for an empty list, so "no entities key" and "an empty
// list" do not survive the round trip. The resource layer re-establishes the
// distinction against the operator's config.
type PiiRedactionConfig struct {
	Language  *string
	Entities  []string
	OnFailure *string
	Threshold *float64
}

// WorkspaceSettingsUpdateInput is a PARTIAL update: a nil field is omitted and
// the server leaves the stored value unchanged. PiiRedaction is the exception —
// sending it fully REPLACES the stored object.
type WorkspaceSettingsUpdateInput struct {
	DisplayName          *string
	EnforceEnabledModels *bool
	PiiRedaction         *PiiRedaction
}

// IsEmpty reports whether the input would write nothing at all.
func (in WorkspaceSettingsUpdateInput) IsEmpty() bool {
	return in.DisplayName == nil && in.EnforceEnabledModels == nil && in.PiiRedaction == nil
}

// WorkspaceSettingsAPI has no Create or Delete: the singleton always exists and
// can never be removed.
type WorkspaceSettingsAPI interface {
	Get(ctx context.Context) (*WorkspaceSettings, error)
	Update(ctx context.Context, in WorkspaceSettingsUpdateInput) (*WorkspaceSettings, error)
}

type connectWorkspaceSettings struct {
	c platformv1connect.WorkspaceSettingsServiceClient
}

func workspaceSettingsFromProto(s *platformv1.WorkspaceSettings) WorkspaceSettings {
	return WorkspaceSettings{
		Key:                  s.GetKey(),
		DisplayName:          s.GetDisplayName(),
		EnforceEnabledModels: s.GetEnforceEnabledModels(),
		PiiRedaction:         piiRedactionFromProto(s.GetPiiRedaction()),
	}
}

func piiRedactionFromProto(p *platformv1.PiiRedaction) *PiiRedaction {
	if p == nil {
		return nil
	}
	out := &PiiRedaction{Enabled: p.GetEnabled()}
	if cfg := p.GetConfig(); cfg != nil {
		out.Config = &PiiRedactionConfig{
			Language:  cloneStr(cfg.Language),
			Entities:  append([]string(nil), cfg.GetEntities()...),
			OnFailure: cloneStr(cfg.OnFailure),
			Threshold: cloneFloat(cfg.Threshold),
		}
	}
	return out
}

func piiRedactionToProto(p *PiiRedaction) *platformv1.PiiRedaction {
	if p == nil {
		return nil
	}
	out := &platformv1.PiiRedaction{Enabled: p.Enabled}
	if cfg := p.Config; cfg != nil {
		out.Config = &platformv1.PiiRedactionConfig{
			Language:  cloneStr(cfg.Language),
			Entities:  append([]string(nil), cfg.Entities...),
			OnFailure: cloneStr(cfg.OnFailure),
			Threshold: cloneFloat(cfg.Threshold),
		}
	}
	return out
}

// cloneStr / cloneFloat / cloneBool keep the caller and the proto message from
// aliasing the same pointer.
func cloneStr(v *string) *string {
	if v == nil {
		return nil
	}
	s := *v
	return &s
}

func cloneFloat(v *float64) *float64 {
	if v == nil {
		return nil
	}
	f := *v
	return &f
}

func cloneBool(v *bool) *bool {
	if v == nil {
		return nil
	}
	b := *v
	return &b
}

func (c *connectWorkspaceSettings) Get(ctx context.Context) (*WorkspaceSettings, error) {
	resp, err := c.c.GetWorkspaceSettings(ctx, &platformv1.GetWorkspaceSettingsRequest{})
	if err != nil {
		return nil, mapConnectError("workspace settings", err)
	}
	s := workspaceSettingsFromProto(resp.GetSettings())
	return &s, nil
}

func (c *connectWorkspaceSettings) Update(ctx context.Context, in WorkspaceSettingsUpdateInput) (*WorkspaceSettings, error) {
	req := &platformv1.UpdateWorkspaceSettingsRequest{
		DisplayName:          cloneStr(in.DisplayName),
		EnforceEnabledModels: cloneBool(in.EnforceEnabledModels),
		PiiRedaction:         piiRedactionToProto(in.PiiRedaction),
	}
	resp, err := c.c.UpdateWorkspaceSettings(ctx, req)
	if err != nil {
		return nil, mapConnectError("workspace settings", err)
	}
	s := workspaceSettingsFromProto(resp.GetSettings())
	return &s, nil
}
