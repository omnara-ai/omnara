package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"time"

	"github.com/omnara-ai/omnara/internal/urlpolicy"
)

type Kind string

const (
	KindGeneric                Kind = "generic"
	KindOAuthTokenSet          Kind = "oauth_token_set"
	KindSlackAppCredentials    Kind = "slack_app_credentials"
	KindAWSCredentials         Kind = "aws_credentials"
	KindIntegrationCredentials Kind = "integration_credentials"

	KeyValue              = "value"
	KeyAccessToken        = "access_token"
	KeyRefreshToken       = "refresh_token"
	KeyIDToken            = "id_token"
	KeyClientSecret       = "client_secret"
	KeyClientID           = "client_id"
	KeySigningSecret      = "signing_secret"
	KeyMCPURL             = "mcp_url"
	KeyResource           = "resource"
	KeyTokenEndpoint      = "token_endpoint"
	KeyScopes             = "scopes"
	KeyTokenType          = "token_type"
	KeyAWSAccessKeyID     = "access_key_id"
	KeyAWSSecretAccessKey = "secret_access_key"
	KeyAWSSessionToken    = "session_token"
	KeyAWSRoleARN         = "role_arn"
	KeyAWSExternalID      = "external_id"

	EncryptionSchemeAES256GCMEnvelopeV1 = "aes-256-gcm-envelope-v1"
	DEKWrappedByLocal                   = "local"

	// Bound accepted secret values before encryption so one payload cannot dominate memory or row size.
	MaxPayloadValueBytes                       = 64 * 1024
	MaxIntegrationCredentialKeys               = 64
	MaxIntegrationCredentialKeyBytes           = 128
	MaxIntegrationCredentialPayloadBytes       = 256 * 1024
	MaxOAuthAccessTokenTTLSeconds        int64 = 9_223_372_036

	dataKeySize     = 32
	localKeySize    = 32
	aesGCMNonceSize = 12
)

var integrationCredentialKeyPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)

var ErrInvalidPayload = errors.New("invalid secret payload")

type Payload map[string]string

type OAuthAccessTokenLifetime struct {
	ttl       time.Duration
	startedAt time.Time
}

func NewOAuthAccessTokenLifetime(ttl time.Duration, startedAt time.Time) OAuthAccessTokenLifetime {
	return OAuthAccessTokenLifetime{ttl: ttl, startedAt: startedAt}
}

func FixedOAuthAccessTokenLifetime(ttl time.Duration) OAuthAccessTokenLifetime {
	return OAuthAccessTokenLifetime{ttl: ttl}
}

func (l OAuthAccessTokenLifetime) Remaining() (time.Duration, error) {
	if l.ttl == 0 {
		return 0, nil
	}
	remaining := l.ttl
	if !l.startedAt.IsZero() {
		if elapsed := time.Since(l.startedAt); elapsed > 0 {
			remaining -= elapsed
		}
	}
	if remaining <= 0 {
		return 0, errors.New("oauth access token expired before persistence")
	}
	return remaining, nil
}

type AssociatedData struct {
	OrgID         string
	SecretID      string
	VersionID     string
	VersionNumber int32
	Kind          Kind
	PayloadKeys   []string
}

type EncryptedPayload struct {
	EncryptionScheme  string
	KeyID             string
	DEKWrappedBy      string
	EncryptedDEK      []byte
	EncryptedDEKNonce []byte
	Nonce             []byte
	Ciphertext        []byte
	PayloadKeys       []string
}

type WrappedDataKey struct {
	KeyID      string
	WrappedBy  string
	Ciphertext []byte
	Nonce      []byte
}

type KeyWrapper interface {
	ActiveKeyID(context.Context) (string, error)
	WrapDataKey(ctx context.Context, dataKey []byte, associatedData []byte) (WrappedDataKey, error)
	UnwrapDataKey(ctx context.Context, wrapped WrappedDataKey, associatedData []byte) ([]byte, error)
}

type LocalKeyWrapper struct {
	activeKeyID string
	keys        map[string][]byte
}

