package base

import "fmt"

type (
	Position struct {
		Line   int
		Column int
	}

	Location struct {
		Path     Path
		Position *Position
	}

	Error struct {
		err      error
		Location *Location
	}
)

var _ error = (*Error)(nil)

func NewPosition(line int, column int) *Position {
	return &Position{
		Line:   line,
		Column: column,
	}
}

func (p *Position) String() string {
	return fmt.Sprintf("[%d:%d]", p.Line, p.Column)
}

func NewLocation(path Path, position *Position) *Location {
	return &Location{
		Path:     path,
		Position: position,
	}
}

func (l *Location) String() string {
	if l.Position != nil {
		return fmt.Sprintf("%s%s", l.Path.String(), l.Position.String())
	}
	return l.Path.String()
}

// NewError creates a new error with the given message and location.
func NewError(err error, location *Location) *Error {
	return &Error{
		err:      err,
		Location: location,
	}
}

// Error implements the [error] interface. The location is part of the message
// rather than something a caller has to know to ask for: an error is printed
// wherever it is finally handled, by code that has no reason to know one kind of
// error carries a location and another does not.
func (e *Error) Error() string {
	if e.Location != nil {
		return fmt.Sprintf("%s (%s)", e.err.Error(), e.Location.String())
	}
	return e.err.Error()
}

func (e *Error) Unwrap() error {
	return e.err
}
