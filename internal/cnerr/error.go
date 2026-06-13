// Package cnerr defines the shared structured error type used by both the
// internal/detect and internal/engine packages.
//
// Design decision (AD-8): placing the error type in a neutral low-level package
// avoids import cycles: detect imports cnerr, engine imports cnerr and detect,
// but neither cnerr nor detect imports engine.
package cnerr

import "fmt"

// Error is a structured, user-facing error that carries the operation that
// failed, the underlying cause, and an actionable fix suggestion. It implements
// the error interface.
//
// Callers that only need to display or log the error can use .Error().
// Callers that need to surface a fix suggestion (e.g. the TUI or the doctor
// engine) should type-assert to *Error and read FixSuggestion.
type Error struct {
	// Op is the name of the operation that failed (e.g. "detect.AVX",
	// "engine.install.preflight").
	Op string

	// Cause is the underlying error returned by the subsystem. May be nil when
	// the error is purely semantic (e.g. "unsupported distro").
	Cause error

	// FixSuggestion is a human-readable, actionable suggestion that the UI can
	// present directly to the operator. Must not be empty for any error that a
	// user can act on.
	FixSuggestion string
}

// Error implements the error interface. Format: "Op: message [fix: suggestion]".
func (e *Error) Error() string {
	msg := e.Op
	if e.Cause != nil {
		msg += ": " + e.Cause.Error()
	}
	if e.FixSuggestion != "" {
		msg += " [fix: " + e.FixSuggestion + "]"
	}
	return msg
}

// Unwrap returns the underlying cause so errors.Is / errors.As work on the
// chain.
func (e *Error) Unwrap() error {
	return e.Cause
}

// New returns a *Error with no underlying cause.
func New(op, fix string) *Error {
	return &Error{Op: op, FixSuggestion: fix}
}

// Wrap wraps an existing error into a *Error.
func Wrap(op string, cause error, fix string) *Error {
	return &Error{Op: op, Cause: cause, FixSuggestion: fix}
}

// Wrapf wraps an existing error with a formatted message as Op.
func Wrapf(cause error, fix, format string, a ...any) *Error {
	return &Error{Op: fmt.Sprintf(format, a...), Cause: cause, FixSuggestion: fix}
}
