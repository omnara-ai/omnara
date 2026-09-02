package log

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Fields = map[string]any

type Event struct {
	mu         sync.Mutex
	log        *slog.Logger
	attrs      []slog.Attr
	name       string
	id         string
	started    time.Time
	err        error
	done       bool
	levelSet   bool
	level      EventLevel
	beforeDone []func(Finalizer)
	dbQueries  []DBQueryTraceRecord
	httpReqs   []HTTPRequestTraceRecord
}

// Finalizer is the handle that beforeDone callbacks receive. It exposes
// mutators that do NOT acquire e.mu, so a callback can never deadlock
// against the lock Done() may have just held. By the time callbacks run,
// Done() has set done=true and released the mutex, so concurrent
// Attach/Level/Error calls from other goroutines noop and unlocked
// mutation from inside the callback is safe.
type Finalizer struct{ e *Event }

func (f Finalizer) Attach(fieldSets ...Fields) {
	for _, fields := range fieldSets {
		f.e.applyFields(fields)
	}
}

func (f Finalizer) Level(level EventLevel) {
	f.e.pinLevel(level)
}

func (f Finalizer) Error(err error) {
	f.e.err = mergeEventErrors(f.e.err, err)
}

func NewEvent(ctx context.Context, name string, fieldSets ...Fields) *Event {
	e := &Event{
		log:     LoggerFromContext(ctx),
		name:    name,
		id:      uuid.NewString(),
		started: time.Now(),
	}
	base := Fields{
		"event.name":       name,
		"event.id":         e.id,
		"event.started_at": e.started.UTC(),
	}
	if parent, ok := FromContext(ctx); ok {
		base["parent.event.id"] = parent.id
		base["parent.event.name"] = parent.name
	}
	e.applyFields(base)
	for _, fields := range fieldSets {
		e.applyFields(fields)
	}
	return e
}

func (e *Event) Attach(fieldSets ...Fields) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.done {
		return
	}
	for _, fields := range fieldSets {
		e.applyFields(fields)
	}
}

func (e *Event) Error(err error) {
	if e == nil || err == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.done {
		return
	}
	e.err = mergeEventErrors(e.err, err)
}

func mergeEventErrors(existing, additional error) error {
	switch {
	case existing == nil:
		return additional
	case additional == nil:
		return existing
	case errors.Is(existing, additional):
		return existing
	default:
		return errors.Join(existing, additional)
	}
}

func (e *Event) Level(level EventLevel) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.done || e.levelSet {
		return
	}
	e.pinLevel(level)
}

func (e *Event) pinLevel(level EventLevel) {
	if e.levelSet {
		return
	}
	e.level = level
	e.levelSet = true
}

func (e *Event) beforeDoneFunc(fn func(Finalizer)) {
	if e == nil || fn == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.done {
		return
	}
	e.beforeDone = append(e.beforeDone, fn)
}

// Done finalizes the event and emits one log record. Concurrent and repeat
// calls are safe — only the first call emits; subsequent ones noop.
//
// Invariant: e.mu is never held across a callback or any user-supplied
// function. We claim ownership under the lock (done=true), release it,
// then run callbacks. Concurrent Attach/Level/Error callers see done=true
// and noop, so unlocked access from inside callbacks is safe and the
// public API can never deadlock against itself from inside a callback.
func (e *Event) Done(ctx context.Context) {
	if e == nil {
		return
	}
	e.mu.Lock()
	if e.done {
		e.mu.Unlock()
		return
	}
	e.done = true
	callbacks := e.beforeDone
	e.beforeDone = nil
	e.mu.Unlock()

	// Sole owner from here on. Callbacks mutate via Finalizer (unlocked).
	for _, fn := range callbacks {
		fn(Finalizer{e: e})
	}
	e.flushTraceFields()

	level := InfoLevel
	switch {
	case e.levelSet:
		level = e.level
	case e.err != nil:
		level = ErrorLevel
	}
	attrs := append([]slog.Attr(nil), e.attrs...)
	attrs = append(attrs, slog.Duration("event.duration", time.Since(e.started)))
	if e.err != nil {
		attrs = append(attrs, slog.String("error.message", e.err.Error()))
	}
	attrs = dedupAttrs(attrs)
	e.log.LogAttrs(ctx, level, e.name, attrs...)
}

func (e *Event) applyFields(fields Fields) {
	if len(fields) == 0 {
		return
	}
	e.attrs = append(e.attrs, attrsFromFields(fields)...)
}

func attrsFromFields(fields Fields) []slog.Attr {
	attrs := make([]slog.Attr, 0, len(fields))
	for k, v := range fields {
		attrs = append(attrs, slog.Any(k, normalizeFieldValue(v)))
	}
	return attrs
}

func dedupAttrs(attrs []slog.Attr) []slog.Attr {
	if len(attrs) < 2 {
		return attrs
	}
	latest := make(map[string]int, len(attrs))
	for index, attr := range attrs {
		latest[attr.Key] = index
	}
	out := attrs[:0]
	for index, attr := range attrs {
		if latest[attr.Key] == index {
			out = append(out, attr)
		}
	}
	return out
}

func normalizeFieldValue(v any) any {
	if id, ok := v.(uuid.UUID); ok && id == uuid.Nil {
		return nil
	}
	return v
}
