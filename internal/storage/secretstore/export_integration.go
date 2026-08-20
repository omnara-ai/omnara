//go:build integration

package secretstore

import (
	"context"

	"github.com/omnara-ai/omnara/internal/storage/management"
)

func (s *Store) DeleteSecretOnceForIntegration(
	ctx context.Context,
	input DeleteSecretInput,
) (SecretRecord, error) {
	if isNilID(input.OrgID) || isNilID(input.SecretID) || isNilID(input.Actor.ID) {
		return SecretRecord{}, invalidSecretRequest("org, secret, and actor are required")
	}
	record, err := s.GetSecret(ctx, input.OrgID, input.SecretID)
	if err != nil {
		return SecretRecord{}, err
	}
	if err := management.RequireTenant(record.ManagementKind, "secrets"); err != nil {
		return SecretRecord{}, err
	}
	if err := s.authorizeSecretManage(ctx, record, input.Actor); err != nil {
		return SecretRecord{}, err
	}
	return s.deleteSecretOnce(ctx, input, record)
}
