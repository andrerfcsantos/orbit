package logging

import (
	"context"
	"sync"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// newObserved builds a logger whose entries can be inspected.
func newObserved(t *testing.T, level zapcore.Level) (*zap.Logger, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(level)
	return zap.New(core), logs
}

func TestCanonicalEmitsOneEntryWithAllFields(t *testing.T) {
	logger, logs := newObserved(t, zapcore.DebugLevel)
	ctx, canonical := NewCanonical(context.Background(), logger)

	canonical.Add(zap.String("event.kind", "test"))
	// A different layer, reached only through the context, contributes to the same
	// entry: this is what makes one line per unit of work possible.
	AddFields(ctx, zap.Int("http.status", 200))
	canonical.Emit(zapcore.InfoLevel, "done")

	if got := logs.Len(); got != 1 {
		t.Fatalf("emitted %d entries, want exactly 1", got)
	}
	entry := logs.All()[0]
	if entry.Message != "done" {
		t.Errorf("message = %q, want done", entry.Message)
	}
	fields := entry.ContextMap()
	if fields["event.kind"] != "test" {
		t.Errorf("event.kind = %v, want test", fields["event.kind"])
	}
	if fields["http.status"] != int64(200) {
		t.Errorf("http.status = %v, want 200", fields["http.status"])
	}
}

func TestCanonicalReplacesRatherThanDuplicatingKeys(t *testing.T) {
	logger, logs := newObserved(t, zapcore.DebugLevel)
	_, canonical := NewCanonical(context.Background(), logger)

	canonical.Add(zap.String("event.outcome", "success"))
	// A later layer refines an earlier layer's value. With an append-only field
	// list the entry would carry event.outcome twice and a consumer would have to
	// guess which one counts.
	canonical.Add(zap.String("event.outcome", "failure"))
	canonical.Emit(zapcore.InfoLevel, "done")

	entry := logs.All()[0]
	if got := entry.ContextMap()["event.outcome"]; got != "failure" {
		t.Errorf("event.outcome = %v, want failure (the later value)", got)
	}

	seen := 0
	for _, field := range entry.Context {
		if field.Key == "event.outcome" {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("event.outcome appears %d times, want 1", seen)
	}
}

func TestCanonicalPreservesFieldOrder(t *testing.T) {
	logger, logs := newObserved(t, zapcore.DebugLevel)
	_, canonical := NewCanonical(context.Background(), logger)

	canonical.Add(zap.String("first", "1"))
	canonical.Add(zap.String("second", "2"))
	// Replacing a value must not move the field: stable order keeps entries
	// readable and diffable.
	canonical.Add(zap.String("first", "1-updated"))
	canonical.Add(zap.String("third", "3"))
	canonical.Emit(zapcore.InfoLevel, "done")

	var keys []string
	for _, field := range logs.All()[0].Context {
		keys = append(keys, field.Key)
	}
	want := []string{"first", "second", "third"}
	if len(keys) != len(want) {
		t.Fatalf("fields = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Errorf("field %d = %q, want %q (order: %v)", i, keys[i], want[i], keys)
		}
	}
}

func TestCanonicalEmitIsIdempotent(t *testing.T) {
	logger, logs := newObserved(t, zapcore.DebugLevel)
	_, canonical := NewCanonical(context.Background(), logger)

	canonical.Emit(zapcore.InfoLevel, "first")
	// Callers defer Emit so a panic still produces an entry, and also emit
	// explicitly at the natural end. Without the guard, every such unit of work
	// would be logged twice.
	canonical.Emit(zapcore.ErrorLevel, "second")

	if got := logs.Len(); got != 1 {
		t.Fatalf("emitted %d entries, want 1", got)
	}
	if got := logs.All()[0].Message; got != "first" {
		t.Errorf("message = %q, want first", got)
	}
}

func TestCanonicalSetLoggerRetargetsKeepingFields(t *testing.T) {
	bootstrapLogger, bootstrapLogs := newObserved(t, zapcore.DebugLevel)
	configured, configuredLogs := newObserved(t, zapcore.DebugLevel)

	_, canonical := NewCanonical(context.Background(), bootstrapLogger)
	// A field added before the configured logger exists must survive: the command
	// name is known before configuration is resolved.
	canonical.Add(zap.String("cli.command", "orb version"))

	canonical.SetLogger(configured)
	canonical.Emit(zapcore.InfoLevel, "done")

	if got := bootstrapLogs.Len(); got != 0 {
		t.Errorf("bootstrap logger received %d entries, want 0 after retargeting", got)
	}
	if got := configuredLogs.Len(); got != 1 {
		t.Fatalf("configured logger received %d entries, want 1", got)
	}
	if got := configuredLogs.All()[0].ContextMap()["cli.command"]; got != "orb version" {
		t.Errorf("cli.command = %v, want it preserved across retargeting", got)
	}
}

func TestCanonicalSetLoggerIgnoredAfterEmit(t *testing.T) {
	first, firstLogs := newObserved(t, zapcore.DebugLevel)
	second, secondLogs := newObserved(t, zapcore.DebugLevel)

	_, canonical := NewCanonical(context.Background(), first)
	canonical.Emit(zapcore.InfoLevel, "done")
	// The entry is already written; retargeting now would silently do nothing
	// useful, so it must not redirect a subsequent (no-op) emit either.
	canonical.SetLogger(second)

	if got := firstLogs.Len(); got != 1 {
		t.Errorf("first logger got %d entries, want 1", got)
	}
	if got := secondLogs.Len(); got != 0 {
		t.Errorf("second logger got %d entries, want 0", got)
	}
}

func TestAddFieldsWithoutCanonicalIsSafe(t *testing.T) {
	// Code paths shared between a unit of work and, say, initialization must be
	// able to annotate unconditionally.
	AddFields(context.Background(), zap.String("k", "v"))
	AddFields(context.TODO(), zap.String("k", "v"))
}

func TestFromContextNeverReturnsNil(t *testing.T) {
	if FromContext(context.Background()) == nil {
		t.Error("FromContext(empty) = nil, want a no-op logger")
	}

	logger, _ := newObserved(t, zapcore.DebugLevel)
	ctx := WithLogger(context.Background(), logger)
	if FromContext(ctx) != logger {
		t.Error("FromContext did not return the attached logger")
	}
}

func TestCanonicalIsConcurrencySafe(t *testing.T) {
	logger, logs := newObserved(t, zapcore.DebugLevel)
	_, canonical := NewCanonical(context.Background(), logger)

	// A streaming handler may be annotated from several goroutines; this is the
	// race detector's hook into that.
	const workers = 32
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			canonical.Add(zap.Int("worker", i))
			canonical.Add(zap.String("shared", "value"))
		}(i)
	}
	wg.Wait()
	canonical.Emit(zapcore.InfoLevel, "done")

	if got := logs.Len(); got != 1 {
		t.Fatalf("emitted %d entries, want 1", got)
	}
}

