package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestEncryptDecryptPayloadRoundTrip(t *testing.T) {
	keyWrapper := testWrapper(t)
	aad := testAAD()
	aad.Kind = KindOAuthTokenSet
	payload := Payload{
		KeyAccessToken:   "access",
		KeyRefreshToken:  "refresh",
		KeyTokenEndpoint: "https://example.com/token",
		KeyClientID:      "client-id",
		KeyResource:      "https://example.com/resource",
		KeyIDToken:       "id",
	}

	encrypted, err := EncryptPayload(context.Background(), keyWrapper, KindOAuthTokenSet, payload, aad)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if bytes.Contains(encrypted.Ciphertext, []byte("access")) || bytes.Contains(encrypted.EncryptedDEK, []byte("access")) {
		t.Fatal("encrypted envelope contains plaintext")
	}
	decrypted, err := DecryptPayload(context.Background(), keyWrapper, encrypted, aad)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !reflect.DeepEqual(decrypted, payload) {
		t.Fatalf("decrypted payload = %#v, want %#v", decrypted, payload)
	}
}

func TestAssociatedDataWireFormatGolden(t *testing.T) {
	got, err := aadBytes(testAAD())
	if err != nil {
		t.Fatalf("aad bytes: %v", err)
	}
	want := `{"schema_version":1,"org_id":"org","secret_id":"secret","version_id":"version",` +
		`"version_number":1,"kind":"generic","payload_keys":["value"]}`
	if string(got) != want {
		t.Fatalf("AAD bytes changed:\ngot  %s\nwant %s", got, want)
	}
}

func TestEncryptUsesFreshRandomness(t *testing.T) {
	keyWrapper := testWrapper(t)
	aad := testAAD()
	payload := Payload{KeyValue: "secret"}

	first, err := EncryptPayload(context.Background(), keyWrapper, KindGeneric, payload, aad)
	if err != nil {
		t.Fatalf("encrypt first: %v", err)
	}
	second, err := EncryptPayload(context.Background(), keyWrapper, KindGeneric, payload, aad)
	if err != nil {
		t.Fatalf("encrypt second: %v", err)
	}
	if bytes.Equal(first.Ciphertext, second.Ciphertext) || bytes.Equal(first.EncryptedDEK, second.EncryptedDEK) {
		t.Fatal("encrypting same payload twice reused ciphertext or wrapped DEK")
	}
}

func TestDecryptRejectsTampering(t *testing.T) {
	keyWrapper := testWrapper(t)
	aad := testAAD()
	encrypted, err := EncryptPayload(context.Background(), keyWrapper, KindGeneric, Payload{KeyValue: "secret"}, aad)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*EncryptedPayload, *AssociatedData)
	}{
		{name: "ciphertext", mutate: func(e *EncryptedPayload, _ *AssociatedData) { e.Ciphertext[0] ^= 0xff }},
		{name: "nonce", mutate: func(e *EncryptedPayload, _ *AssociatedData) { e.Nonce[0] ^= 0xff }},
		{name: "wrapped dek", mutate: func(e *EncryptedPayload, _ *AssociatedData) { e.EncryptedDEK[0] ^= 0xff }},
		{name: "wrapped dek nonce", mutate: func(e *EncryptedPayload, _ *AssociatedData) { e.EncryptedDEKNonce[0] ^= 0xff }},
		{name: "aad org", mutate: func(_ *EncryptedPayload, a *AssociatedData) { a.OrgID = "different" }},
		{name: "aad secret", mutate: func(_ *EncryptedPayload, a *AssociatedData) { a.SecretID = "different" }},
		{name: "aad version", mutate: func(_ *EncryptedPayload, a *AssociatedData) { a.VersionID = "different" }},
		{name: "aad version number", mutate: func(_ *EncryptedPayload, a *AssociatedData) { a.VersionNumber++ }},
		{name: "aad kind", mutate: func(_ *EncryptedPayload, a *AssociatedData) { a.Kind = KindOAuthTokenSet }},
		{
			name:   "payload keys",
			mutate: func(e *EncryptedPayload, _ *AssociatedData) { e.PayloadKeys = []string{KeyAccessToken} },
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			clone := cloneEnvelope(encrypted)
			cloneAAD := aad
			tt.mutate(&clone, &cloneAAD)
			if _, err := DecryptPayload(context.Background(), keyWrapper, clone, cloneAAD); err == nil {
				t.Fatal("expected decrypt to reject tampering")
			}
		})
	}
}

