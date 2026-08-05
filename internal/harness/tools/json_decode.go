package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

func decodeSingleStrictJSON(raw json.RawMessage, dst any, label string) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New(label + " must contain a single JSON value")
	} else if !errors.Is(err, io.EOF) {
		return errors.New(label + " must contain a single JSON value")
	}
	return nil
}
