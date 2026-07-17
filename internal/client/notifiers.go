package client

import (
	"context"
	"fmt"
	"strings"

	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
)

// Notifier destination types, prefix-stripped from NotifierType.
const (
	NotifierTypeEmail        = "EMAIL"
	NotifierTypeSlackWebhook = "SLACK_WEBHOOK"
	NotifierTypeWebhook      = "WEBHOOK"
)

// Notifier is the transport-agnostic projection of a notifier destination.
// metadata and headers (arbitrary JSON / secret-bearing structs) are not
// surfaced by the provider yet — see the resource docs.
type Notifier struct {
	ID          string
	ProjectID   string
	DisplayName string
	Type        string
	Emails      []string
	// IncomingWebhookURL / WebhookURL can carry embedded secrets, so the
	// resource marks them Sensitive.
	IncomingWebhookURL string
	WebhookURL         string
	CreatedAt          string
	UpdatedAt          string
}

// NotifierPage is one page of a cursor-paginated list.
type NotifierPage struct {
	Notifiers []Notifier
	HasMore   bool
}

// NotifierCreateInput carries the fields for a create.
type NotifierCreateInput struct {
	ProjectID          string
	DisplayName        string
	Type               string
	Emails             []string
	IncomingWebhookURL string
	WebhookURL         string
}

// NotifierUpdateInput is a sparse patch: nil pointers leave the field unchanged.
type NotifierUpdateInput struct {
	ID                 string
	ProjectID          *string
	DisplayName        *string
	Type               *string
	Emails             []string
	IncomingWebhookURL *string
	WebhookURL         *string
}

// NotifiersAPI is the per-resource seam for the notifiers domain (Connect-backed).
type NotifiersAPI interface {
	List(ctx context.Context, params ListParams) (*NotifierPage, error)
	Get(ctx context.Context, id string) (*Notifier, error)
	Create(ctx context.Context, in NotifierCreateInput) (*Notifier, error)
	Update(ctx context.Context, in NotifierUpdateInput) (*Notifier, error)
	Delete(ctx context.Context, id string) error
}

type connectNotifiers struct {
	c platformv1connect.NotifiersServiceClient
}

func notifierTypeToProto(short string) (platformv1.NotifierType, error) {
	if short == "" {
		return platformv1.NotifierType_NOTIFIER_TYPE_UNSPECIFIED, nil
	}
	v, ok := platformv1.NotifierType_value["NOTIFIER_TYPE_"+strings.ToUpper(short)]
	if !ok {
		return 0, fmt.Errorf("unknown notifier type %q", short)
	}
	return platformv1.NotifierType(v), nil
}

func notifierTypeFromProto(t platformv1.NotifierType) string {
	if t == platformv1.NotifierType_NOTIFIER_TYPE_UNSPECIFIED {
		return ""
	}
	return strings.TrimPrefix(t.String(), "NOTIFIER_TYPE_")
}

func notifierFromProto(n *platformv1.Notifier) Notifier {
	return Notifier{
		ID:                 n.GetId(),
		ProjectID:          n.GetProjectId(),
		DisplayName:        n.GetDisplayName(),
		Type:               notifierTypeFromProto(n.GetType()),
		Emails:             n.GetEmails(),
		IncomingWebhookURL: n.GetIncomingWebhookUrl(),
		WebhookURL:         n.GetWebhookUrl(),
		CreatedAt:          formatTimestamp(n.GetCreatedAt()),
		UpdatedAt:          formatTimestamp(n.GetUpdatedAt()),
	}
}

func (c *connectNotifiers) List(ctx context.Context, params ListParams) (*NotifierPage, error) {
	req := &platformv1.ListNotifiersRequest{}
	if params.Limit > 0 {
		req.Limit = &params.Limit
	}
	if params.StartingAfter != "" {
		req.StartingAfter = params.StartingAfter
	}
	resp, err := c.c.ListNotifiers(ctx, req)
	if err != nil {
		return nil, mapConnectError(err)
	}
	out := &NotifierPage{HasMore: resp.GetHasMore()}
	for _, n := range resp.GetData() {
		out.Notifiers = append(out.Notifiers, notifierFromProto(n))
	}
	return out, nil
}

func (c *connectNotifiers) Get(ctx context.Context, id string) (*Notifier, error) {
	resp, err := c.c.GetNotifier(ctx, &platformv1.GetNotifierRequest{NotifierId: id})
	if err != nil {
		return nil, mapConnectError(err)
	}
	n := notifierFromProto(resp.GetNotifier())
	return &n, nil
}

func (c *connectNotifiers) Create(ctx context.Context, in NotifierCreateInput) (*Notifier, error) {
	nt, err := notifierTypeToProto(in.Type)
	if err != nil {
		return nil, err
	}
	req := &platformv1.CreateNotifierRequest{
		ProjectId:          in.ProjectID,
		DisplayName:        in.DisplayName,
		Type:               nt,
		Emails:             in.Emails,
		IncomingWebhookUrl: in.IncomingWebhookURL,
		WebhookUrl:         in.WebhookURL,
	}
	resp, err := c.c.CreateNotifier(ctx, req)
	if err != nil {
		return nil, mapConnectError(err)
	}
	n := notifierFromProto(resp.GetNotifier())
	return &n, nil
}

func (c *connectNotifiers) Update(ctx context.Context, in NotifierUpdateInput) (*Notifier, error) {
	req := &platformv1.UpdateNotifierRequest{NotifierId: in.ID, Emails: in.Emails}
	if in.ProjectID != nil {
		req.ProjectId = in.ProjectID
	}
	if in.DisplayName != nil {
		req.DisplayName = in.DisplayName
	}
	if in.Type != nil {
		nt, err := notifierTypeToProto(*in.Type)
		if err != nil {
			return nil, err
		}
		req.Type = &nt
	}
	if in.IncomingWebhookURL != nil {
		req.IncomingWebhookUrl = in.IncomingWebhookURL
	}
	if in.WebhookURL != nil {
		req.WebhookUrl = in.WebhookURL
	}
	resp, err := c.c.UpdateNotifier(ctx, req)
	if err != nil {
		return nil, mapConnectError(err)
	}
	n := notifierFromProto(resp.GetNotifier())
	return &n, nil
}

func (c *connectNotifiers) Delete(ctx context.Context, id string) error {
	_, err := c.c.DeleteNotifier(ctx, &platformv1.DeleteNotifierRequest{NotifierId: id})
	return mapConnectError(err)
}
