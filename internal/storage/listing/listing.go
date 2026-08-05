package listing

import (
	"time"

	"github.com/google/uuid"
)

const timestampLayout = "2006-01-02T15:04:05.000000"

type KeysetCursor struct {
	Set       bool
	CreatedAt time.Time
	ID        uuid.UUID
}

type Options struct {
	NamePattern string
	SortField   string
	SortDesc    bool
	After       Cursor
}

type Cursor struct {
	Set    bool
	IsNull bool
	Key    string
	ID     uuid.UUID
}

func SortAllowed(got string, allowed ...string) bool {
	for _, candidate := range allowed {
		if got == candidate {
			return true
		}
	}
	return false
}

func Normalize(options Options) Options {
	if options.SortField == "" {
		options.SortField = "created_at"
		options.SortDesc = true
	}
	return options
}

func TimestampKey(value time.Time) string {
	return value.UTC().Format(timestampLayout)
}

func ParseTimestampKey(value string) (time.Time, error) {
	return time.ParseInLocation(timestampLayout, value, time.UTC)
}