func NewLocalKeyWrapper(activeKeyID string, keys map[string][]byte) (*LocalKeyWrapper, error) {
	if activeKeyID == "" {
		return nil, errors.New("active key id is required")
	}
	if len(keys) == 0 {
		return nil, errors.New("at least one key is required")
	}
	copied := make(map[string][]byte, len(keys))
	seenKeys := make(map[string]string, len(keys))
	for keyID, key := range keys {
		if keyID == "" {
			return nil, errors.New("key id cannot be empty")
		}
		if len(key) != localKeySize {
			return nil, fmt.Errorf("key %q must be %d bytes", keyID, localKeySize)
		}
		encodedKey := string(key)
		if existingKeyID, ok := seenKeys[encodedKey]; ok {
			return nil, fmt.Errorf("key %q duplicates key material from %q", keyID, existingKeyID)
		}
		seenKeys[encodedKey] = keyID
		copied[keyID] = append([]byte(nil), key...)
	}
	if _, ok := copied[activeKeyID]; !ok {
		return nil, fmt.Errorf("active key id %q is not configured", activeKeyID)
	}
	return &LocalKeyWrapper{activeKeyID: activeKeyID, keys: copied}, nil
}

func (w *LocalKeyWrapper) ActiveKeyID(context.Context) (string, error) {
	if w == nil || w.activeKeyID == "" {
		return "", errors.New("active key id is not configured")
	}
	return w.activeKeyID, nil
}

func (w *LocalKeyWrapper) WrapDataKey(
	ctx context.Context,
	dataKey []byte,
	associatedData []byte,
) (WrappedDataKey, error) {
	if err := validateDataKey(dataKey); err != nil {
		return WrappedDataKey{}, err
	}
	keyID, err := w.ActiveKeyID(ctx)
	if err != nil {
		return WrappedDataKey{}, err
	}
	kek, err := w.key(keyID)
	if err != nil {
		return WrappedDataKey{}, err
	}
	nonce, err := randomBytes(aesGCMNonceSize)
	if err != nil {
		return WrappedDataKey{}, err
	}
	aead, err := newAESGCM(kek)
	if err != nil {
		return WrappedDataKey{}, err
	}
	return WrappedDataKey{
		KeyID:      keyID,
		WrappedBy:  DEKWrappedByLocal,
		Ciphertext: aead.Seal(nil, nonce, dataKey, associatedData),
		Nonce:      nonce,
	}, nil
}

func (w *LocalKeyWrapper) UnwrapDataKey(
	_ context.Context,
	wrapped WrappedDataKey,
	associatedData []byte,
) ([]byte, error) {
	if wrapped.WrappedBy != DEKWrappedByLocal {
		return nil, fmt.Errorf("unsupported secret DEK wrapper %q", wrapped.WrappedBy)
	}
	kek, err := w.key(wrapped.KeyID)
	if err != nil {
		return nil, err
	}
	aead, err := newAESGCM(kek)
	if err != nil {
		return nil, err
	}
	if err := validateNonce("encrypted DEK nonce", wrapped.Nonce, aead); err != nil {
		return nil, err
	}
	dataKey, err := aead.Open(nil, wrapped.Nonce, wrapped.Ciphertext, associatedData)
	if err != nil {
		return nil, fmt.Errorf("unwrap secret data key: %w", err)
	}
	if err := validateDataKey(dataKey); err != nil {
		return nil, err
	}
	return dataKey, nil
}

func (w *LocalKeyWrapper) key(keyID string) ([]byte, error) {
	if w == nil {
		return nil, errors.New("key wrapper is not configured")
	}
	key, ok := w.keys[keyID]
	if !ok {
		return nil, fmt.Errorf("unknown key id %q", keyID)
	}
	return append([]byte(nil), key...), nil
}

