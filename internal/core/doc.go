// Package core is where orbit's domain logic lives, independent of how it is
// invoked.
//
// A tool with real rules of its own -- one that models something, rather than
// reporting a value -- puts them here, as core/git does for versions, tags and
// git's output, so they can be tested and reasoned about without cobra, a
// terminal, or git itself.
//
// Nothing under core may import a transport, cobra, or net/http; see
// architecture_test.go.
package core