func TestDecryptRejectsEnvelopeRelocationAcrossVersions(t *testing.T) {
	keyWrapper := testWrapper(t)
	firstAAD := testAAD()
	secondAAD := testAAD()
	secondAAD.VersionID = "version-2"
	secondAAD.VersionNumber = 2
	first, err := EncryptPayload(context.Background(), keyWrapper, KindGeneric, Payload{KeyValue: "first"}, firstAAD)
	if err != nil {
		t.Fatalf("encrypt first: %v", err)
	}
	second, err := EncryptPayload(context.Background(), keyWrapper, KindGeneric, Payload{KeyValue: "second"}, secondAAD)
	if err != nil {
		t.Fatalf("encrypt second: %v", err)
	}
	relocated := first
	relocated.Ciphertext = second.Ciphertext
	relocated.Nonce = second.Nonce
	if _, err := DecryptPayload(context.Background(), keyWrapper, relocated, firstAAD); err == nil {
		t.Fatal("expected relocated payload ciphertext to fail decrypt")
	}
}

func TestValidatePayloadByKind(t *testing.T) {
	cases := []struct {
		name    string
		kind    Kind
		payload Payload
		wantErr bool
	}{
		{name: "generic", kind: KindGeneric, payload: Payload{KeyValue: "secret"}},
		{name: "generic extra", kind: KindGeneric, payload: Payload{KeyValue: "secret", KeyAccessToken: "no"}, wantErr: true},
		{name: "oauth access only", kind: KindOAuthTokenSet, payload: Payload{KeyAccessToken: "access"}},
		{
			name: "slack app credentials",
			kind: KindSlackAppCredentials,
			payload: Payload{
				KeyAccessToken:   "xoxb-token",
				KeyClientID:      "client-id",
				KeyClientSecret:  "client-secret",
				KeySigningSecret: "signing-secret",
			},
		},
		{
			name: "slack app credentials missing signing secret",
			kind: KindSlackAppCredentials,
			payload: Payload{
				KeyAccessToken:  "xoxb-token",
				KeyClientID:     "client-id",
				KeyClientSecret: "client-secret",
			},
			wantErr: true,
		},
		{
			name: "slack app credentials extra key",
			kind: KindSlackAppCredentials,
			payload: Payload{
				KeyAccessToken:   "xoxb-token",
				KeyClientID:      "client-id",
				KeyClientSecret:  "client-secret",
				KeySigningSecret: "signing-secret",
				KeyRefreshToken:  "refresh-token",
			},
			wantErr: true,
		},
		{
			name:    "oauth refresh without access",
			kind:    KindOAuthTokenSet,
			payload: Payload{KeyRefreshToken: "refresh"},
			wantErr: true,
		},
		{
			name:    "oversized value",
			kind:    KindGeneric,
			payload: Payload{KeyValue: strings.Repeat("x", MaxPayloadValueBytes+1)},
			wantErr: true,
		},
		{name: "unknown kind", kind: "api_key", payload: Payload{KeyValue: "secret"}, wantErr: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ValidatePayload(tt.kind, tt.payload)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidatePayload error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDecryptRejectsUnknownKey(t *testing.T) {
	keyWrapper := testWrapper(t)
	aad := testAAD()
	encrypted, err := EncryptPayload(context.Background(), keyWrapper, KindGeneric, Payload{KeyValue: "secret"}, aad)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	encrypted.KeyID = "missing"
	_, err = DecryptPayload(context.Background(), keyWrapper, encrypted, aad)
	if err == nil {
		t.Fatal("expected unknown key error")
	}
}

func TestDecryptRejectsWrongKeyMaterial(t *testing.T) {
	keyWrapper := testWrapper(t)
	aad := testAAD()
	encrypted, err := EncryptPayload(context.Background(), keyWrapper, KindGeneric, Payload{KeyValue: "secret"}, aad)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	wrongWrapper, err := NewLocalKeyWrapper(
		"test-key",
		map[string][]byte{"test-key": []byte("abcdef0123456789abcdef0123456789")},
	)
	if err != nil {
		t.Fatalf("new wrong keyWrapper: %v", err)
	}
	if _, err := DecryptPayload(context.Background(), wrongWrapper, encrypted, aad); err == nil {
		t.Fatal("expected wrong key material to fail decrypt")
	}
}

func TestDecryptRejectsUnsupportedEnvelopeFields(t *testing.T) {
	keyWrapper := testWrapper(t)
	aad := testAAD()
	encrypted, err := EncryptPayload(context.Background(), keyWrapper, KindGeneric, Payload{KeyValue: "secret"}, aad)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*EncryptedPayload)
	}{
		{name: "scheme", mutate: func(e *EncryptedPayload) { e.EncryptionScheme = "other" }},
		{name: "dek wrapper", mutate: func(e *EncryptedPayload) { e.DEKWrappedBy = "kms" }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			clone := cloneEnvelope(encrypted)
			tt.mutate(&clone)
			if _, err := DecryptPayload(context.Background(), keyWrapper, clone, aad); err == nil {
				t.Fatal("expected unsupported envelope field to fail decrypt")
			}
		})
	}
}

