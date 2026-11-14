package exit

import "fmt"

// Exit codes. Usage errors are distinguished from runtime failures so a script
// can tell "I called this wrongly" from "it ran and failed".
const (
	Success = 0
	Failure = 1
	// UsageCode follows the convention of 2 for a command-line misuse.
	UsageCode = 2
)

// Error carries an exit code with an error, so a command can determine the
// process exit status without calling os.Exit from deep inside the tree -- which
// would skip deferred cleanup, including the log file flush.
type Error struct {
	Code int
	Err  error
}

func (e *Error) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exit code %d", e.Code)
	}
	return e.Err.Error()
}

func (e *Error) Unwrap() error { return e.Err }

// Usage wraps err as an argument or flag misuse, which exits with UsageCode and
// prints the command's usage.
func Usage(err error) error {
	return &Error{Code: UsageCode, Err: err}
}

// Fail wraps err as a runtime failure.
func Fail(err error) error {
	return &Error{Code: Failure, Err: err}
}