func TestNilCanonicalMethodsAreSafe(t *testing.T) {
	var canonical *Canonical
	// Nil-safety keeps call sites free of guards in code that may run outside a
	// unit of work.
	canonical.Add(zap.String("k", "v"))
	canonical.SetLogger(zap.NewNop())
	canonical.Emit(zapcore.InfoLevel, "ignored")
	if canonical.Emitted() {
		t.Error("nil Canonical reported Emitted() = true")
	}
	if fields := canonical.Fields(); fields != nil {
		t.Errorf("nil Canonical Fields() = %v, want nil", fields)
	}
}

// Work detached from its request keeps the request's accumulator, so once the
// middleware has written that entry anything added later goes nowhere. The fields
// have to reach the log by another route or the detached run is unobservable.
func TestAddFieldsOrEmitWritesItsOwnEntryAfterTheCanonicalOneIsGone(t *testing.T) {
	logger, logs := newObserved(t, zapcore.DebugLevel)
	ctx, canonical := NewCanonical(context.Background(), logger)

	AddFieldsOrEmit(ctx, zapcore.WarnLevel, "should not appear", zap.String("during", "request"))
	canonical.Emit(zapcore.InfoLevel, "request")

	AddFieldsOrEmit(ctx, zapcore.WarnLevel, "cache store failed", zap.String("cache.error", "connection refused"))

	entries := logs.All()
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want the request entry and the detached one: %v", len(entries), entries)
	}
	if entries[0].Message != "request" {
		t.Errorf("first entry = %q, want the canonical one", entries[0].Message)
	}
	// Added before the emit, so it belongs on the canonical entry, not its own.
	if _, ok := entries[0].ContextMap()["during"]; !ok {
		t.Errorf("a field added while the entry was open should be on it: %v", entries[0].ContextMap())
	}
	if entries[1].Message != "cache store failed" {
		t.Errorf("second entry = %q, want the standalone one", entries[1].Message)
	}
	if got := entries[1].ContextMap()["cache.error"]; got != "connection refused" {
		t.Errorf("cache.error = %v, want the cause to survive", got)
	}
}

