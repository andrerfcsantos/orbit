package logging

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Canonical accumulates the fields describing one unit of work and emits them as
// a single log entry. It is safe for concurrent use.
//
// Fields are held in a map keyed by field name -- a mapped diagnostic context --
// rather than an append-only slice, so a later layer can refine a value an
// earlier layer supplied (a handler narrowing event.outcome, say) without the
// entry carrying the same key twice with different values. Insertion order is
// tracked separately so output stays readable and stable.
type Canonical struct {
	mu     sync.Mutex
	logger *zap.Logger

	fields     map[string]zap.Field
	fieldOrder []string
	// anonymous holds fields with an empty key. They cannot participate in
	// replacement, but keeping them loses less than dropping them.
	anonymous []zap.Field
	emitted   bool

	routine bool
	summary string
	// atLeast is the lowest level the entry may be written at, once raised.
	atLeast zapcore.Level
	raised  bool
}

type canonicalKey struct{}

type loggerKey struct{}

// NewCanonical attaches a new Canonical to ctx. Downstream code enriches the entry
// through AddFields(ctx, ...) without needing a reference to it, and FromContext
// returns the entry's logger, following it when SetLogger retargets the entry.
func NewCanonical(ctx context.Context, logger *zap.Logger) (context.Context, *Canonical) {
	if logger == nil {
		logger = Nop()
	}
	c := &Canonical{logger: logger, fields: make(map[string]zap.Field)}
	ctx = context.WithValue(ctx, loggerKey{}, c)
	return context.WithValue(ctx, canonicalKey{}, c), c
}

// CanonicalFromContext returns the Canonical attached to ctx, if any.
func CanonicalFromContext(ctx context.Context) (*Canonical, bool) {
	if ctx == nil {
		return nil, false
	}
	c, ok := ctx.Value(canonicalKey{}).(*Canonical)
	return c, ok && c != nil
}

// WithLogger returns a copy of ctx carrying logger. A nil logger is ignored so
// callers can pass an optional logger through without guarding.
func WithLogger(ctx context.Context, logger *zap.Logger) context.Context {
	if logger == nil {
		return ctx
	}
	return context.WithValue(ctx, loggerKey{}, logger)
}

// FromContext returns the logger attached to ctx, or a no-op logger. It never
// returns nil, so callers need not check before logging.
func FromContext(ctx context.Context) *zap.Logger {
	if logger, ok := contextLogger(ctx); ok {
		return logger
	}
	return Nop()
}

// contextLogger returns the logger attached to ctx by WithLogger or NewCanonical,
// whichever is nearer. A canonical entry's logger is read when asked for rather
// than copied into ctx: a CLI invocation starts on the bootstrap logger and is
// retargeted once configuration resolves, and a copy would keep writing to stderr.
func contextLogger(ctx context.Context) (*zap.Logger, bool) {
	if ctx == nil {
		return nil, false
	}
	switch held := ctx.Value(loggerKey{}).(type) {
	case *zap.Logger:
		return held, held != nil
	case *Canonical:
		held.mu.Lock()
		defer held.mu.Unlock()
		return held.logger, true
	}
	return nil, false
}

// AddFields adds or replaces fields on the canonical entry for ctx. It does
// nothing when ctx carries no entry, so code that runs both inside and outside a
// unit of work can annotate unconditionally.
func AddFields(ctx context.Context, fields ...zap.Field) {
	if c, ok := CanonicalFromContext(ctx); ok {
		c.Add(fields...)
	}
}

// AddFieldsOrEmit records fields on the canonical entry, or writes them as their
// own entry when that one has already gone. Either way they are written at level
// or above: recorded on an entry, they raise the level it is written at, so a
// failure the work recovered from still shows at a warn-level logger.
//
// For work detached from the request that started it: context.WithoutCancel keeps
// the accumulator, so a run that outlives its caller goes on adding to an entry
// the middleware has already written, where nothing it says is ever seen again.
func AddFieldsOrEmit(ctx context.Context, level zapcore.Level, msg string, fields ...zap.Field) {
	if len(fields) == 0 {
		return
	}
	if c, ok := CanonicalFromContext(ctx); ok && c.addIfOpenAt(level, fields...) {
		return
	}
	if entry := FromContext(ctx).Check(level, msg); entry != nil {
		entry.Write(fields...)
	}
}

