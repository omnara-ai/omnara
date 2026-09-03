package channelconnector

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/omnara-ai/omnara/internal/bearertoken"
	"github.com/omnara-ai/omnara/internal/registryname"
)

type Config struct {
	ID           string       `json:"id"`
	Token        string       `json:"token"`
	Capabilities []Capability `json:"capabilities"`
}

type Identity struct {
	ID           string
	Capabilities []Capability
}

type Capability struct {
	ConnectorKey string `json:"connector_key"`
	Provider     string `json:"provider"`
}

type credential struct {
	identity Identity
	hash     [sha256.Size]byte
}

type Authenticator struct {
	credentials []credential
}

func NewAuthenticator(configs []Config) (*Authenticator, error) {
	seenIDs := make(map[string]struct{}, len(configs))
	seenHashes := make(map[[sha256.Size]byte]struct{}, len(configs))
	credentials := make([]credential, 0, len(configs))
	for index, config := range configs {
		normalized, err := normalizeConfig(config)
		if err != nil {
			return nil, fmt.Errorf("channel connector %d: %w", index, err)
		}
		if _, exists := seenIDs[normalized.ID]; exists {
			return nil, fmt.Errorf("channel connector id %q is duplicated", normalized.ID)
		}
		hash := sha256.Sum256([]byte(normalized.Token))
		if _, exists := seenHashes[hash]; exists {
			return nil, errors.New("channel connector token is duplicated")
		}
		seenIDs[normalized.ID] = struct{}{}
		seenHashes[hash] = struct{}{}
		credentials = append(credentials, credential{
			identity: Identity{
				ID: normalized.ID, Capabilities: normalized.Capabilities,
			},
			hash: hash,
		})
	}
	return &Authenticator{credentials: credentials}, nil
}

func (a *Authenticator) Authenticate(token string) (Identity, error) {
	if a == nil || bearertoken.Validate(token, bearertoken.KindChannelConnector) != nil {
		return Identity{}, errors.New("unauthorized channel connector")
	}
	presented := sha256.Sum256([]byte(token))
	matched := -1
	for index := range a.credentials {
		if subtle.ConstantTimeCompare(presented[:], a.credentials[index].hash[:]) == 1 {
			matched = index
		}
	}
	if matched < 0 {
		return Identity{}, errors.New("unauthorized channel connector")
	}
	identity := a.credentials[matched].identity
	identity.Capabilities = slices.Clone(identity.Capabilities)
	return identity, nil
}

func normalizeConfig(config Config) (Config, error) {
	config.ID = strings.TrimSpace(config.ID)
	if !registryname.Valid(config.ID) {
		return Config{}, errors.New("id must be a lowercase registry name")
	}
	if err := bearertoken.Validate(config.Token, bearertoken.KindChannelConnector); err != nil {
		return Config{}, errors.New("token must be a valid channel connector token")
	}
	var err error
	config.Capabilities, err = NormalizeCapabilities(config.Capabilities)
	if err != nil {
		return Config{}, err
	}
	return config, nil
}

func NormalizeCapabilities(values []Capability) ([]Capability, error) {
	if len(values) == 0 || len(values) > 64 {
		return nil, errors.New("capabilities must contain between 1 and 64 values")
	}
	out := make([]Capability, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value.ConnectorKey = strings.TrimSpace(value.ConnectorKey)
		value.Provider = strings.TrimSpace(value.Provider)
		if !registryname.Valid(value.ConnectorKey) || !registryname.Valid(value.Provider) {
			return nil, errors.New("capabilities must contain lowercase registry names")
		}
		if strings.HasPrefix(value.ConnectorKey, "native_") {
			return nil, errors.New("native connector keys cannot be delegated")
		}
		key := value.ConnectorKey + "\x00" + value.Provider
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out, nil
}
