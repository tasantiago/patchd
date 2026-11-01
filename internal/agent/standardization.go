package agent

import (
	"context"
	"github.com/tasantiago/patchd/internal/protocol"
	"slices"
)

func (c *Client) SupportsStandardization() bool {
	return slices.Contains(c.Capabilities, protocol.CapabilityStandardization)
}

func (c *Client) Standardization(ctx context.Context, user protocol.SelfServiceUser) (protocol.StandardizationPlan, error) {
	var plan protocol.StandardizationPlan
	_, err := c.post(ctx, protocol.StandardizationPath, user, &plan)
	if err == nil {
		err = plan.Check()
	}
	return plan, err
}

func (c *Client) StandardizationReport(ctx context.Context, user protocol.SelfServiceUser, result protocol.StandardizationResult) error {
	if err := result.Check(); err != nil {
		return err
	}
	var response struct {
		OK bool `json:"ok"`
	}
	_, err := c.post(ctx, protocol.StandardizationReportPath, protocol.StandardizationReport{SelfServiceUser: user, StandardizationResult: result}, &response)
	return err
}

func (a *Agent) Standardization(ctx context.Context, user protocol.SelfServiceUser) (protocol.StandardizationPlan, error) {
	plan, err := a.client().Standardization(ctx, user)
	if err == nil && plan.CreatedJobs > 0 {
		a.Wake()
	}
	return plan, err
}

func (a *Agent) StandardizationReport(ctx context.Context, user protocol.SelfServiceUser, result protocol.StandardizationResult) error {
	return a.client().StandardizationReport(ctx, user, result)
}
