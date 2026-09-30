package modelenvelope

import (
	"encoding/hex"
	"errors"
)

const RequestInputIdentityVersion = 1

type RequestInputIdentity struct {
	Version           int
	RouteFingerprint  string
	StaticFingerprint string
	PrefixFingerprint string
	ItemCount         int
}

func (i RequestInputIdentity) Validate() error {
	if i.Version != RequestInputIdentityVersion || i.ItemCount <= 0 {
		return errors.New("request input identity has an unsupported version or empty input")
	}
	for _, fingerprint := range []string{i.RouteFingerprint, i.StaticFingerprint, i.PrefixFingerprint} {
		if len(fingerprint) != 64 {
			return errors.New("request input identity requires SHA-256 fingerprints")
		}
		if _, err := hex.DecodeString(fingerprint); err != nil {
			return errors.New("request input identity has an invalid fingerprint")
		}
	}
	return nil
}
