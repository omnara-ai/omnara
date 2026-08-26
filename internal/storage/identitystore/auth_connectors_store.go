package identitystore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/textutil"
)

const (
	AuthConnectorKindOIDC   = "oidc"
	AuthConnectorKindGitHub = "github"

	AuthConnectorEmailTrustPolicyNone          = "none"
	AuthConnectorEmailTrustPolicyVerifiedEmail = "verified_email"
)

func (s *Store) UpsertAuthConnector(ctx context.Context, input CreateAuthConnectorInput) (AuthConnectorRecord, error) {
	normalizeAuthConnectorInput(&input)
	if err := validateAuthConnectorInput(input); err != nil {
		return AuthConnectorRecord{}, err
	}
	secretJSON, err := s.encryptedAuthConnectorSecretJSON(ctx, input.Slug, input.ClientSecret)
	if err != nil {
		return AuthConnectorRecord{}, err
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AuthConnectorRecord{}, fmt.Errorf("begin upsert auth connector: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	row, err := qtx.InsertAuthConnector(ctx, dbsqlc.InsertAuthConnectorParams{
		ID:                    uuid.New(),
		Slug:                  strings.TrimSpace(input.Slug),
		Kind:                  input.Kind,
		DisplayName:           strings.TrimSpace(input.DisplayName),
		Issuer:                strings.TrimSpace(input.Issuer),
		AuthorizationUrl:      strings.TrimSpace(input.AuthorizationURL),
		TokenUrl:              strings.TrimSpace(input.TokenURL),
		UserinfoUrl:           strings.TrimSpace(input.UserinfoURL),
		ClientID:              input.ClientID,
		EncryptedClientSecret: secretJSON,
		Scopes:                normalizedConnectorScopes(input.Kind, input.Scopes),
		EmailTrustPolicy:      input.EmailTrustPolicy,
		Enabled:               input.Enabled,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		existing, lookupErr := qtx.GetAuthConnectorBySlugForUpdate(
			ctx,
			dbsqlc.GetAuthConnectorBySlugForUpdateParams{Slug: input.Slug},
		)
		switch {
		case lookupErr == nil:
			if existing.Kind != input.Kind || existing.Issuer != input.Issuer {
				return AuthConnectorRecord{}, fmt.Errorf("%w %q", storeerr.ErrAuthConnectorImmutable, input.Slug)
			}
			row, err = qtx.UpdateAuthConnectorConfig(ctx, dbsqlc.UpdateAuthConnectorConfigParams{
				DisplayName:           input.DisplayName,
				AuthorizationUrl:      input.AuthorizationURL,
				TokenUrl:              input.TokenURL,
				UserinfoUrl:           input.UserinfoURL,
				ClientID:              input.ClientID,
				EncryptedClientSecret: secretJSON,
				Scopes:                normalizedConnectorScopes(input.Kind, input.Scopes),
				EmailTrustPolicy:      input.EmailTrustPolicy,
				Enabled:               input.Enabled,
				ID:                    existing.ID,
			})
		case errors.Is(lookupErr, pgx.ErrNoRows):
			_, issuerErr := qtx.GetAuthConnectorByIssuerForUpdate(
				ctx,
				dbsqlc.GetAuthConnectorByIssuerForUpdateParams{Issuer: input.Issuer},
			)
			if issuerErr == nil {
				return AuthConnectorRecord{}, fmt.Errorf("%w %q", storeerr.ErrAuthConnectorIdentityConflict, input.Issuer)
			}
			if !errors.Is(issuerErr, pgx.ErrNoRows) {
				return AuthConnectorRecord{}, fmt.Errorf("look up auth connector issuer: %w", issuerErr)
			}
			return AuthConnectorRecord{}, errors.New("auth connector insert conflicted without a matching slug or issuer")
		default:
			return AuthConnectorRecord{}, fmt.Errorf("look up auth connector slug: %w", lookupErr)
		}
	}
	if err != nil {
		return AuthConnectorRecord{}, fmt.Errorf("write auth connector: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return AuthConnectorRecord{}, fmt.Errorf("commit upsert auth connector: %w", err)
	}
	return s.authConnectorRecordFromSQLC(ctx, row)
}

func normalizeAuthConnectorInput(input *CreateAuthConnectorInput) {
	input.Slug = strings.TrimSpace(input.Slug)
	input.Kind = strings.TrimSpace(input.Kind)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Issuer = strings.TrimSpace(input.Issuer)
	input.AuthorizationURL = strings.TrimSpace(input.AuthorizationURL)
	input.TokenURL = strings.TrimSpace(input.TokenURL)
	input.UserinfoURL = strings.TrimSpace(input.UserinfoURL)
	input.EmailTrustPolicy = strings.TrimSpace(input.EmailTrustPolicy)
	if input.EmailTrustPolicy == "" {
		switch input.Kind {
		case AuthConnectorKindGitHub:
			input.EmailTrustPolicy = AuthConnectorEmailTrustPolicyVerifiedEmail
		default:
			input.EmailTrustPolicy = AuthConnectorEmailTrustPolicyNone
		}
	}
}

func validateAuthConnectorInput(input CreateAuthConnectorInput) error {
	if strings.TrimSpace(input.Slug) == "" {
		return errors.New("auth connector slug is required")
	}
	if !textutil.IsLowerURLSafeLabel(input.Slug) {
		return errors.New("auth connector slug must be a lowercase URL-safe label")
	}
	if input.Kind != AuthConnectorKindOIDC && input.Kind != AuthConnectorKindGitHub {
		return errors.New("unsupported auth connector kind")
	}
	if strings.TrimSpace(input.DisplayName) == "" {
		return errors.New("auth connector display name is required")
	}
	if input.ClientID == "" || input.ClientSecret == "" {
		return errors.New("auth connector client id and secret are required")
	}
	if input.Issuer == "" {
		return errors.New("auth connector issuer is required")
	}
	if input.Kind == AuthConnectorKindOIDC &&
		(input.AuthorizationURL != "" || input.TokenURL != "" || input.UserinfoURL != "") {
		return errors.New("oidc auth connectors use issuer discovery; endpoint overrides are not supported")
	}
	if input.Kind == AuthConnectorKindGitHub &&
		(input.AuthorizationURL == "" || input.TokenURL == "" || input.UserinfoURL == "") {
		return errors.New("github auth connector endpoints are required")
	}
	if input.EmailTrustPolicy != AuthConnectorEmailTrustPolicyNone &&
		input.EmailTrustPolicy != AuthConnectorEmailTrustPolicyVerifiedEmail {
		return errors.New("unsupported auth connector email trust policy")
	}
	return nil
}

func (s *Store) encryptedAuthConnectorSecretJSON(ctx context.Context, slug, clientSecret string) ([]byte, error) {
	encrypted, err := s.encryptAuthConnectorSecret(ctx, slug, clientSecret)
	if err != nil {
		return nil, err
	}
	secretJSON, err := json.Marshal(encrypted)
	if err != nil {
		return nil, fmt.Errorf("marshal auth connector secret: %w", err)
	}
	return secretJSON, nil
}

func (s *Store) GetEnabledAuthConnectorBySlug(ctx context.Context, slug string) (AuthConnectorRecord, error) {
	row, err := s.q.GetEnabledAuthConnectorBySlug(ctx, dbsqlc.GetEnabledAuthConnectorBySlugParams{Slug: slug})
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthConnectorRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return AuthConnectorRecord{}, fmt.Errorf("load enabled auth connector: %w", err)
	}
	return s.authConnectorRecordFromSQLC(ctx, row)
}

func (s *Store) ListEnabledAuthConnectorSummaries(ctx context.Context) ([]AuthConnectorSummaryRecord, error) {
	rows, err := s.q.ListEnabledAuthConnectorSummaries(ctx)
	if err != nil {
		return nil, fmt.Errorf("list auth connectors: %w", err)
	}
	out := make([]AuthConnectorSummaryRecord, 0, len(rows))
	for _, row := range rows {
		out = append(
			out,
			AuthConnectorSummaryRecord{ID: row.ID, Slug: row.Slug, Kind: row.Kind, DisplayName: row.DisplayName},
		)
	}
	return out, nil
}

func (s *Store) DisableUnlistedAuthConnectors(ctx context.Context, activeSlugs []string) (int64, error) {
	slugs := make([]string, 0, len(activeSlugs))
	for _, slug := range activeSlugs {
		slug = strings.TrimSpace(slug)
		if slug != "" {
			slugs = append(slugs, slug)
		}
	}
	disabled, err := s.q.DisableUnlistedAuthConnectors(
		ctx,
		dbsqlc.DisableUnlistedAuthConnectorsParams{ActiveSlugs: slugs},
	)
	if err != nil {
		return 0, fmt.Errorf("disable unlisted auth connectors: %w", err)
	}
	return disabled, nil
}

func (s *Store) authConnectorRecordFromSQLC(
	ctx context.Context,
	row dbsqlc.AuthConnector,
) (AuthConnectorRecord, error) {
	var encrypted secrets.EncryptedPayload
	if err := json.Unmarshal(row.EncryptedClientSecret, &encrypted); err != nil {
		return AuthConnectorRecord{}, fmt.Errorf("parse auth connector secret envelope: %w", err)
	}
	payload, err := secrets.DecryptPayload(ctx, s.secretKeyWrapper, encrypted, authConnectorSecretAAD(row.Slug))
	if err != nil {
		return AuthConnectorRecord{}, fmt.Errorf("decrypt auth connector secret: %w", err)
	}
	clientSecret := payload[secrets.KeyValue]
	if clientSecret == "" {
		return AuthConnectorRecord{}, errors.New("auth connector secret payload is missing")
	}
	return authConnectorRecordFromSQLC(row, clientSecret), nil
}

func (s *Store) encryptAuthConnectorSecret(
	ctx context.Context,
	slug, clientSecret string,
) (secrets.EncryptedPayload, error) {
	encrypted, err := secrets.EncryptPayload(
		ctx,
		s.secretKeyWrapper,
		secrets.KindGeneric,
		secrets.Payload{secrets.KeyValue: clientSecret},
		authConnectorSecretAAD(slug),
	)
	if err != nil {
		return secrets.EncryptedPayload{}, fmt.Errorf("encrypt auth connector secret: %w", err)
	}
	return encrypted, nil
}

func authConnectorSecretAAD(slug string) secrets.AssociatedData {
	return secrets.AssociatedData{
		OrgID:         "instance",
		SecretID:      "auth-connector-" + slug,
		VersionID:     "client-secret",
		VersionNumber: 1,
		Kind:          secrets.KindGeneric,
	}
}

func normalizedConnectorScopes(kind string, scopes []string) []string {
	if len(scopes) == 0 {
		switch kind {
		case AuthConnectorKindOIDC:
			scopes = []string{"openid", "email", "profile"}
		case AuthConnectorKindGitHub:
			scopes = []string{"read:user", "user:email"}
		}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" || seen[scope] {
			continue
		}
		seen[scope] = true
		out = append(out, scope)
	}
	slices.Sort(out)
	return out
}
