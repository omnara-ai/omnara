package dbsafe

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// ErrInvalidText matches the errors Text and JSONStrings return for text that
// Postgres cannot store.
var ErrInvalidText = errors.New("text cannot be stored")

type invalidTextError string

func (err invalidTextError) Error() string { return string(err) }

func (invalidTextError) Is(target error) bool { return target == ErrInvalidText }

func Text(value string) error {
	if !utf8.ValidString(value) {
		return invalidTextError("contains invalid UTF-8")
	}
	if strings.IndexByte(value, 0) >= 0 {
		return invalidTextError("contains U+0000")
	}
	return nil
}

func JSONStrings(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("decode JSON: %w", err)
		}
		if value, ok := token.(string); ok {
			if err := Text(value); err != nil {
				return err
			}
		}
	}
}