func TestDecryptRejectsMalformedNonceLengths(t *testing.T) {
	keyWrapper := testWrapper(t)
	aad := testAAD()
	encrypted, err := EncryptPayload(context.Background(), keyWrapper, KindGeneric, Payload{KeyValue: "secret"}, aad)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*EncryptedPayload)
	}{
		{name: "payload nonce", mutate: func(e *EncryptedPayload) { e.Nonce = []byte("short") }},
		{name: "wrapped dek nonce", mutate: func(e *EncryptedPayload) { e.EncryptedDEKNonce = []byte("short") }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			clone := cloneEnvelope(encrypted)
			tt.mutate(&clone)
			if _, err := DecryptPayload(context.Background(), keyWrapper, clone, aad); err == nil {
				t.Fatal("expected malformed nonce length to fail decrypt")
			}
		})
	}
}

func TestDecryptRejectsConfusedOrTruncatedEnvelopeFields(t *testing.T) {
	keyWrapper := testWrapper(t)
	aad := testAAD()
	encrypted, err := EncryptPayload(context.Background(), keyWrapper, KindGeneric, Payload{KeyValue: "secret"}, aad)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*EncryptedPayload)
	}{
		{name: "swap ciphertext and wrapped dek", mutate: func(e *EncryptedPayload) {
			e.Ciphertext, e.EncryptedDEK = e.EncryptedDEK, e.Ciphertext
		}},
		{name: "swap nonces", mutate: func(e *EncryptedPayload) {
			e.Nonce, e.EncryptedDEKNonce = e.EncryptedDEKNonce, e.Nonce
		}},
		{name: "empty ciphertext", mutate: func(e *EncryptedPayload) { e.Ciphertext = nil }},
		{name: "truncated ciphertext", mutate: func(e *EncryptedPayload) { e.Ciphertext = e.Ciphertext[:1] }},
		{name: "empty wrapped dek", mutate: func(e *EncryptedPayload) { e.EncryptedDEK = nil }},
		{name: "truncated wrapped dek", mutate: func(e *EncryptedPayload) { e.EncryptedDEK = e.EncryptedDEK[:1] }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			clone := cloneEnvelope(encrypted)
			tt.mutate(&clone)
			if _, err := DecryptPayload(context.Background(), keyWrapper, clone, aad); err == nil {
				t.Fatal("expected confused or truncated envelope to fail decrypt")
			}
		})
	}
}

