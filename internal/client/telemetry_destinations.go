package client

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	tracesv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/traces/v1"
	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/traces/v1/tracesv1connect"
)

const (
	TelemetryContentModeMetadataOnly = "METADATA_ONLY"
	TelemetryContentModeFullContent  = "FULL_CONTENT"

	TelemetryProjectScopeAll       = "ALL"
	TelemetryProjectScopeSelected  = "SELECTED"
	TelemetryProjectScopeAllExcept = "ALL_EXCEPT"

	telemetryContentModePrefix  = "TELEMETRY_CONTENT_MODE_"
	telemetryProjectScopePrefix = "TELEMETRY_PROJECT_SCOPE_"

	// The only provider the server accepts.
	telemetryProviderCustom = "custom"
	// The web app reads the endpoint it shows in its edit form from this setting.
	telemetryEndpointSetting = "endpointUrl"
)

type TelemetryDestination struct {
	ID          string
	DisplayName string
	EndpointURL string
	// Header values are write-only; the server returns the lowercased names only.
	RequestHeaderNames []string
	ContentMode        string
	ProjectScope       string
	ProjectIDs         []string
	Enabled            bool
	CreatedAt          string
	UpdatedAt          string
}

// TelemetryDestinationInput is the full desired state; an update replaces the destination.
type TelemetryDestinationInput struct {
	DisplayName    string
	EndpointURL    string
	RequestHeaders map[string]string
	ContentMode    string
	ProjectScope   string
	ProjectIDs     []string
	Enabled        bool
}

type TelemetryDestinationsAPI interface {
	Get(ctx context.Context, id string) (*TelemetryDestination, error)
	Create(ctx context.Context, in TelemetryDestinationInput) (*TelemetryDestination, error)
	Update(ctx context.Context, id string, in TelemetryDestinationInput) (*TelemetryDestination, error)
	Delete(ctx context.Context, id string) error
}

type connectTelemetryDestinations struct {
	c tracesv1connect.TelemetryDestinationsServiceClient
}

func telemetryEnumToProto(values map[string]int32, prefix, kind, short string) (int32, error) {
	v, ok := values[prefix+short]
	if !ok || v == 0 {
		return 0, &Error{Code: CodeInvalid, Message: fmt.Sprintf("unknown telemetry %s %q", kind, short)}
	}
	return v, nil
}

func telemetryDestinationToProto(in TelemetryDestinationInput) (*tracesv1.TelemetryDestination, error) {
	contentMode, err := telemetryEnumToProto(tracesv1.TelemetryContentMode_value, telemetryContentModePrefix, "content mode", in.ContentMode)
	if err != nil {
		return nil, err
	}
	projectScope, err := telemetryEnumToProto(tracesv1.TelemetryProjectScope_value, telemetryProjectScopePrefix, "project scope", in.ProjectScope)
	if err != nil {
		return nil, err
	}
	return &tracesv1.TelemetryDestination{
		DisplayName:    in.DisplayName,
		Provider:       telemetryProviderCustom,
		EndpointUrl:    in.EndpointURL,
		RequestHeaders: in.RequestHeaders,
		Settings:       map[string]string{telemetryEndpointSetting: in.EndpointURL},
		ContentMode:    tracesv1.TelemetryContentMode(contentMode),
		ProjectScope:   tracesv1.TelemetryProjectScope(projectScope),
		ProjectIds:     in.ProjectIDs,
		Enabled:        in.Enabled,
	}, nil
}

func telemetryDestinationFromProto(d *tracesv1.TelemetryDestination) *TelemetryDestination {
	return &TelemetryDestination{
		ID:                 d.GetId(),
		DisplayName:        d.GetDisplayName(),
		EndpointURL:        d.GetEndpointUrl(),
		RequestHeaderNames: slices.Sorted(maps.Keys(d.GetRequestHeaders())),
		ContentMode:        strings.TrimPrefix(d.GetContentMode().String(), telemetryContentModePrefix),
		ProjectScope:       strings.TrimPrefix(d.GetProjectScope().String(), telemetryProjectScopePrefix),
		ProjectIDs:         d.GetProjectIds(),
		Enabled:            d.GetEnabled(),
		CreatedAt:          formatTimestamp(d.GetCreatedAt()),
		UpdatedAt:          formatTimestamp(d.GetUpdatedAt()),
	}
}

func (c *connectTelemetryDestinations) Get(ctx context.Context, id string) (*TelemetryDestination, error) {
	resp, err := c.c.GetDestination(ctx, &tracesv1.GetTelemetryDestinationRequest{DestinationId: id})
	if err != nil {
		return nil, mapConnectError("telemetry destination", err)
	}
	return telemetryDestinationFromProto(resp.GetDestination()), nil
}

func (c *connectTelemetryDestinations) Create(ctx context.Context, in TelemetryDestinationInput) (*TelemetryDestination, error) {
	destination, err := telemetryDestinationToProto(in)
	if err != nil {
		return nil, err
	}
	resp, err := c.c.CreateDestination(ctx, &tracesv1.CreateTelemetryDestinationRequest{Destination: destination})
	if err != nil {
		return nil, mapConnectError("telemetry destination", err)
	}
	return telemetryDestinationFromProto(resp.GetDestination()), nil
}

func (c *connectTelemetryDestinations) Update(ctx context.Context, id string, in TelemetryDestinationInput) (*TelemetryDestination, error) {
	destination, err := telemetryDestinationToProto(in)
	if err != nil {
		return nil, err
	}
	resp, err := c.c.UpdateDestination(ctx, &tracesv1.UpdateTelemetryDestinationRequest{DestinationId: id, Destination: destination})
	if err != nil {
		return nil, mapConnectError("telemetry destination", err)
	}
	return telemetryDestinationFromProto(resp.GetDestination()), nil
}

func (c *connectTelemetryDestinations) Delete(ctx context.Context, id string) error {
	_, err := c.c.DeleteDestination(ctx, &tracesv1.DeleteTelemetryDestinationRequest{DestinationId: id})
	return mapConnectError("telemetry destination", err)
}
