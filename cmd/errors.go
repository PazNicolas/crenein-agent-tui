package cmd

import "fmt"

// exitCodeError carries a non-default exit code through the cobra error path.
// Execute() inspects it and calls os.Exit with the embedded code.
type exitCodeError struct {
	code int
	err  error
}

func (e *exitCodeError) Error() string { return e.err.Error() }
func (e *exitCodeError) Unwrap() error { return e.err }

// Global exit code table.
const (
	ExitSuccess   = 0
	ExitOpFailure = 1
	// ExitDoctorWarning = 1: doctor warnings exit 1 (same as general failure — intentional;
	// distinguishing warnings from criticals is done via exit 2, not a separate code above 1).
	ExitDoctorWarning  = 1 // doctor: warnings found (= ExitOpFailure, documented contract)
	ExitDoctorCritical = 2 // doctor: critical issues found
	ExitPreflight      = 3
	ExitAborted        = 4
	ExitRolledBack     = 5 // update: rollback succeeded
	ExitRollbackFailed = 6 // update: rollback also failed
	// ExitSelfUpdateAvailable = 10 — belongs to self-update only, not in global table.
	ExitUsage = 64 // EX_USAGE
)

// usageError wraps a usage/validation message with exit code 64.
func usageError(msg string) error {
	return &exitCodeError{code: ExitUsage, err: fmt.Errorf("%s", msg)}
}

// preflightError wraps a pre-flight check failure with exit code 3.
func preflightError(err error) error {
	return &exitCodeError{code: ExitPreflight, err: err}
}

// abortedError returns an exit-4 error for user-declined operations.
func abortedError() error {
	return &exitCodeError{code: ExitAborted, err: fmt.Errorf("operation aborted by user")}
}

// rolledBackError wraps a failure-with-successful-rollback with exit code 5.
func rolledBackError(err error) error {
	return &exitCodeError{code: ExitRolledBack, err: err}
}

// rollbackFailedError wraps a failure-where-rollback-also-failed with exit code 6.
func rollbackFailedError(err error) error {
	return &exitCodeError{code: ExitRollbackFailed, err: err}
}

// opFailureError wraps a generic operation failure with exit code 1.
func opFailureError(err error) error {
	return &exitCodeError{code: ExitOpFailure, err: err}
}