func TestDecryptRejectsPayloadKeyEnvelopeMismatch(t *testing.T) {
	keyWrapper := testWrapper(t)
	dataKey := bytes.Repeat([]byte{9}, 32)
	aad := testAAD()
	for _, payloadKeys := range [][]string{{KeyAccessToken, KeyValue}, {KeyValue, KeyValue}} {
		envelopeAAD := aad
		envelopeAAD.PayloadKeys = payloadKeys
		associatedData, err := aadBytes(envelopeAAD)
		if err != nil {
			t.Fatalf("aad bytes: %v", err)
		}
		wrapped, err := keyWrapper.WrapDataKey(context.Background(), dataKey, associatedData)
		if err != nil {
			t.Fatalf("wrap data key: %v", err)
		}
		payloadBody, err := json.Marshal(Payload{KeyValue: "secret"})
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		payloadAEAD, err := newAESGCM(dataKey)
		if err != nil {
			t.Fatalf("payload AEAD: %v", err)
		}
		nonce := bytes.Repeat([]byte{4}, aesGCMNonceSize)
		encrypted := EncryptedPayload{
			EncryptionScheme:  EncryptionSchemeAES256GCMEnvelopeV1,
			KeyID:             wrapped.KeyID,
			DEKWrappedBy:      wrapped.WrappedBy,
			EncryptedDEK:      wrapped.Ciphertext,
			EncryptedDEKNonce: wrapped.Nonce,
			Nonce:             nonce,
			Ciphertext:        payloadAEAD.Seal(nil, nonce, payloadBody, associatedData),
			PayloadKeys:       envelopeAAD.PayloadKeys,
		}
		if _, err := DecryptPayload(context.Background(), keyWrapper, encrypted, aad); err == nil {
			t.Fatalf("expected payload key mismatch to fail decrypt for keys %v", payloadKeys)
		}
	}
}

func TestPayloadKeysAreOrderInsensitive(t *testing.T) {
	keyWrapper := testWrapper(t)
	aad := testAAD()
	aad.Kind = KindOAuthTokenSet
	payload := Payload{
		KeyAccessToken:   "access",
		KeyRefreshToken:  "refresh",
		KeyTokenEndpoint: "https://example.com/token",
		KeyClientID:      "client-id",
		KeyResource:      "https://example.com/resource",
	}
	encrypted, err := EncryptPayload(context.Background(), keyWrapper, KindOAuthTokenSet, payload, aad)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	encrypted.PayloadKeys = []string{KeyTokenEndpoint, KeyResource, KeyRefreshToken, KeyClientID, KeyAccessToken}
	decrypted, err := DecryptPayload(context.Background(), keyWrapper, encrypted, aad)
	if err != nil {
		t.Fatalf("decrypt reversed payload keys: %v", err)
	}
	if !reflect.DeepEqual(decrypted, payload) {
		t.Fatalf("decrypted = %#v, want %#v", decrypted, payload)
	}
}

func TestDecryptRejectsKindMismatch(t *testing.T) {
	keyWrapper := testWrapper(t)
	aad := testAAD()
	aad.Kind = KindOAuthTokenSet
	encrypted, err := EncryptPayload(
		context.Background(),
		keyWrapper,
		KindOAuthTokenSet,
		Payload{KeyAccessToken: "access"},
		aad,
	)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	wrongAAD := aad
	wrongAAD.Kind = KindGeneric
	if _, err := DecryptPayload(context.Background(), keyWrapper, encrypted, wrongAAD); err == nil {
		t.Fatal("expected kind mismatch to fail decrypt")
	}
}

func TestRejectsMalformedAssociatedData(t *testing.T) {
	keyWrapper := testWrapper(t)
	aad := testAAD()
	encrypted, err := EncryptPayload(context.Background(), keyWrapper, KindGeneric, Payload{KeyValue: "secret"}, aad)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	cases := []struct {
		name string
		aad  AssociatedData
	}{
		{
			name: "empty org",
			aad: AssociatedData{
				SecretID:      "secret",
				VersionID:     "version",
				VersionNumber: 1,
				Kind:          KindGeneric,
				PayloadKeys:   []string{KeyValue},
			},
		},
		{
			name: "empty secret",
			aad: AssociatedData{
				OrgID:         "org",
				VersionID:     "version",
				VersionNumber: 1,
				Kind:          KindGeneric,
				PayloadKeys:   []string{KeyValue},
			},
		},
		{
			name: "empty version",
			aad: AssociatedData{
				OrgID:         "org",
				SecretID:      "secret",
				VersionNumber: 1,
				Kind:          KindGeneric,
				PayloadKeys:   []string{KeyValue},
			},
		},
		{
			name: "zero version number",
			aad: AssociatedData{
				OrgID:       "org",
				SecretID:    "secret",
				VersionID:   "version",
				Kind:        KindGeneric,
				PayloadKeys: []string{KeyValue},
			},
		},
		{
			name: "empty kind",
			aad: AssociatedData{
				OrgID:         "org",
				SecretID:      "secret",
				VersionID:     "version",
				VersionNumber: 1,
				PayloadKeys:   []string{KeyValue},
			},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecryptPayload(context.Background(), keyWrapper, encrypted, tt.aad); err == nil {
				t.Fatal("expected malformed AAD to fail decrypt")
			}
			if _, err := RewrapPayloadKey(context.Background(), keyWrapper, encrypted, tt.aad); err == nil {
				t.Fatal("expected malformed AAD to fail rewrap")
			}
		})
	}
}

