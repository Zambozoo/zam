package codegen

import (
	"encoding/binary"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
)

func testMethods() []methodDefinition {
	return []methodDefinition{{instruction: 10, typeListID: 11}, {instruction: 20, typeListID: 21}}
}

func testMembers() []memberDefinition {
	return []memberDefinition{
		{Offset: 0, Type: typeInstance{
			typeInstanceShort: typeInstanceShort{typeID: 3, genericID: 0}}},
		{Offset: 8, Type: typeInstance{
			typeInstanceShort: typeInstanceShort{typeID: 4, genericID: 1},
			arrayLength:       3, tailedLength: 5}},
	}
}

func testSupertypeSet() map[typeInstanceShort]struct{} {
	return map[typeInstanceShort]struct{}{
		{typeID: 9, genericID: 0}: {},
		{typeID: 7, genericID: 2}: {},
	}
}

func testDispatch() map[uint64]uint32 {
	return map[uint64]uint32{
		classOrAbstractInterfaceMethodKey{typeInstanceShort{typeID: 9}, 0}.packed():               1,
		classOrAbstractInterfaceMethodKey{typeInstanceShort{typeID: 7, genericID: 2}, 1}.packed(): 0,
	}
}

func testGenericEntry() genericMapEntry {
	return genericMapEntry{
		instanceByteSize:     32,
		instanceTailByteSize: 8,
		genericTypes: typeListInstance{typeIDs: []typeInstance{
			{typeInstanceShort: typeInstanceShort{typeID: 1}},
		}},
		// One id per method the declaration compiled a body for, which is what
		// the generic definitions below hold three of. Distinct, and more than
		// one, because the run is what the supertype set behind it is found by
		// adding: a count read short would put every line after this inside it.
		instanceMethodTypeIDs: []uint64{100, 101, 102},
		instanceMembers:       testMembers(),
		supertypeSet:          testSupertypeSet(),
	}
}

func testGenericClassEntry() genericClassMapEntry {
	return genericClassMapEntry{
		genericMapEntry: testGenericEntry(),
		dispatchEntries: testDispatch(),
	}
}

// Every offset in the header is a sum of ByteCounts, so a definition that
// reports one width and writes another silently moves every section after it.
// This is the invariant worth holding all four definitions to.
func TestTypeDefinitionByteCountMatchesWhatIsWritten(t *testing.T) {
	entry := testGenericEntry()
	classEntry := testGenericClassEntry()
	definitions := map[string]typeDefinition{
		"struct": &typeDefinitionStruct{
			instanceMethods: testMethods(), instanceMembers: testMembers(),
			instanceByteSize: 24, instanceTailByteSize: 8, initializerInstruction: 40,
		},
		"enum": &typeDefinitionEnum{
			instanceMethods: testMethods(), instanceMembers: testMembers(),
			instanceByteSize: 16, initializerInstruction: 41,
		},
		"abstract": &typeDefinitionAbstract{
			instanceMethods: testMethods(), instanceMembers: testMembers(),
			instanceByteSize: 24, instanceTailByteSize: 0, initializerInstruction: 42,
			supertypeSet: testSupertypeSet(),
		},
		"class": &typeDefinitionClass{
			typeDefinitionAbstract: typeDefinitionAbstract{
				instanceMethods: testMethods(), instanceMembers: testMembers(),
				instanceByteSize: 24, instanceTailByteSize: 0, initializerInstruction: 42,
				supertypeSet: testSupertypeSet(),
			},
			dispatchEntries: testDispatch(),
		},
		"generic abstract": &typeDefinitionGenericAbstract{
			concreteTypesSizeMap:       []genericMapEntry{entry},
			instanceMethodInstructions: []uint64{50, 51, 52},
			initializerInstruction:     43,
		},
		"generic class": &typeDefinitionGenericClass{
			concreteTypesSizeMap:       []genericClassMapEntry{classEntry},
			instanceMethodInstructions: []uint64{50, 51, 52},
			initializerInstruction:     43,
		},
		"empty struct":            &typeDefinitionStruct{},
		"empty generic":           &typeDefinitionGenericAbstract{},
		"interface":               &interfaceDefinition{supertypeSet: testSupertypeSet()},
		"empty interface":         &interfaceDefinition{},
		"generic interface":       &typeDefinitionGenericInterface{concreteSupertypeSets: []map[typeInstanceShort]struct{}{testSupertypeSet()}},
		"empty generic interface": &typeDefinitionGenericInterface{},
		"generic entry":           &entry,
		"generic class entry":     &classEntry,
		"type list": typeListInstance{typeIDs: []typeInstance{{
			typeInstanceShort: typeInstanceShort{typeID: 1}, tailedLength: 2}}},
		"empty type list": typeListInstance{},
	}

	for name, definition := range definitions {
		t.Run(name, func(t *testing.T) {
			p := make([]byte, definition.ByteCount())

			n, err := definition.Read(p)

			assert.NoError(t, err)
			assert.Equal(t, definition.ByteCount(), n,
				"%s advertises %d bytes but wrote %d", name, definition.ByteCount(), n)
		})

		t.Run(name+" refuses a buffer one byte short", func(t *testing.T) {
			if definition.ByteCount() == 0 {
				t.Skip("nothing to be short of")
			}
			n, err := definition.Read(make([]byte, definition.ByteCount()-1))

			assert.ErrorIs(t, err, io.ErrShortBuffer)
			assert.Zero(t, n)
		})
	}
}

