package syntax

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReferencesRecordsWhatEachNameRefersTo(t *testing.T) {
	file, other := &File{}, &File{}
	declared := &Token{Kind: TTIdentifier, Offset: 4, Value: "x"}
	used := &Token{Kind: TTIdentifier, Offset: 20, Value: "x"}
	target := Target{File: file, Name: declared}

	r := NewReferences()
	r.Add(file, declared, target, TypeInt)
	r.Add(other, used, target, TypeInt)

	ref, ok := r.At(other, 20)
	assert.True(t, ok)
	assert.Equal(t, target, ref.Target)
	assert.Equal(t, used, ref.Name)
	_, ok = r.At(file, 20)
	assert.False(t, ok, "an offset is looked up in the file the name is written in")

	assert.ElementsMatch(t, []Reference{
		{File: file, Name: declared, Target: target, Type: TypeInt},
		{File: other, Name: used, Target: target, Type: TypeInt},
	}, r.To(target))
}

// A name is resolved once for every instantiation of the generic it is written
// in, and what it was first resolved as is what it keeps.
func TestReferencesKeepsTheFirstResolution(t *testing.T) {
	file := &File{}
	name := &Token{Kind: TTIdentifier, Offset: 1, Value: "T"}
	first := Target{File: file, Name: &Token{Offset: 10}}

	r := NewReferences()
	r.Add(file, name, first, TypeInt)
	r.Add(file, name, Target{File: file, Name: &Token{Offset: 30}}, TypeString)

	ref, _ := r.At(file, 1)
	assert.Equal(t, first, ref.Target)
	assert.Equal(t, TypeInt, ref.Type)
}

// A node the compiler built rather than one that was written has no token to be
// found at, so it is not recorded.
func TestReferencesSkipsNamesWithNoToken(t *testing.T) {
	file := &File{}
	r := NewReferences()
	r.Add(file, nil, Target{File: file, Name: &Token{}}, nil)
	r.Add(file, &Token{Offset: 3}, Target{}, nil)

	_, ok := r.At(file, 3)
	assert.False(t, ok)
	assert.Empty(t, r.To(Target{}))
}
