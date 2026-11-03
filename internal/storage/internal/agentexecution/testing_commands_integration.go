//go:build integration

package agentexecution

import "context"

func (h *Handle) AdmitInputs(ctx context.Context) (InputAdmission, error) {
	return executeCommand(ctx, h, func(m *executionMutation) (InputAdmission, error) {
		snapshot, err := h.LoadExecution(ctx)
		if err != nil {
			return InputAdmission{}, err
		}
		if snapshot.Selection.Work != WorkInput {
			return InputAdmission{}, nil
		}
		return h.admitInputs(ctx, m, snapshot.Selection.Admission)
	})
}

func (c *AgentCell) Transact(ctx context.Context, run func(*Unit) error) error {
	_, err := Retry(ctx, "agent execution", func(ctx context.Context) (struct{}, error) {
		u, err := c.Begin(ctx)
		if err != nil {
			return struct{}{}, err
		}
		defer func() { _ = u.Rollback(context.WithoutCancel(ctx)) }()
		if err = run(u); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, u.Commit(ctx, "agent execution")
	})
	return err
}
