package app

import "errors"

// ErrorKind is the stable CLI category for a failed operation.
type ErrorKind int

const (
	ErrorInternal ErrorKind = iota
	ErrorInput
	ErrorTarget
)

// Error preserves the operational category while retaining the underlying cause.
type Error struct {
	Kind ErrorKind
	Err  error
}

func (err *Error) Error() string {
	return err.Err.Error()
}

func (err *Error) Unwrap() error {
	return err.Err
}

// KindOf returns the outermost application error category.
func KindOf(err error) ErrorKind {
	var applicationError *Error
	if errors.As(err, &applicationError) {
		return applicationError.Kind
	}
	return ErrorInternal
}

func inputError(err error) error {
	return &Error{Kind: ErrorInput, Err: err}
}

func targetError(err error) error {
	return &Error{Kind: ErrorTarget, Err: err}
}
