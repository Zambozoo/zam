package base

import "os"

const (
	MainMethodName = "main"

	// The instance methods std.Object declares, which every object therefore
	// has: equality, ordering, hashing, writing itself out as a string, marking
	// itself for collection, and the destructor the program runs through delete.
	EqualMethodName   = "Eq"
	CompareMethodName = "Cmp"
	HashMethodName    = "Hash"
	StringMethodName  = "Str"
	MarkMethodName    = "Mark"
	DeleteMethodName  = "Delete"
)

// The slot each of std.Object's instance methods occupies in the flattened
// method table of every object. A class or an abstract descends from Object, a
// base's methods are laid out ahead of the ones a declaration adds, and an
// override takes the slot it inherited rather than a new one, so one of these
// indices reaches the same method whatever the object turns out to be. That is
// what lets a caller reach Eq without a lookup, which is the whole reason the
// numbers are worth naming.
//
// These are not the source of the numbering — the order std.Object declares its
// methods in is — so they are held to that order by TestObjectMethodSlots
// rather than relied on to impose it.
const (
	EqualMethodSlot = iota
	CompareMethodSlot
	HashMethodSlot
	StringMethodSlot
	MarkMethodSlot
	DeleteMethodSlot
)

// ObjectMethodNames are std.Object's instance methods keyed by the slot each
// occupies, so that an index is a slot and the array is the two facts written
// down once. It is what the order guard compares the declaration against.
var ObjectMethodNames = [...]string{
	EqualMethodSlot:   EqualMethodName,
	CompareMethodSlot: CompareMethodName,
	HashMethodSlot:    HashMethodName,
	StringMethodSlot:  StringMethodName,
	MarkMethodSlot:    MarkMethodName,
	DeleteMethodSlot:  DeleteMethodName,
}

type ExitCode int

const (
	ExitCodeSuccess ExitCode = iota
	ExitCodeError
)

func Exit(code ExitCode) {
	os.Exit(int(code))
}
