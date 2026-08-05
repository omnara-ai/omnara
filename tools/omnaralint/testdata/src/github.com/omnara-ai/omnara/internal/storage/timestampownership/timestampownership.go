package timestampownership

import (
	faketime "example.com/faketime"
	stdtime "time"
)

type MutationInput struct {
	Now        stdtime.Time  // want "direct time.Time fields on storage Input types must be explicit Source"
	DeadlineAt stdtime.Time  // want "direct time.Time fields on storage Input types must be explicit Source"
	ExpiresAt  *stdtime.Time // want "direct time.Time fields on storage Input types must be explicit Source"
}

type SourceInput struct {
	SourceStartedAt stdtime.Time
	SourceEndedAt   *stdtime.Time
}

type QueueCursor struct {
	QueuedAt stdtime.Time
}

type ListQueueInput struct {
	After QueueCursor
}

type TimeAlias = stdtime.Time

type TimePointerAlias = *stdtime.Time

type SimilarStore struct{}

func (*SimilarStore) CompleteAt(endedAt stdtime.Time) {}

type ExistingRecord struct {
	DeadlineAt stdtime.Time
}

type ReusedRecordInput ExistingRecord // want "storage Input types must declare an explicit struct"

type AliasedInput struct {
	ScheduledAt TimeAlias        // want "direct time.Time fields on storage Input types must be explicit Source"
	ExpiresAt   TimePointerAlias // want "direct time.Time fields on storage Input types must be explicit Source"
}

type localClock struct{}

func (localClock) Now() stdtime.Time {
	return stdtime.Time{}
}

func mutate() stdtime.Time {
	return stdtime.Now() // want "storage must use database-owned durable time; time.Now is not allowed in production storage code"
}

func useLocalClock() stdtime.Time {
	time := localClock{}
	return time.Now()
}

func usePackageNamedTime() int {
	return faketime.Now()
}
