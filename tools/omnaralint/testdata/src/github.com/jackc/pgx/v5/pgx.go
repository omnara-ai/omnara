package pgx

type Row interface{ Scan(...any) error }
type Batch struct{}
type BatchResults interface{ Close() error }

type Identifier []string
type CopyFromSource interface{ Next() bool }
