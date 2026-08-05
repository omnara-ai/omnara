package storage

import (
	"fmt"

	"github.com/google/uuid"
)

type ID = uuid.UUID

var NilID = uuid.Nil

func ParseID(value string) (ID, error) {
	id, err := uuid.Parse(value)
	if err != nil {
		return NilID, fmt.Errorf("parse canonical uuid: %w", err)
	}
	return id, nil
}
