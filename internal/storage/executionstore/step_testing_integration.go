//go:build integration

package executionstore

import "context"

func (s *Store) IntegrationClaimUnprepared(
	ctx context.Context, input ClaimNextAgentWorkInput,
) (ClaimedAgentWork, bool, error) {
	return s.claimNextAgentWork(ctx, input, false)
}