func TestLocalWrapperWrapUnwrapRoundTrip(t *testing.T) {
	keyWrapper := testWrapper(t)
	dataKey := bytes.Repeat([]byte{7}, 32)
	associatedData := []byte("aad")
	wrapped, err := keyWrapper.WrapDataKey(context.Background(), dataKey, associatedData)
	if err != nil {
		t.Fatalf("wrap data key: %v", err)
	}
	unwrapped, err := keyWrapper.UnwrapDataKey(context.Background(), wrapped, associatedData)
	if err != nil {
		t.Fatalf("unwrap data key: %v", err)
	}
	if !bytes.Equal(unwrapped, dataKey) {
		t.Fatal("unwrapped data key differs from original")
	}
}

func TestLocalWrapperBindsAssociatedData(t *testing.T) {
	keyWrapper := testWrapper(t)
	dataKey := bytes.Repeat([]byte{7}, 32)
	wrapped, err := keyWrapper.WrapDataKey(context.Background(), dataKey, []byte("aad-a"))
	if err != nil {
		t.Fatalf("wrap data key: %v", err)
	}
	if _, err := keyWrapper.UnwrapDataKey(context.Background(), wrapped, []byte("aad-b")); err == nil {
		t.Fatal("expected unwrap with different associated data to fail")
	}
}

func TestEncryptAndRewrapUseDistinctNonces(t *testing.T) {
	oldWrapper, err := NewLocalKeyWrapper(
		"old-key",
		map[string][]byte{"old-key": []byte("0123456789abcdef0123456789abcdef")},
	)
	if err != nil {
		t.Fatalf("new old wrapper: %v", err)
	}
	rotatedWrapper, err := NewLocalKeyWrapper("new-key", map[string][]byte{
		"old-key": []byte("0123456789abcdef0123456789abcdef"),
		"new-key": []byte("abcdef0123456789abcdef0123456789"),
	})
	if err != nil {
		t.Fatalf("new rotated wrapper: %v", err)
	}
	payloadNonces := map[string]bool{}
	dekNonces := map[string]bool{}
	for i := 1; i <= 1000; i++ {
		aad := testAAD()
		aad.VersionID = "version-" + strconv.Itoa(i)
		aad.VersionNumber = int32(i)
		encrypted, err := EncryptPayload(context.Background(), oldWrapper, KindGeneric, Payload{KeyValue: "secret"}, aad)
		if err != nil {
			t.Fatalf("encrypt %d: %v", i, err)
		}
		if payloadNonces[string(encrypted.Nonce)] {
			t.Fatalf("duplicate payload nonce at iteration %d", i)
		}
		payloadNonces[string(encrypted.Nonce)] = true
		if dekNonces[string(encrypted.EncryptedDEKNonce)] {
			t.Fatalf("duplicate wrapped DEK nonce at iteration %d", i)
		}
		dekNonces[string(encrypted.EncryptedDEKNonce)] = true
		rewrapped, err := RewrapPayloadKey(context.Background(), rotatedWrapper, encrypted, aad)
		if err != nil {
			t.Fatalf("rewrap %d: %v", i, err)
		}
		if dekNonces[string(rewrapped.EncryptedDEKNonce)] {
			t.Fatalf("duplicate rewrapped DEK nonce at iteration %d", i)
		}
		dekNonces[string(rewrapped.EncryptedDEKNonce)] = true
	}
}

