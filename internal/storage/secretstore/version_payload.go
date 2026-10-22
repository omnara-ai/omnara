package secretstore

import (
	"context"
	"errors"
	"maps"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

type versionPayload struct {
	orgID    uuid.UUID
	secretID uuid.UUID
	kind     secrets.Kind
	payload  secrets.Payload
}

func (s *Store) DecryptSecretVersion(ctx context.Context, orgID, secretID, versionID uuid.UUID,
	kind secrets.Kind) (SecretPayloadRecord, error) {
	if s.secretKeyWrapper == nil {
		return SecretPayloadRecord{}, errors.New("secret key wrapper is required")
	}
	s.versionMu.Lock()
	cached, ok := s.versionPayloads[versionID]
	s.versionMu.Unlock()
	if ok && cached.orgID == orgID && cached.secretID == secretID && cached.kind == kind {
		return SecretPayloadRecord{CurrentVersionID: versionID, Payload: maps.Clone(cached.payload)}, nil
	}
	row, err := s.q.GetSecretVersion(ctx, dbsqlc.GetSecretVersionParams{OrgID: orgID, SecretID: secretID, ID: versionID})
	if err != nil {
		return SecretPayloadRecord{}, err
	}
	payload, err := secrets.DecryptPayload(ctx, s.secretKeyWrapper, secrets.EncryptedPayload{
		EncryptionScheme: row.EncryptionScheme, KeyID: row.KeyID, DEKWrappedBy: row.DekWrappedBy,
		EncryptedDEK: row.EncryptedDek, EncryptedDEKNonce: row.EncryptedDekNonce, Nonce: row.Nonce,
		Ciphertext: row.Ciphertext, PayloadKeys: row.PayloadKeys,
	}, secrets.AssociatedData{OrgID: orgID.String(), SecretID: secretID.String(),
		VersionID: versionID.String(), VersionNumber: row.VersionNumber, Kind: kind})
	if err != nil {
		return SecretPayloadRecord{}, err
	}
	s.versionMu.Lock()
	if s.versionPayloads == nil {
		s.versionPayloads = make(map[uuid.UUID]versionPayload)
	}
	if len(s.versionPayloads) >= 256 {
		clear(s.versionPayloads)
	}
	s.versionPayloads[versionID] = versionPayload{orgID: orgID, secretID: secretID, kind: kind,
		payload: maps.Clone(payload)}
	s.versionMu.Unlock()
	return SecretPayloadRecord{CurrentVersionID: versionID, Payload: payload}, nil
}
