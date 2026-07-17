package client

import (
	"context"
	"net/http"

	"github.com/orq-ai/terraform-provider-orq/internal/restgen"
)

// Policy is the transport-agnostic projection of a policy (router entity). The
// resource layer sees this, not the oapi-codegen generated type.
type Policy struct {
	ID          string
	DisplayName string
	Description string
	Enabled     bool
	// ProjectID is empty for a workspace-global policy.
	ProjectID string
}

// PolicyPage is one page of a cursor-paginated list.
type PolicyPage struct {
	Policies []Policy
	HasMore  bool
}

// PoliciesAPI is the per-resource seam for the policies domain. It is the phase-1
// representative of a REST-backed (oapi-codegen) domain: it proves the seam and
// the error normalization behave identically whether a domain rides Connect or
// REST. Broader policy CRUD arrives in a later task.
type PoliciesAPI interface {
	List(ctx context.Context, params ListParams) (*PolicyPage, error)
}

// restPolicies implements PoliciesAPI over the REST /v2/policies endpoint.
type restPolicies struct {
	c *restgen.ClientWithResponses
}

func (p *restPolicies) List(ctx context.Context, params ListParams) (*PolicyPage, error) {
	restParams := &restgen.PolicyListParams{}
	if params.Limit > 0 {
		limit := int64(params.Limit)
		restParams.Limit = &limit
	}
	if params.StartingAfter != "" {
		restParams.StartingAfter = &params.StartingAfter
	}

	resp, err := p.c.PolicyListWithResponse(ctx, restParams)
	if err != nil {
		// Transport-level failure (connection refused, redirect rejection,
		// context cancellation) — no HTTP status is available.
		return nil, mapRESTTransportError(err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, mapRESTStatus(resp.StatusCode(), resp.Body)
	}

	out := &PolicyPage{HasMore: resp.JSON200.HasMore}
	if resp.JSON200.Data != nil {
		for _, pol := range *resp.JSON200.Data {
			policy := Policy{
				ID:          pol.UnderscoreId,
				DisplayName: pol.DisplayName,
				Enabled:     pol.Enabled,
				ProjectID:   pol.ProjectId,
			}
			if pol.Description != nil {
				policy.Description = *pol.Description
			}
			out.Policies = append(out.Policies, policy)
		}
	}
	return out, nil
}