func TestLocalWrapperSupportsKeyRotation(t *testing.T) {
	oldWrapper, err := NewLocalKeyWrapper(
		"old-key",
		map[string][]byte{"old-key": []byte("0123456789abcdef0123456789abcdef")},
	)
	if err != nil {
		t.Fatalf("new old keyWrapper: %v", err)
	}
	aad := testAAD()
	payload := Payload{KeyValue: "secret"}
	oldEncrypted, err := EncryptPayload(context.Background(), oldWrapper, KindGeneric, payload, aad)
	if err != nil {
		t.Fatalf("encrypt old: %v", err)
	}
	if oldEncrypted.KeyID != "old-key" {
		t.Fatalf("old key id = %q, want old-key", oldEncrypted.KeyID)
	}

	rotatedWrapper, err := NewLocalKeyWrapper("new-key", map[string][]byte{
		"old-key": []byte("0123456789abcdef0123456789abcdef"),
		"new-key": []byte("abcdef0123456789abcdef0123456789"),
	})
	if err != nil {
		t.Fatalf("new rotated keyWrapper: %v", err)
	}
	decrypted, err := DecryptPayload(context.Background(), rotatedWrapper, oldEncrypted, aad)
	if err != nil {
		t.Fatalf("decrypt old with rotated keyWrapper: %v", err)
	}
	if !reflect.DeepEqual(decrypted, payload) {
		t.Fatalf("decrypted old payload = %#v, want %#v", decrypted, payload)
	}
	newEncrypted, err := EncryptPayload(context.Background(), rotatedWrapper, KindGeneric, payload, aad)
	if err != nil {
		t.Fatalf("encrypt new: %v", err)
	}
	if newEncrypted.KeyID != "new-key" {
		t.Fatalf("new key id = %q, want new-key", newEncrypted.KeyID)
	}
}

func TestRewrapPayloadKeyReturnsUnchangedEnvelopeForActiveKey(t *testing.T) {
	keyWrapper := testWrapper(t)
	aad := testAAD()
	encrypted, err := EncryptPayload(context.Background(), keyWrapper, KindGeneric, Payload{KeyValue: "secret"}, aad)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	rewrapped, err := RewrapPayloadKey(context.Background(), keyWrapper, encrypted, aad)
	if err != nil {
		t.Fatalf("rewrap active key: %v", err)
	}
	if !reflect.DeepEqual(rewrapped, encrypted) {
		t.Fatal("active-key rewrap changed envelope")
	}
}

func TestRewrapPayloadKeyRotatesEnvelopeWithoutReencryptingPayload(t *testing.T) {
	oldWrapper, err := NewLocalKeyWrapper(
		"old-key",
		map[string][]byte{"old-key": []byte("0123456789abcdef0123456789abcdef")},
	)
	if err != nil {
		t.Fatalf("new old keyWrapper: %v", err)
	}
	aad := testAAD()
	payload := Payload{KeyValue: "secret"}
	encrypted, err := EncryptPayload(context.Background(), oldWrapper, KindGeneric, payload, aad)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	rotatedWrapper, err := NewLocalKeyWrapper("new-key", map[string][]byte{
		"old-key": []byte("0123456789abcdef0123456789abcdef"),
		"new-key": []byte("abcdef0123456789abcdef0123456789"),
	})
	if err != nil {
		t.Fatalf("new rotated keyWrapper: %v", err)
	}
	rewrapped, err := RewrapPayloadKey(context.Background(), rotatedWrapper, encrypted, aad)
	if err != nil {
		t.Fatalf("rewrap: %v", err)
	}
	if rewrapped.KeyID != "new-key" {
		t.Fatalf("rewrapped key id = %q, want new-key", rewrapped.KeyID)
	}
	if !bytes.Equal(rewrapped.Ciphertext, encrypted.Ciphertext) || !bytes.Equal(rewrapped.Nonce, encrypted.Nonce) {
		t.Fatal("rewrap changed payload ciphertext")
	}
	if bytes.Equal(rewrapped.EncryptedDEK, encrypted.EncryptedDEK) ||
		bytes.Equal(rewrapped.EncryptedDEKNonce, encrypted.EncryptedDEKNonce) {
		t.Fatal("rewrap reused wrapped DEK or nonce")
	}
	decrypted, err := DecryptPayload(context.Background(), rotatedWrapper, rewrapped, aad)
	if err != nil {
		t.Fatalf("decrypt rewrapped: %v", err)
	}
	if !reflect.DeepEqual(decrypted, payload) {
		t.Fatalf("decrypted rewrapped payload = %#v, want %#v", decrypted, payload)
	}
	newOnlyWrapper, err := NewLocalKeyWrapper(
		"new-key",
		map[string][]byte{"new-key": []byte("abcdef0123456789abcdef0123456789")},
	)
	if err != nil {
		t.Fatalf("new-only keyWrapper: %v", err)
	}
	if _, err := DecryptPayload(context.Background(), newOnlyWrapper, rewrapped, aad); err != nil {
		t.Fatalf("decrypt rewrapped without old key: %v", err)
	}
}

