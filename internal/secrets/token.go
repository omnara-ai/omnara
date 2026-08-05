package secrets

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	sealedTokenVersionV1   = 1
	sealedTokenAADPrefix   = "omnara-sealed-token/v1/"
	maxSealedTokenFieldLen = 65535
)

var errInvalidSealedToken = errors.New("invalid sealed token")

func SealToken(ctx context.Context, keyWrapper KeyWrapper, purpose string, plaintext []byte) (string, error) {
	if keyWrapper == nil {
		return "", errors.New("key wrapper is required")
	}
	if purpose == "" {
		return "", errors.New("sealed token purpose is required")
	}
	if len(plaintext) == 0 {
		return "", errors.New("sealed token plaintext is required")
	}
	aad := []byte(sealedTokenAADPrefix + purpose)
	dek, err := randomBytes(dataKeySize)
	if err != nil {
		return "", err
	}
	wrapped, err := keyWrapper.WrapDataKey(ctx, dek, aad)
	if err != nil {
		return "", err
	}
	nonce, err := randomBytes(aesGCMNonceSize)
	if err != nil {
		return "", err
	}
	aead, err := newAESGCM(dek)
	if err != nil {
		return "", err
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, aad)
	var token []byte
	token = append(token, sealedTokenVersionV1)
	for _, field := range [][]byte{
		[]byte(wrapped.KeyID),
		[]byte(wrapped.WrappedBy),
		wrapped.Ciphertext,
		wrapped.Nonce,
		nonce,
		ciphertext,
	} {
		if len(field) > maxSealedTokenFieldLen {
			return "", fmt.Errorf("sealed token field exceeds %d bytes", maxSealedTokenFieldLen)
		}
		token = binary.BigEndian.AppendUint16(token, uint16(len(field)))
		token = append(token, field...)
	}
	return base64.RawURLEncoding.EncodeToString(token), nil
}

func OpenToken(ctx context.Context, keyWrapper KeyWrapper, purpose string, token string) ([]byte, error) {
	if keyWrapper == nil {
		return nil, errors.New("key wrapper is required")
	}
	if purpose == "" {
		return nil, errors.New("sealed token purpose is required")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, errInvalidSealedToken
	}
	if len(raw) < 1 || raw[0] != sealedTokenVersionV1 {
		return nil, errInvalidSealedToken
	}
	rest := raw[1:]
	fields := make([][]byte, 6)
	for i := range fields {
		if len(rest) < 2 {
			return nil, errInvalidSealedToken
		}
		length := int(binary.BigEndian.Uint16(rest))
		rest = rest[2:]
		if len(rest) < length {
			return nil, errInvalidSealedToken
		}
		fields[i] = rest[:length]
		rest = rest[length:]
	}
	if len(rest) != 0 {
		return nil, errInvalidSealedToken
	}
	keyID, wrappedBy := fields[0], fields[1]
	encryptedDEK, dekNonce := fields[2], fields[3]
	nonce, ciphertext := fields[4], fields[5]
	aad := []byte(sealedTokenAADPrefix + purpose)
	dek, err := keyWrapper.UnwrapDataKey(
		ctx,
		WrappedDataKey{KeyID: string(keyID), WrappedBy: string(wrappedBy), Ciphertext: encryptedDEK, Nonce: dekNonce},
		aad,
	)
	if err != nil {
		return nil, errInvalidSealedToken
	}
	aead, err := newAESGCM(dek)
	if err != nil {
		return nil, err
	}
	if err := validateNonce("sealed token nonce", nonce, aead); err != nil {
		return nil, errInvalidSealedToken
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, errInvalidSealedToken
	}
	return plaintext, nil
}