func TestTypeDefinitionClassRead(t *testing.T) {
	def := &typeDefinitionClass{
		typeDefinitionAbstract: typeDefinitionAbstract{
			instanceMethods:        testMethods(),
			instanceMembers:        testMembers(),
			instanceByteSize:       24,
			instanceTailByteSize:   8,
			initializerInstruction: 42,
			supertypeSet:           testSupertypeSet(),
		},
		dispatchEntries: testDispatch(),
	}
	p := make([]byte, def.ByteCount())

	_, err := def.Read(p)
	assert.NoError(t, err)

	word := func(i int) uint64 { return binary.LittleEndian.Uint64(p[i*wordByteSize:]) }

	// The head is every fixed-width field, so each sits at the same word in every
	// class definition and the runtime casts to it rather than walking to it. The
	// runs follow, and only their contents move with the data.
	assert.Equal(t, uint64(2), word(0), "method count")
	assert.Equal(t, uint64(2), word(1), "member count")
	assert.Equal(t, uint64(24), word(2), "instance byte size")
	assert.Equal(t, uint64(8), word(3), "instance tail byte size")
	assert.Equal(t, uint64(42), word(4), "initializer instruction")

	// Then the runs, methods first: two methods of two words, then two members of
	// four words, and the supertype set after them.
	assert.Equal(t, uint64(10), word(5), "first method's instruction")
	assert.Equal(t, uint64(8), word(13), "second member's offset")
}

// A supertype table is asked about ids that may not be in it, which is the one
// thing a perfect hash cannot be asked on its own: a missing key resolves to
// some present key's slot. Storing each key as its own value is what makes the
// answer checkable, so what is worth holding is that a member comes back as
// itself and a non-member comes back as anything else.
func TestSupertypeSetAnswersMembership(t *testing.T) {
	set := testSupertypeSet()
	entries := make(map[uint64]uint32, len(set))
	for short := range set {
		entries[uint64(short.packed())] = short.packed()
	}
	h := buildPerfectHash(entries)

	for short := range set {
		assert.Equal(t, short.packed(), h.lookup(uint64(short.packed())),
			"%v is in the set and must resolve to itself", short)
	}

	absent := typeInstanceShort{typeID: 11, genericID: 4}
	assert.NotEqual(t, absent.packed(), h.lookup(uint64(absent.packed())),
		"%v is not in the set and must not resolve to itself", absent)
}

// The dispatch table is what the runtime resolves an interface call through, so
// what matters is that it still answers correctly once it has been round-tripped
// through the layout the runtime reads it back in.
func TestTypeDefinitionDispatchResolvesInterfaceMethods(t *testing.T) {
	key := classOrAbstractInterfaceMethodKey{
		InterfaceType: typeInstanceShort{typeID: 7, genericID: 2},
		methodIndex:   1,
	}

	assert.Equal(t, uint32(0), buildPerfectHash(testDispatch()).lookup(key.packed()))
}