// Nothing to say means nothing to write.
func TestAddFieldsOrEmitIsSilentWithNoFields(t *testing.T) {
	logger, logs := newObserved(t, zapcore.DebugLevel)
	ctx, canonical := NewCanonical(context.Background(), logger)
	canonical.Emit(zapcore.InfoLevel, "request")

	AddFieldsOrEmit(ctx, zapcore.WarnLevel, "nothing")
	if len(logs.All()) != 1 {
		t.Errorf("an empty call wrote an entry: %v", logs.All())
	}
}

func TestFromContextFollowsARetargetedEntry(t *testing.T) {
	// A CLI invocation starts on the bootstrap logger and is retargeted once
	// configuration resolves; code reading the logger from ctx must follow.
	bootstrap, bootstrapLogs := newObserved(t, zapcore.DebugLevel)
	configured, configuredLogs := newObserved(t, zapcore.DebugLevel)
	ctx, canonical := NewCanonical(context.Background(), bootstrap)
	canonical.SetLogger(configured)

	FromContext(ctx).Info("after the retarget")

	if bootstrapLogs.Len() != 0 || configuredLogs.Len() != 1 {
		t.Errorf("bootstrap got %d entries and configured %d, want 0 and 1", bootstrapLogs.Len(), configuredLogs.Len())
	}
}

func TestFromContextPrefersTheNearestLogger(t *testing.T) {
	outer, outerLogs := newObserved(t, zapcore.DebugLevel)
	inner, innerLogs := newObserved(t, zapcore.DebugLevel)
	ctx, _ := NewCanonical(WithLogger(context.Background(), outer), inner)

	FromContext(ctx).Info("inside the unit of work")

	if outerLogs.Len() != 0 || innerLogs.Len() != 1 {
		t.Errorf("outer got %d entries and inner %d, want 0 and 1", outerLogs.Len(), innerLogs.Len())
	}
}

func TestAddFieldsOrEmitRaisesTheEntrysLevel(t *testing.T) {
	// A failure the work recovered from is recorded on the entry, and must still
	// reach a logger that only takes warnings.
	logger, logs := newObserved(t, zapcore.WarnLevel)
	ctx, canonical := NewCanonical(context.Background(), logger)

	AddFieldsOrEmit(ctx, zapcore.WarnLevel, "the cache could not be read", zap.String("cache.error", "refused"))
	canonical.Emit(zapcore.InfoLevel, "request")

	if logs.Len() != 1 || logs.All()[0].Level != zapcore.WarnLevel {
		t.Fatalf("got %v, want one entry at warn", logs.All())
	}
}