// Summarize sets a short, human summary of what the unit of work did, such as
// "found 12 IDs in prd", which the entry's message ends with. A later call replaces
// an earlier one. The summary is held to the same rules as fields: name only values
// that are already safe to record, never an argument, a secret or a request body.
func Summarize(ctx context.Context, summary string) {
	if c, ok := CanonicalFromContext(ctx); ok {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.summary = summary
	}
}

// Routine marks the unit of work as routine, such as a health probe that runs
// every few seconds, so its entry is written at debug level when it succeeds. One
// that fails keeps its level, since that is the probe worth seeing.
func Routine(ctx context.Context) {
	if c, ok := CanonicalFromContext(ctx); ok {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.routine = true
	}
}

// Summary returns what Summarize set, or "".
func (c *Canonical) Summary() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.summary
}

// IsRoutine reports whether Routine marked the unit of work.
func (c *Canonical) IsRoutine() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.routine
}

// failureReason is what the entry says went wrong: the error a layer recorded.
func (c *Canonical) failureReason() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if field, ok := c.fields["error"]; ok && field.Type == zapcore.StringType {
		return field.String
	}
	return ""
}

// Add adds fields to the entry, replacing any field with the same key.
func (c *Canonical) Add(fields ...zap.Field) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.add(fields...)
}

// add merges fields. The caller must hold c.mu.
func (c *Canonical) add(fields ...zap.Field) {
	for _, field := range fields {
		if field.Key == "" {
			c.anonymous = append(c.anonymous, field)
			continue
		}
		if _, exists := c.fields[field.Key]; !exists {
			c.fieldOrder = append(c.fieldOrder, field.Key)
		}
		c.fields[field.Key] = field
	}
}

// AddIfOpen adds fields and reports whether the entry was still open to take
// them. Work that outlives its request uses the answer to log them elsewhere
// rather than onto an entry already written.
func (c *Canonical) AddIfOpen(fields ...zap.Field) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.emitted {
		return false
	}
	c.add(fields...)
	return true
}

// addIfOpenAt is AddIfOpen, also raising the level the entry is written at to at
// least level.
func (c *Canonical) addIfOpenAt(level zapcore.Level, fields ...zap.Field) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.emitted {
		return false
	}
	c.add(fields...)
	if !c.raised || level > c.atLeast {
		c.atLeast, c.raised = level, true
	}
	return true
}

// SetLogger retargets the entry at a different logger, keeping the fields
// gathered so far.
//
// This exists because of an ordering problem: configuration determines how to
// log (level, format, destination), but resolving configuration can itself fail,
// and that failure needs logging. So a unit of work starts against a minimal
// bootstrap logger, and is retargeted here once the configured logger exists.
// Retargeting after the entry has been emitted is refused, since the entry it
// would apply to is already written.
func (c *Canonical) SetLogger(logger *zap.Logger) {
	if c == nil || logger == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.emitted {
		return
	}
	c.logger = logger
}

// Fields returns the accumulated fields in insertion order. Intended for tests
// and for callers that need to inspect an entry before it is emitted.
func (c *Canonical) Fields() []zap.Field {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshotFields()
}

// Emit writes the accumulated fields as one entry at the given level, or at the
// level AddFieldsOrEmit raised it to if that is higher.
//
// It is idempotent: later calls do nothing. Callers defer Emit so that an entry
// is produced on every exit path including a panic, while still emitting
// explicitly at a natural end -- without the guard, both would fire and the unit
// of work would be logged twice.
func (c *Canonical) Emit(level zapcore.Level, msg string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.emitted {
		c.mu.Unlock()
		return
	}
	c.emitted = true
	if c.raised && level < c.atLeast {
		level = c.atLeast
	}
	logger := c.logger
	fields := c.snapshotFields()
	c.mu.Unlock()

	if entry := logger.Check(level, msg); entry != nil {
		entry.Write(fields...)
	}
}

// Emitted reports whether the entry has been written.
func (c *Canonical) Emitted() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.emitted
}

// snapshotFields copies the fields in order. The caller must hold c.mu.
func (c *Canonical) snapshotFields() []zap.Field {
	fields := make([]zap.Field, 0, len(c.fieldOrder)+len(c.anonymous))
	for _, key := range c.fieldOrder {
		fields = append(fields, c.fields[key])
	}
	return append(fields, c.anonymous...)
}

// RoundDuration trims a duration to a sensible precision for a log message.
func RoundDuration(elapsed time.Duration) time.Duration {
	if elapsed < time.Millisecond {
		return elapsed.Round(time.Microsecond)
	}
	return elapsed.Round(time.Millisecond)
}
