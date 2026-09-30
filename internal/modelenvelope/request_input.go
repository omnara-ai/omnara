package modelenvelope

import (
	"encoding/hex"
	"errors"
)

type RequestInputIdentity struct {
	Fingerprint string
	ItemCount   int
}

func (i RequestInputIdentity) Validate() error {
	if i.ItemCount <= 0 {
		return errors.New("request input identity requires nonempty input")
	}
	if len(i.Fingerprint) != 64 {
		return errors.New("request input identity requires a SHA-256 fingerprint")
	}
	if _, err := hex.DecodeString(i.Fingerprint); err != nil {
		return errors.New("request input identity has an invalid fingerprint")
	}
	return nil
}
