package core

import "fmt"

// ErrorKind classifies a grove error. Each kind maps to a process exit code
// (documents/02-cli-spec.md §"Exit codes") and to a stable string surfaced
// as the `code` field of --json error output.
type ErrorKind string

const (
	KindGeneric            ErrorKind = "error"
	KindMisuse             ErrorKind = "misuse"
	KindNoWorkspace        ErrorKind = "no_workspace"
	KindNoSources          ErrorKind = "no_sources"
	KindModelUnreachable   ErrorKind = "model_unreachable"
	KindSourceUnreachable  ErrorKind = "source_unreachable"
	KindCorruptedWorkspace ErrorKind = "corrupted_workspace"
)

// ExitCode returns the process exit status for a kind.
func (k ErrorKind) ExitCode() int {
	switch k {
	case KindMisuse:
		return 2
	case KindNoWorkspace:
		return 3
	case KindNoSources:
		return 4
	case KindModelUnreachable:
		return 5
	case KindSourceUnreachable:
		return 6
	case KindCorruptedWorkspace:
		return 7
	default:
		return 1
	}
}

// Error is a structured, user-facing grove error. Adapters render it as
// plain text or as {code, message, remediation} JSON; the CLI process exits
// with Kind.ExitCode().
type Error struct {
	Kind        ErrorKind
	Message     string
	Remediation string
	Err         error // wrapped cause, may be nil
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Err)
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.Err }

func (e *Error) ExitCode() int { return e.Kind.ExitCode() }

// NewError builds a structured error with no wrapped cause.
func NewError(kind ErrorKind, message, remediation string) *Error {
	return &Error{Kind: kind, Message: message, Remediation: remediation}
}

// WrapError builds a structured error around an underlying cause. The cause
// stays reachable via errors.Is / errors.As.
func WrapError(kind ErrorKind, cause error, message, remediation string) *Error {
	return &Error{Kind: kind, Message: message, Remediation: remediation, Err: cause}
}
