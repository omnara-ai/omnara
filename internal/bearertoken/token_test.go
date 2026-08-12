package bearertoken

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestGoldenVectors(t *testing.T) {
	type vector struct {
		Kind   Kind   `json:"kind"`
		Secret string `json:"secret"`
		Token  string `json:"token"`
	}
	var fixture struct {
		Alphabet                string `json:"alphabet"`
		SecretLength            int    `json:"secret_length"`
		ChecksumLength          int    `json:"checksum_length"`
		ChecksumEncodingVectors []struct {
			Value   uint32 `json:"value"`
			Encoded string `json:"encoded"`
		} `json:"checksum_encoding_vectors"`
		Vectors []vector `json:"vectors"`
	}
	body, err := os.ReadFile("../../testdata/bearer-token-v1.json")
	if err != nil {
		t.Fatalf("read shared golden vectors: %v", err)
	}
	if err := json.Unmarshal(body, &fixture); err != nil {
		t.Fatalf("decode shared golden vectors: %v", err)
	}
	if fixture.Alphabet != alphabet || fixture.SecretLength != secretLength ||
		fixture.ChecksumLength != checksumLength {
		t.Fatalf("shared token parameters do not match implementation: %+v", fixture)
	}
	if len(fixture.Vectors) != 12 {
		t.Fatalf("golden vector count = %d, want 12", len(fixture.Vectors))
	}
	for _, test := range fixture.Vectors {
		got, err := format(test.Kind, test.Secret)
		if err != nil {
			t.Fatalf("format %s token: %v", test.Kind, err)
		}
		if got != test.Token {
			t.Fatalf("format %s token = %q, want %q", test.Kind, got, test.Token)
		}
		parsed, err := Parse(got)
		if err != nil || parsed != test.Kind {
			t.Fatalf("parse %s golden token = %q, %v", test.Kind, parsed, err)
		}
	}
	for _, test := range fixture.ChecksumEncodingVectors {
		if got := encodeChecksum(test.Value); got != test.Encoded {
			t.Fatalf("encode checksum %d = %q, want %q", test.Value, got, test.Encoded)
		}
	}
}

func TestGenerateProducesCanonicalDistinctTokens(t *testing.T) {
	for _, kind := range []Kind{KindPersonalAccess, KindOrganization, KindDaemon} {
		t.Run(string(kind), func(t *testing.T) {
			seen := make(map[string]struct{}, 128)
			for range 128 {
				token, err := Generate(kind)
				if err != nil {
					t.Fatalf("generate token: %v", err)
				}
				if err := Validate(token, kind); err != nil {
					t.Fatalf("validate generated token %q: %v", token, err)
				}
				if _, duplicate := seen[token]; duplicate {
					t.Fatalf("generated duplicate token %q", token)
				}
				seen[token] = struct{}{}
			}
		})
	}
}

func TestParseRejectsMalformedTokens(t *testing.T) {
	valid, err := format(KindPersonalAccess, strings.Repeat("A", secretLength))
	if err != nil {
		t.Fatal(err)
	}
	secretEnd := len(personalAccessPrefix) + secretLength
	tests := []struct {
		name  string
		token string
	}{
		{name: "empty"},
		{name: "legacy", token: "omnara_pat_id_secret"},
		{name: "wrong version", token: strings.Replace(valid, "_v1_", "_v2_", 1)},
		{name: "short secret", token: valid[:secretEnd-1] + valid[secretEnd:]},
		{name: "long secret", token: valid[:secretEnd] + "A" + valid[secretEnd:]},
		{name: "secret underscore", token: valid[:len(personalAccessPrefix)+5] + "_" + valid[len(personalAccessPrefix)+6:]},
		{name: "secret hyphen", token: valid[:len(personalAccessPrefix)+5] + "-" + valid[len(personalAccessPrefix)+6:]},
		{name: "missing separator", token: valid[:secretEnd] + "A" + valid[secretEnd+1:]},
		{name: "checksum alphabet", token: valid[:len(valid)-1] + "-"},
		{name: "checksum mismatch", token: valid[:len(valid)-1] + differentBase62(valid[len(valid)-1])},
		{name: "non ascii", token: valid[:len(personalAccessPrefix)+5] + "é" + valid[len(personalAccessPrefix)+6:]},
		{name: "trailing data", token: valid + "A"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Parse(test.token); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Parse(%q) error = %v, want ErrInvalid", test.token, err)
			}
			if _, err := Parse(test.token); test.token != "" && err != nil && strings.Contains(err.Error(), test.token) {
				t.Fatalf("Parse error disclosed token %q: %v", test.token, err)
			}
		})
	}
}

func TestValidateRejectsWrongKind(t *testing.T) {
	token, err := format(KindOrganization, strings.Repeat("B", secretLength))
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(token, KindPersonalAccess); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Validate wrong kind error = %v, want ErrInvalid", err)
	}
}

func TestFormatRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name   string
		kind   Kind
		secret string
	}{
		{name: "kind", kind: "unknown", secret: strings.Repeat("A", secretLength)},
		{name: "short", kind: KindDaemon, secret: strings.Repeat("A", secretLength-1)},
		{name: "non base62", kind: KindDaemon, secret: strings.Repeat("A", secretLength-1) + "_"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := format(test.kind, test.secret); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Format error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestGenerateSecretUsesRejectionSampling(t *testing.T) {
	source := []byte{248, 249, 250, 251, 252, 253, 254, 255, 0, 61, 62, 247}
	for len(source) < secretLength+8 {
		source = append(source, 0)
	}
	secret, err := generateSecret(strings.NewReader(string(source)))
	if err != nil {
		t.Fatalf("generate secret: %v", err)
	}
	if len(secret) != secretLength {
		t.Fatalf("secret length = %d, want %d", len(secret), secretLength)
	}
	if want := "0z0z"; secret[:len(want)] != want {
		t.Fatalf("secret begins %q, want rejected bytes followed by %q", secret[:len(want)], want)
	}
}

func TestGenerateSecretAcceptsFinalBytesWithEOF(t *testing.T) {
	secret, err := generateSecret(&finalEOFReader{data: make([]byte, secretLength)})
	if err != nil {
		t.Fatalf("generate secret from final EOF read: %v", err)
	}
	if secret != strings.Repeat("0", secretLength) {
		t.Fatalf("secret = %q, want zero-vector secret", secret)
	}
}

func TestChecksumEncodingBoundaries(t *testing.T) {
	if got := encodeChecksum(0); got != "000000" {
		t.Fatalf("encode zero checksum = %q, want 000000", got)
	}
	if got := encodeChecksum(^uint32(0)); got != "4gfFC3" {
		t.Fatalf("encode max checksum = %q, want 4gfFC3", got)
	}
}

func TestGenerateSecretRejectsStalledOrFailedReader(t *testing.T) {
	if _, err := generateSecret(stalledReader{}); !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("stalled reader error = %v, want io.ErrNoProgress", err)
	}
	want := errors.New("entropy unavailable")
	if _, err := generateSecret(errorReader{err: want}); !errors.Is(err, want) {
		t.Fatalf("failed reader error = %v, want %v", err, want)
	}
}

func differentBase62(value byte) string {
	if value == '0' {
		return "1"
	}
	return "0"
}

type stalledReader struct{}

func (stalledReader) Read([]byte) (int, error) { return 0, nil }

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

type finalEOFReader struct {
	data []byte
	done bool
}

func (r *finalEOFReader) Read(target []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	return copy(target, r.data), io.EOF
}