func ValidatePayload(kind Kind, payload Payload) ([]string, error) {
	if len(payload) == 0 {
		return nil, errors.New("secret payload is required")
	}
	allowed := map[string]bool{}
	var required []string
	switch kind {
	case KindGeneric:
		allowed[KeyValue] = true
		required = []string{KeyValue}
	case KindOAuthTokenSet:
		allowed[KeyAccessToken] = true
		allowed[KeyRefreshToken] = true
		allowed[KeyIDToken] = true
		allowed[KeyClientSecret] = true
		allowed[KeyClientID] = true
		allowed[KeyMCPURL] = true
		allowed[KeyResource] = true
		allowed[KeyTokenEndpoint] = true
		allowed[KeyScopes] = true
		allowed[KeyTokenType] = true
		required = []string{KeyAccessToken}
	case KindSlackAppCredentials:
		allowed[KeyAccessToken] = true
		allowed[KeyClientID] = true
		allowed[KeyClientSecret] = true
		allowed[KeySigningSecret] = true
		required = []string{KeyAccessToken, KeyClientID, KeyClientSecret, KeySigningSecret}
	case KindAWSCredentials:
		allowed[KeyAWSAccessKeyID] = true
		allowed[KeyAWSSecretAccessKey] = true
		allowed[KeyAWSSessionToken] = true
		allowed[KeyAWSRoleARN] = true
		allowed[KeyAWSExternalID] = true
		required = []string{KeyAWSAccessKeyID, KeyAWSSecretAccessKey}
	case KindIntegrationCredentials:
		return validateIntegrationCredentialPayload(payload)
	default:
		return nil, fmt.Errorf("unsupported secret kind %q", kind)
	}
	for _, key := range required {
		if payload[key] == "" {
			return nil, fmt.Errorf("secret payload key %q is required", key)
		}
	}
	if kind == KindOAuthTokenSet {
		refreshKeys := []string{KeyRefreshToken, KeyTokenEndpoint, KeyClientID, KeyResource}
		hasRefreshMaterial := false
		for _, key := range append(refreshKeys, KeyClientSecret) {
			if payload[key] != "" {
				hasRefreshMaterial = true
				break
			}
		}
		if hasRefreshMaterial {
			for _, key := range refreshKeys {
				if payload[key] == "" {
					return nil, fmt.Errorf("oauth refresh material key %q is required", key)
				}
			}
			if err := urlpolicy.RequireHTTPSOrLoopback(payload[KeyTokenEndpoint]); err != nil {
				return nil, fmt.Errorf("oauth token endpoint: %w", err)
			}
		}
	}
	if kind == KindAWSCredentials && payload[KeyAWSExternalID] != "" && payload[KeyAWSRoleARN] == "" {
		return nil, errors.New("aws external id requires a role arn")
	}
	keys := make([]string, 0, len(payload))
	for key, value := range payload {
		if !allowed[key] {
			return nil, fmt.Errorf("secret payload key %q is not allowed for kind %q", key, kind)
		}
		if value == "" {
			return nil, fmt.Errorf("secret payload key %q cannot be empty", key)
		}
		if len(value) > MaxPayloadValueBytes {
			return nil, fmt.Errorf("secret payload key %q exceeds %d bytes", key, MaxPayloadValueBytes)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, nil
}

func validateIntegrationCredentialPayload(payload Payload) ([]string, error) {
	if len(payload) == 0 {
		return nil, errors.New("secret payload is required")
	}
	if len(payload) > MaxIntegrationCredentialKeys {
		return nil, fmt.Errorf(
			"integration credential payload exceeds %d keys",
			MaxIntegrationCredentialKeys,
		)
	}
	keys := make([]string, 0, len(payload))
	totalBytes := 0
	for key, value := range payload {
		if len(key) > MaxIntegrationCredentialKeyBytes ||
			!integrationCredentialKeyPattern.MatchString(key) {
			return nil, fmt.Errorf("integration credential key %q is invalid", key)
		}
		if value == "" {
			return nil, fmt.Errorf("secret payload key %q cannot be empty", key)
		}
		if len(value) > MaxPayloadValueBytes {
			return nil, fmt.Errorf(
				"secret payload key %q exceeds %d bytes",
				key,
				MaxPayloadValueBytes,
			)
		}
		totalBytes += len(key) + len(value)
		if totalBytes > MaxIntegrationCredentialPayloadBytes {
			return nil, fmt.Errorf(
				"integration credential payload exceeds %d bytes",
				MaxIntegrationCredentialPayloadBytes,
			)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, nil
}

func EncryptPayload(
	ctx context.Context,
	keyWrapper KeyWrapper,
	kind Kind,
	payload Payload,
	aad AssociatedData,
) (EncryptedPayload, error) {
	if keyWrapper == nil {
		return EncryptedPayload{}, errors.New("key wrapper is required")
	}
	payloadKeys, err := ValidatePayload(kind, payload)
	if err != nil {
		return EncryptedPayload{}, fmt.Errorf("%w: %w", ErrInvalidPayload, err)
	}
	aad.PayloadKeys = payloadKeys
	if err := validateAssociatedData(kind, aad); err != nil {
		return EncryptedPayload{}, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return EncryptedPayload{}, fmt.Errorf("marshal secret payload: %w", err)
	}
	dek, err := randomBytes(dataKeySize)
	if err != nil {
		return EncryptedPayload{}, err
	}
	nonce, err := randomBytes(aesGCMNonceSize)
	if err != nil {
		return EncryptedPayload{}, err
	}
	payloadAEAD, err := newAESGCM(dek)
	if err != nil {
		return EncryptedPayload{}, err
	}
	associatedBytes, err := aadBytes(aad)
	if err != nil {
		return EncryptedPayload{}, err
	}
	wrappedDEK, err := keyWrapper.WrapDataKey(ctx, dek, associatedBytes)
	if err != nil {
		return EncryptedPayload{}, err
	}
	if err := verifyWrappedDataKey(ctx, keyWrapper, wrappedDEK, dek, associatedBytes); err != nil {
		return EncryptedPayload{}, err
	}
	return EncryptedPayload{
		EncryptionScheme:  EncryptionSchemeAES256GCMEnvelopeV1,
		KeyID:             wrappedDEK.KeyID,
		DEKWrappedBy:      wrappedDEK.WrappedBy,
		EncryptedDEK:      wrappedDEK.Ciphertext,
		EncryptedDEKNonce: wrappedDEK.Nonce,
		Nonce:             nonce,
		Ciphertext:        payloadAEAD.Seal(nil, nonce, body, associatedBytes),
		PayloadKeys:       payloadKeys,
	}, nil
}

func DecryptPayload(
	ctx context.Context,
	keyWrapper KeyWrapper,
	encrypted EncryptedPayload,
	aad AssociatedData,
) (Payload, error) {
	if keyWrapper == nil {
		return nil, errors.New("key wrapper is required")
	}
	if encrypted.EncryptionScheme != EncryptionSchemeAES256GCMEnvelopeV1 {
		return nil, fmt.Errorf("unsupported secret encryption scheme %q", encrypted.EncryptionScheme)
	}
	aad.PayloadKeys = append([]string(nil), encrypted.PayloadKeys...)
	sort.Strings(aad.PayloadKeys)
	if err := validateAssociatedData(aad.Kind, aad); err != nil {
		return nil, err
	}
	associatedBytes, err := aadBytes(aad)
	if err != nil {
		return nil, err
	}
	dek, err := keyWrapper.UnwrapDataKey(
		ctx,
		WrappedDataKey{
			KeyID:      encrypted.KeyID,
			WrappedBy:  encrypted.DEKWrappedBy,
			Ciphertext: encrypted.EncryptedDEK,
			Nonce:      encrypted.EncryptedDEKNonce,
		},
		associatedBytes,
	)
	if err != nil {
		return nil, err
	}
	if err := validateDataKey(dek); err != nil {
		return nil, err
	}
	payloadAEAD, err := newAESGCM(dek)
	if err != nil {
		return nil, err
	}
	if err := validateNonce("payload nonce", encrypted.Nonce, payloadAEAD); err != nil {
		return nil, err
	}
	body, err := payloadAEAD.Open(nil, encrypted.Nonce, encrypted.Ciphertext, associatedBytes)
	if err != nil {
		return nil, fmt.Errorf("decrypt secret payload: %w", err)
	}
	var payload Payload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("unmarshal secret payload: %w", err)
	}
	actualPayloadKeys, err := ValidatePayload(aad.Kind, payload)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(actualPayloadKeys, aad.PayloadKeys) {
		return nil, errors.New("secret payload keys do not match envelope")
	}
	return payload, nil
}

func RewrapPayloadKey(
	ctx context.Context,
	keyWrapper KeyWrapper,
	encrypted EncryptedPayload,
	aad AssociatedData,
) (EncryptedPayload, error) {
	if keyWrapper == nil {
		return EncryptedPayload{}, errors.New("key wrapper is required")
	}
	if encrypted.EncryptionScheme != EncryptionSchemeAES256GCMEnvelopeV1 {
		return EncryptedPayload{}, fmt.Errorf("unsupported secret encryption scheme %q", encrypted.EncryptionScheme)
	}
	aad.PayloadKeys = append([]string(nil), encrypted.PayloadKeys...)
	sort.Strings(aad.PayloadKeys)
	if err := validateAssociatedData(aad.Kind, aad); err != nil {
		return EncryptedPayload{}, err
	}
	activeKeyID, err := keyWrapper.ActiveKeyID(ctx)
	if err != nil {
		return EncryptedPayload{}, err
	}
	if encrypted.KeyID == activeKeyID {
		return encrypted, nil
	}
	associatedBytes, err := aadBytes(aad)
	if err != nil {
		return EncryptedPayload{}, err
	}
	dek, err := keyWrapper.UnwrapDataKey(
		ctx,
		WrappedDataKey{
			KeyID:      encrypted.KeyID,
			WrappedBy:  encrypted.DEKWrappedBy,
			Ciphertext: encrypted.EncryptedDEK,
			Nonce:      encrypted.EncryptedDEKNonce,
		},
		associatedBytes,
	)
	if err != nil {
		return EncryptedPayload{}, err
	}
	if err := validateDataKey(dek); err != nil {
		return EncryptedPayload{}, err
	}
	rewrappedDEK, err := keyWrapper.WrapDataKey(ctx, dek, associatedBytes)
	if err != nil {
		return EncryptedPayload{}, err
	}
	if err := verifyWrappedDataKey(ctx, keyWrapper, rewrappedDEK, dek, associatedBytes); err != nil {
		return EncryptedPayload{}, err
	}
	rewrapped := encrypted
	rewrapped.KeyID = rewrappedDEK.KeyID
	rewrapped.DEKWrappedBy = rewrappedDEK.WrappedBy
	rewrapped.EncryptedDEKNonce = rewrappedDEK.Nonce
	rewrapped.EncryptedDEK = rewrappedDEK.Ciphertext
	rewrapped.PayloadKeys = append([]string(nil), encrypted.PayloadKeys...)
	return rewrapped, nil
}

func validateAssociatedData(kind Kind, aad AssociatedData) error {
	if aad.OrgID == "" || aad.SecretID == "" || aad.VersionID == "" || aad.VersionNumber <= 0 || aad.Kind == "" {
		return errors.New("complete associated data is required")
	}
	if aad.Kind != kind {
		return errors.New("associated data kind does not match payload kind")
	}
	if len(aad.PayloadKeys) == 0 {
		return errors.New("associated data payload keys are required")
	}
	return nil
}

func aadBytes(aad AssociatedData) ([]byte, error) {
	keys := append([]string(nil), aad.PayloadKeys...)
	sort.Strings(keys)
	// This struct is a persisted encryption wire format. Reordering fields or
	// changing tags changes the AEAD associated data and makes existing secrets
	// undecryptable; keep internal tests pinned to the exact bytes.
	type canonicalAAD struct {
		SchemaVersion int      `json:"schema_version"`
		OrgID         string   `json:"org_id"`
		SecretID      string   `json:"secret_id"`
		VersionID     string   `json:"version_id"`
		VersionNumber int32    `json:"version_number"`
		Kind          Kind     `json:"kind"`
		PayloadKeys   []string `json:"payload_keys"`
	}
	return json.Marshal(
		canonicalAAD{
			SchemaVersion: 1,
			OrgID:         aad.OrgID,
			SecretID:      aad.SecretID,
			VersionID:     aad.VersionID,
			VersionNumber: aad.VersionNumber,
			Kind:          aad.Kind,
			PayloadKeys:   keys,
		},
	)
}

func newAESGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create AES-GCM: %w", err)
	}
	return aead, nil
}

func validateNonce(name string, nonce []byte, aead cipher.AEAD) error {
	if len(nonce) != aead.NonceSize() {
		return fmt.Errorf("%s must be %d bytes", name, aead.NonceSize())
	}
	return nil
}

func validateDataKey(dataKey []byte) error {
	if len(dataKey) != dataKeySize {
		return fmt.Errorf("data key must be %d bytes", dataKeySize)
	}
	return nil
}

func verifyWrappedDataKey(
	ctx context.Context,
	keyWrapper KeyWrapper,
	wrapped WrappedDataKey,
	wantDataKey []byte,
	associatedData []byte,
) error {
	gotDataKey, err := keyWrapper.UnwrapDataKey(ctx, wrapped, associatedData)
	if err != nil {
		return fmt.Errorf("verify wrapped data key: %w", err)
	}
	if err := validateDataKey(gotDataKey); err != nil {
		return fmt.Errorf("verify wrapped data key: %w", err)
	}
	if subtle.ConstantTimeCompare(gotDataKey, wantDataKey) != 1 {
		return errors.New("verify wrapped data key: unwrapped data key mismatch")
	}
	return nil
}

func randomBytes(n int) ([]byte, error) {
	out := make([]byte, n)
	if _, err := rand.Read(out); err != nil {
		return nil, fmt.Errorf("generate random bytes: %w", err)
	}
	return out, nil
}
