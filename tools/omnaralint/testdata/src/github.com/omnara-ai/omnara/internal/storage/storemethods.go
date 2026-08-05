package storage

import "time"

type Store struct{}

type Service struct{}

type MutationInput struct{}

type ScheduledTime time.Time

type TimeAlias = time.Time

func (*Store) CompleteAt(endedAt time.Time) {} // want "exported storage mutation methods must not accept direct time.Time parameters"

func (*Store) CompletePointer(endedAt *time.Time) { // want "exported storage mutation methods must not accept direct time.Time parameters"
}

func (*Store) CompleteAlias(endedAt TimeAlias) {} // want "exported storage mutation methods must not accept direct time.Time parameters"

func (*Store) CompleteInput(input MutationInput) {}

func (*Store) Schedule(at ScheduledTime) {}

func (*Store) LastTransition() time.Time { return time.Time{} }

func (*Service) CompleteAt(endedAt time.Time) { // want "exported storage mutation methods must not accept direct time.Time parameters"
}

func (*Service) CompleteInput(input MutationInput) {}

func convertDatabaseTime(value time.Time) time.Time { return value }

type SimilarStore struct{}

func (*SimilarStore) CompleteAt(endedAt time.Time) {}