func TestLocalWrapperRequiresConfiguredKey(t *testing.T) {
	if _, err := NewLocalKeyWrapper("", map[string][]byte{"key": bytes.Repeat([]byte{1}, 32)}); err == nil {
		t.Fatal("expected missing key id error")
	}
	if _, err := NewLocalKeyWrapper("key", map[string][]byte{"key": []byte("short")}); err == nil {
		t.Fatal("expected short key error")
	}
	if _, err := NewLocalKeyWrapper("missing", map[string][]byte{"key": bytes.Repeat([]byte{1}, 32)}); err == nil {
		t.Fatal("expected missing active key error")
	}
	if _, err := NewLocalKeyWrapper(
		"key-a",
		map[string][]byte{"key-a": bytes.Repeat([]byte{1}, 32), "key-b": bytes.Repeat([]byte{1}, 32)},
	); err == nil {
		t.Fatal("expected duplicate key material error")
	}
	var keyWrapper *LocalKeyWrapper
	if _, err := keyWrapper.ActiveKeyID(context.Background()); err == nil {
		t.Fatal("expected nil keyWrapper error")
	}
	if _, err := keyWrapper.WrapDataKey(context.Background(), bytes.Repeat([]byte{1}, 32), []byte("aad")); err == nil {
		t.Fatal("expected nil keyWrapper wrap error")
	}
	if _, err := keyWrapper.UnwrapDataKey(
		context.Background(),
		WrappedDataKey{
			KeyID:      "key",
			WrappedBy:  DEKWrappedByLocal,
			Ciphertext: []byte("ciphertext"),
			Nonce:      bytes.Repeat([]byte{1}, 12),
		},
		[]byte("aad"),
	); err == nil {
		t.Fatal("expected nil keyWrapper unwrap error")
	}
	wrapper, err := NewLocalKeyWrapper("key", map[string][]byte{"key": bytes.Repeat([]byte{1}, 32)})
	if err != nil {
		t.Fatalf("new keyWrapper: %v", err)
	}
	if _, err := wrapper.WrapDataKey(context.Background(), []byte("short"), []byte("aad")); err == nil {
		t.Fatal("expected short data key error")
	}
	if _, err := wrapper.UnwrapDataKey(
		context.Background(),
		WrappedDataKey{
			KeyID:      "missing",
			WrappedBy:  DEKWrappedByLocal,
			Ciphertext: []byte("ciphertext"),
			Nonce:      bytes.Repeat([]byte{1}, 12),
		},
		[]byte("aad"),
	); err == nil {
		t.Fatal("expected unknown key unwrap error")
	}
	if _, err := EncryptPayload(
		context.Background(),
		nil,
		KindGeneric,
		Payload{KeyValue: "secret"},
		testAAD(),
	); err == nil {
		t.Fatal("expected nil keyWrapper encrypt error")
	}
	if _, err := DecryptPayload(
		context.Background(),
		nil,
		EncryptedPayload{EncryptionScheme: EncryptionSchemeAES256GCMEnvelopeV1},
		testAAD(),
	); err == nil {
		t.Fatal("expected nil keyWrapper decrypt error")
	}
	if _, err := RewrapPayloadKey(
		context.Background(),
		nil,
		EncryptedPayload{EncryptionScheme: EncryptionSchemeAES256GCMEnvelopeV1},
		testAAD(),
	); err == nil {
		t.Fatal("expected nil keyWrapper rewrap error")
	}
}

