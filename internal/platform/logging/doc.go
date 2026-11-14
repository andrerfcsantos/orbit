// Package logging provides orbit's structured logging.
//
// Logging here follows the canonical log line approach: one richly-annotated
// entry per unit of work (a CLI invocation, an HTTP request) rather than many
// scattered lines. A unit of work accumulates fields as it progresses through the
// layers -- see Canonical, which is a mapped diagnostic context -- and emits a
// single entry when it finishes, whether it succeeded or not.
//
// The value of one-line-per-unit-of-work is that every entry answers the same
// questions (what ran, for whom, how long, what happened) and so can be filtered
// and aggregated without joining rows.
package logging
