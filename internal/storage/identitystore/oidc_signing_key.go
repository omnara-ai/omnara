package identitystore

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func (s *Store) OIDCSigningKey(ctx context.Context) (*rsa.PrivateKey, error) {
	row, err := s.q.GetOIDCSigningKey(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := s.createOIDCSigningKey(ctx); err != nil {
			return nil, err
		}
		row, err = s.q.GetOIDCSigningKey(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("load OIDC signing key: %w", err)
	}
	var encrypted secrets.EncryptedPayload
	if err := json.Unmarshal(row, &encrypted); err != nil {
		return nil, fmt.Errorf("decode OIDC signing key: %w", err)
	}
	payload, err := secrets.DecryptPayload(ctx, s.secretKeyWrapper, encrypted, oidcSigningKeyAAD())
	if err != nil {
		return nil, fmt.Errorf("decrypt OIDC signing key: %w", err)
	}
	der, err := base64.StdEncoding.DecodeString(payload[secrets.KeyValue])
	if err != nil {
		return nil, fmt.Errorf("decode OIDC private key: %w", err)
	}
	key, err := x509.ParsePKCS1PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("parse OIDC private key: %w", err)
	}
	return key, nil
}

func (s *Store) createOIDCSigningKey(ctx context.Context) error {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("generate OIDC signing key: %w", err)
	}
	encrypted, err := secrets.EncryptPayload(ctx, s.secretKeyWrapper, secrets.KindGeneric,
		secrets.Payload{secrets.KeyValue: base64.StdEncoding.EncodeToString(x509.MarshalPKCS1PrivateKey(key))},
		oidcSigningKeyAAD())
	if err != nil {
		return fmt.Errorf("encrypt OIDC signing key: %w", err)
	}
	payload, err := json.Marshal(encrypted)
	if err != nil {
		return fmt.Errorf("encode OIDC signing key: %w", err)
	}
	if err := s.q.CreateOIDCSigningKey(ctx, dbsqlc.CreateOIDCSigningKeyParams{EncryptedPrivateKey: payload}); err != nil {
		return fmt.Errorf("persist OIDC signing key: %w", err)
	}
	return nil
}

func oidcSigningKeyAAD() secrets.AssociatedData {
	return secrets.AssociatedData{
		OrgID: "instance", SecretID: "oidc-signing-key", VersionID: "default",
		VersionNumber: 1, Kind: secrets.KindGeneric,
	}
}