func TestDecryptAndRewrapRejectShortUnwrappedDataKey(t *testing.T) {
	goodWrapper := testWrapper(t)
	aad := testAAD()
	encrypted, err := EncryptPayload(context.Background(), goodWrapper, KindGeneric, Payload{KeyValue: "secret"}, aad)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	badWrapper := shortDataKeyWrapper{activeKeyID: "new-key"}
	if _, err := DecryptPayload(context.Background(), badWrapper, encrypted, aad); err == nil {
		t.Fatal("expected short unwrapped data key to fail decrypt")
	}
	if _, err := RewrapPayloadKey(context.Background(), badWrapper, encrypted, aad); err == nil {
		t.Fatal("expected short unwrapped data key to fail rewrap")
	}
}

func TestEncryptAndRewrapRejectNonInvertibleWrapper(t *testing.T) {
	goodWrapper := testWrapper(t)
	aad := testAAD()
	encrypted, err := EncryptPayload(context.Background(), goodWrapper, KindGeneric, Payload{KeyValue: "secret"}, aad)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	badWrapper := nonInvertibleWrapper{base: goodWrapper, activeKeyID: "new-key"}
	if _, err := EncryptPayload(
		context.Background(),
		badWrapper,
		KindGeneric,
		Payload{KeyValue: "secret"},
		aad,
	); err == nil {
		t.Fatal("expected non-invertible wrapper to fail encrypt")
	}
	if _, err := RewrapPayloadKey(context.Background(), badWrapper, encrypted, aad); err == nil {
		t.Fatal("expected non-invertible wrapper to fail rewrap")
	}
}

func testWrapper(t *testing.T) *LocalKeyWrapper {
	t.Helper()
	keyWrapper, err := NewLocalKeyWrapper(
		"test-key",
		map[string][]byte{"test-key": []byte("0123456789abcdef0123456789abcdef")},
	)
	if err != nil {
		t.Fatalf("new keyWrapper: %v", err)
	}
	return keyWrapper
}

type shortDataKeyWrapper struct {
	activeKeyID string
}

func (w shortDataKeyWrapper) ActiveKeyID(context.Context) (string, error) {
	return w.activeKeyID, nil
}

func (w shortDataKeyWrapper) WrapDataKey(context.Context, []byte, []byte) (WrappedDataKey, error) {
	return WrappedDataKey{}, errors.New("wrap should not be called")
}

func (w shortDataKeyWrapper) UnwrapDataKey(context.Context, WrappedDataKey, []byte) ([]byte, error) {
	return []byte("sixteen-byte-key"), nil
}

type nonInvertibleWrapper struct {
	base        KeyWrapper
	activeKeyID string
}

func (w nonInvertibleWrapper) ActiveKeyID(context.Context) (string, error) {
	return w.activeKeyID, nil
}

func (w nonInvertibleWrapper) WrapDataKey(
	ctx context.Context,
	dataKey []byte,
	associatedData []byte,
) (WrappedDataKey, error) {
	wrapped, err := w.base.WrapDataKey(ctx, dataKey, associatedData)
	if err != nil {
		return WrappedDataKey{}, err
	}
	wrapped.KeyID = w.activeKeyID
	return wrapped, nil
}

func (w nonInvertibleWrapper) UnwrapDataKey(
	ctx context.Context,
	wrapped WrappedDataKey,
	associatedData []byte,
) ([]byte, error) {
	if wrapped.KeyID != w.activeKeyID {
		return w.base.UnwrapDataKey(ctx, wrapped, associatedData)
	}
	return bytes.Repeat([]byte{3}, dataKeySize), nil
}

func testAAD() AssociatedData {
	return AssociatedData{
		OrgID:         "org",
		SecretID:      "secret",
		VersionID:     "version",
		VersionNumber: 1,
		Kind:          KindGeneric,
		PayloadKeys:   []string{KeyValue},
	}
}

func cloneEnvelope(encrypted EncryptedPayload) EncryptedPayload {
	encrypted.EncryptedDEK = append([]byte(nil), encrypted.EncryptedDEK...)
	encrypted.EncryptedDEKNonce = append([]byte(nil), encrypted.EncryptedDEKNonce...)
	encrypted.Nonce = append([]byte(nil), encrypted.Nonce...)
	encrypted.Ciphertext = append([]byte(nil), encrypted.Ciphertext...)
	encrypted.PayloadKeys = append([]string(nil), encrypted.PayloadKeys...)
	return encrypted
}
