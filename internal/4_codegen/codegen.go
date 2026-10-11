package codegen

import (
	"encoding/binary"
	"io"
	"iter"
	"math"

	base "github.com/zambozoo/zam/internal/0_base"
	command "github.com/zambozoo/zam/internal/1_command"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
)

const wordByteSize = 8

// magic is the first eight bytes of every image, so that a reader can tell
// what it has been handed before it has read anything it would have to trust.
const magic = "ZAM_LANG"

// The version the image is stamped with. Each component is given a field of its
// own inside one word, so a runtime can reject an image with a single comparison
// and still say which version it was handed rather than only that it was wrong.
const (
	versionMajor = 0
	versionMinor = 0
	versionPatch = 0

	versionWord = versionMajor<<32 | versionMinor<<16 | versionPatch
)

// magicWord is [magic] as the word it is written as. Assembling it little-endian
// is what makes the bytes spell the name in the file, since that is the order
// every word is written in: a reader that compares eight bytes and a reader that
// compares one word are then looking at the same thing.
var magicWord = int(binary.LittleEndian.Uint64([]byte(magic)))

type (
	byteCountable interface {
		io.Reader
		ByteCount() int
	}

	counts struct {
		// typeDefinitionCount is the number of type definitions, i.e. the length
		// of the type definition offset map.
		typeDefinitionCount int
		// threadTypeDefinitionsStartIndex is the start index of the thread type definitions.
		threadTypeDefinitionsStartIndex int
		// functionTypeDefinitionsStartIndex is the start index of the function type definitions.
		functionTypeDefinitionsStartIndex int
		// pointerDefinitionsStartIndex is the start index of the pointer definitions.
		pointerDefinitionsStartIndex int
		// structDefinitionsStartIndex is the start index of the struct definitions.
		structDefinitionsStartIndex int
		// genericStructDefinitionsStartIndex is the start index of the generic struct definitions.
		genericStructDefinitionsStartIndex int
		// enumDefinitionsStartIndex is the start index of the enum definitions.
		enumDefinitionsStartIndex int
		// classDefinitionsStartIndex is the start index of the class definitions.
		classDefinitionsStartIndex int
		// genericClassDefinitionsStartIndex is the start index of the generic class definitions.
		genericClassDefinitionsStartIndex int
		// abstractDefinitionsStartIndex is the start index of the abstract definitions.
		abstractDefinitionsStartIndex int
		// genericAbstractDefinitionsStartIndex is the start index of the generic abstract definitions.
		genericAbstractDefinitionsStartIndex int
		// interfaceDefinitionsStartIndex is the start index of the interface definitions.
		interfaceDefinitionsStartIndex int
		// genericInterfaceDefinitionsStartIndex is the start index of the generic interface definitions.
		genericInterfaceDefinitionsStartIndex int

		// staticInstanceMapsByteOffset is the byte offset of the static instance maps.
		staticInstanceMapsByteOffset int
		// staticRegionByteSize is how many bytes of static region the program
		// needs. The map beside it says where each static sits inside that
		// region but never how wide any of them is, so the end of the last one
		// is not something the runtime could work out for itself.
		staticRegionByteSize int
		// staticImageByteOffset is the byte offset of the static region's
		// initial bytes.
		staticImageByteOffset int
		// nativesByteOffset is the byte offset of the native table, which opens
		// with the number of entries: the names are not all the same width, so
		// the run cannot be walked by anything not first told how far it goes.
		nativesByteOffset int
		// genericStaticMethodsByteOffset is the byte offset of the generic
		// static method registry, the lookup table OpPushGenericStaticSlot
		// walks when a generic method is called with open type arguments.
		genericStaticMethodsByteOffset int
		// operationsByteOffset is the byte offset of the operations.
		operationsByteOffset int
	}

	codegenReader struct {
		// gcTriggerRatio is how full the heap must be for a collection to be
		// triggered, as a fraction of its size.
		gcTriggerRatio float64
		// initialStackSize is the initial size of the stack.
		initialStackSize int
		// initialHeapSize is the initial size of the heap.
		initialHeapSize int

		// functionTypeDefinitions are the readers for the function type definitions.
		functionTypeDefinitions []typeDefinition
		// threadTypeDefinitions are the readers for the thread type definitions.
		threadTypeDefinitions []typeDefinition
		// pointerDefinitions are the readers for the pointer definitions.
		pointerDefinitions []typeDefinition
		// structDefinitions are the readers for the struct definitions.
		structDefinitions []typeDefinition
		// genericStructDefinitions are the readers for the generic struct definitions.
		genericStructDefinitions []typeDefinition
		// enumDefinitions are the readers for the enum definitions.
		enumDefinitions []typeDefinition
		// classDefinitions are the readers for the class definitions.
		classDefinitions []typeDefinition
		// genericClassDefinitions are the readers for the generic class definitions.
		genericClassDefinitions []typeDefinition
		// abstractDefinitions are the readers for the abstract definitions.
		abstractDefinitions []typeDefinition
		// genericAbstractDefinitions are the readers for the generic abstract definitions.
		genericAbstractDefinitions []typeDefinition
		// interfaceDefinitions are the readers for the interface definitions.
		interfaceDefinitions []typeDefinition
		// genericInterfaceDefinitions are the readers for the generic interface definitions.
		genericInterfaceDefinitions []typeDefinition

		// staticInstanceMaps are the byte offsets of the static instances, in one
		// index space shared by string literals, static members and static
		// methods, since a static is named on the wire by an index into this one
		// map however it was declared.
		staticInstanceMaps []int
		// stringLiterals maps each interned string literal to its static slot
		// index, in the layout order of the literal region at the head of the
		// static region.
		stringLiterals *base.StaticOrderedMap[stringLiteral, uint64]
		// staticImage is what the static region begins as: every interned
		// literal's bytes, and the header of every static whose value is a
		// markable one. It is not the whole region. Nothing else in it needs
		// saying — a static holds zeroes until something assigns to it — so the
		// image stops at the last byte anything wrote and the loader zero-fills
		// the rest. See zam_vm_init in internal/5_build/runtime/src/vm.c.
		staticImage []byte

		// genericStaticMethods is the registry of generic static methods that
		// may be called with open type arguments. Each entry carries one
		// concrete per instantiation — its ground type arguments, its static
		// slot, and the function type id the body was emitted as — and the
		// runtime binary-searches the entry by the type arguments on the stack
		// at the call site to find the right slot. A method ends up here when
		// a reference to it is compiled with any open-generic type argument;
		// a reference with ground arguments takes the compile-time fast path
		// and never touches this.
		genericStaticMethods []genericStaticMethodEntry
		// genericStaticMethodIDs maps a generic static method's syntax field
		// to the id it was assigned in the registry above. Lookups happen at
		// every call site that needs the runtime dispatch path, and the first
		// one assigns the id; the field pointer identifies the method the
		// same way across them.
		genericStaticMethodIDs map[*syntax.FieldMethod]uint16

		// natives maps each native's name to the slot it holds in the native
		// table section of the image. The name is a C symbol, so the table is
		// flat: one name is one entry however many places name it. Nothing but
		// the name is written, because what a native takes and gives back is the
		// C side's to know — the SDK reads the frame, and the compiler has no
		// signature to hand over.
		natives *base.StaticOrderedMap[string, uint64]

		// operations are the readers for the op code instructions.
		operations []operation

		// counts holds the per-bucket type-id start indices, computed once all
		// skeletons are built. Byte offsets are filled in at serialization time.
		counts *counts
	}

	typeDefinitionsInstantiatorVisitor struct {
		ast    *syntax.AST
		reader *codegenReader

		// typeIndices maps each function and thread type to its globally unique
		// index, for later population.
		typeIndices *syntax.TypeMap[int]
	}

	// inheritableDecl is the subset of a class or abstract declaration needed to
	// populate the interface lookup tables.
	inheritableDecl interface {
		syntax.DeclTypeable
		Interfaces() iter.Seq[syntax.Type]
		InterfaceCount() int
	}

	// virtualConcrete is the subset of a concrete class, abstract or interface
	// declaration that carries its virtual generic id.
	virtualConcrete interface {
		ConcreteID() *uint16
	}

	// genericStaticMethodEntry is one generic static method's dispatch row:
	// its arity (the number of type arguments the call site pushes) and the
	// run of concretes to search by those arguments. Linear search is fine —
	// a generic method rarely has more than a handful of concretes — and
	// avoids the perfect-hash machinery an interface dispatch pays for.
	genericStaticMethodEntry struct {
		arity     uint64
		concretes []genericStaticMethodConcrete
	}

	// genericStaticMethodConcrete is one monomorphization of a generic static
	// method: the ground type arguments that select it, the static slot its
	// closure was built into, and the function type id its body was emitted
	// as. OpPushGenericStaticSlot leaves both on the stack so the caller
	// treats it as a single-step replacement for OpPushStatic + emitPushTypeID.
	genericStaticMethodConcrete struct {
		typeArgs []typeInstance
		slot     uint64
		fnType   typeInstance
	}
)

// reservedTypeIDCount is the number of type ids reserved for primitive types.
// Primitives occupy the range [0, reservedTypeIDCount) and each primitive's
// type id is its TypeKind value directly.
var reservedTypeIDCount = uint16(syntax.TypeKindAny) + 1

func readFloat64(p []byte, value float64) (int, error) {
	if len(p) < wordByteSize {
		return 0, io.ErrShortBuffer
	}
	binary.LittleEndian.PutUint64(p, math.Float64bits(value))
	return wordByteSize, nil
}

func readUint64(p []byte, value uint64) (int, error) {
	if len(p) < wordByteSize {
		return 0, io.ErrShortBuffer
	}
	binary.LittleEndian.PutUint64(p, value)
	return wordByteSize, nil
}

func readInt(p []byte, value int) (int, error) {
	return readUint64(p, uint64(value))
}

// readAllWords writes each value as a word of its own, in the order given.
func readAllWords[T ~int | ~uint32 | ~uint64](p []byte, values ...T) (int, error) {
	totalBytes := 0
	for _, value := range values {
		n, err := readUint64(p[totalBytes:], uint64(value))
		if err != nil {
			return 0, err
		}
		totalBytes += n
	}
	return totalBytes, nil
}

// byteCount returns the total serialized byte size of the readers. Each one's
// ByteCount is already in bytes — it is what that reader's Read bounds-checks
// against and returns as written — so the sum is what the sections after these
// readers are offset by, and not a count of words to be scaled into bytes.
func byteCount[T byteCountable](readers []T) int {
	var totalByteCount int
	for _, reader := range readers {
		totalByteCount += reader.ByteCount()
	}
	return totalByteCount
}

func (b *counts) ByteCount() int { return len(b.words()) * wordByteSize }

func (b *counts) Read(p []byte) (int, error) {
	return readAllWords(p, b.words()...)
}

// words is the header in the order it is written, opening with the magic and
// the version, which are what has to be read before anything after them is worth
// reading. Both ByteCount and Read ask for it, so a field added here cannot end
// up in one and not the other.
func (b *counts) words() []int {
	return []int{
		magicWord,
		versionWord,
		b.typeDefinitionCount,
		b.threadTypeDefinitionsStartIndex,
		b.functionTypeDefinitionsStartIndex,
		b.pointerDefinitionsStartIndex,
		b.structDefinitionsStartIndex,
		b.genericStructDefinitionsStartIndex,
		b.enumDefinitionsStartIndex,
		b.classDefinitionsStartIndex,
		b.genericClassDefinitionsStartIndex,
		b.abstractDefinitionsStartIndex,
		b.genericAbstractDefinitionsStartIndex,
		b.interfaceDefinitionsStartIndex,
		b.genericInterfaceDefinitionsStartIndex,
		b.staticInstanceMapsByteOffset,
		b.staticRegionByteSize,
		b.staticImageByteOffset,
		b.nativesByteOffset,
		b.genericStaticMethodsByteOffset,
		b.operationsByteOffset,
	}
}

// startIndices is the start index of each definition bucket, in the same order
// definitionBuckets returns them, so that the two can be walked together.
func (b *counts) startIndices() []*int {
	return []*int{
		&b.threadTypeDefinitionsStartIndex,
		&b.functionTypeDefinitionsStartIndex,
		&b.pointerDefinitionsStartIndex,
		&b.structDefinitionsStartIndex,
		&b.genericStructDefinitionsStartIndex,
		&b.enumDefinitionsStartIndex,
		&b.classDefinitionsStartIndex,
		&b.genericClassDefinitionsStartIndex,
		&b.abstractDefinitionsStartIndex,
		&b.genericAbstractDefinitionsStartIndex,
		&b.interfaceDefinitionsStartIndex,
		&b.genericInterfaceDefinitionsStartIndex,
	}
}

// definitionBuckets are the definition lists in the order they are serialized,
// which is the order their type ids run in: the start index of each bucket is
// where the one before it ended. The offset map, the section byte counts and
// Read all walk them, and each would be wrong in its own way if any of them
// walked them in a different order.
func (cr *codegenReader) definitionBuckets() [][]typeDefinition {
	return [][]typeDefinition{
		cr.threadTypeDefinitions,
		cr.functionTypeDefinitions,
		cr.pointerDefinitions,
		cr.structDefinitions,
		cr.genericStructDefinitions,
		cr.enumDefinitions,
		cr.classDefinitions,
		cr.genericClassDefinitions,
		cr.abstractDefinitions,
		cr.genericAbstractDefinitions,
		cr.interfaceDefinitions,
		cr.genericInterfaceDefinitions,
	}
}

// staticImageAt is the span of the static region's initial bytes beginning at
// offset, the image grown to reach it. What lies between two spans is left as
// the zero the loader would have given it anyway, which is what lets the
// literals and the static headers be written into one buffer at the offsets the
// slot map already records rather than in the order they are laid out.
func (cr *codegenReader) staticImageAt(offset, byteCount uint64) []byte {
	if end := offset + byteCount; end > uint64(len(cr.staticImage)) {
		cr.staticImage = append(cr.staticImage,
			make([]byte, end-uint64(len(cr.staticImage)))...)
	}
	return cr.staticImage[offset : offset+byteCount]
}

// nativeNames is the native table in slot order, which is the order the names
// were interned in and so the order [operationVisitor.nativeIndex] numbered them.
// An OpNative operand names a slot of this run, so a name written anywhere else
// in it is a different function than the one that was compiled.
//
// A name is laid out as a string literal is — a length and its bytes, padded out
// to a word — without being one. What is written is a C symbol to be resolved
// against the interpreter's table when the two are built together, not a value
// the static region holds; the encoding is shared, the meaning is not.
func (cr *codegenReader) nativeNames() []stringLiteral {
	names := make([]stringLiteral, 0, cr.natives.Len())
	for _, entry := range cr.natives.All() {
		names = append(names, stringLiteral(entry.Key))
	}
	return names
}

// calculateByteCounts starts from the precomputed type-id start indices on the
// reader and fills in the type definition count and section byte offsets by
// walking every definition bucket in serialization order. Alongside the counts
// it returns the type definition map: definitions are not all the same width, so
// the only way to reach the nth one is to be told where it begins.
//
// The offsets it writes are measured from the end of the counts header rather
// than from the start of the file, so the header is not among what they count:
// the first byte after it is offset zero. What follows it are the three words of
// runtime configuration, which is where the walk begins.
func (cr *codegenReader) calculateByteCounts() (*counts, []int) {
	bc := *cr.counts
	buckets := cr.definitionBuckets()

	// The map is indexed by type id, so it has to run from zero however far
	// below the first definition that is. The primitives hold the ids under
	// reservedTypeIDCount and serialize no definition to point at, so their
	// entries stay zero and are never read.
	bc.typeDefinitionCount = int(reservedTypeIDCount)
	for _, bucket := range buckets {
		bc.typeDefinitionCount += len(bucket)
	}

	offset := 3*wordByteSize + bc.typeDefinitionCount*wordByteSize

	typeDefinitionMap := make([]int, bc.typeDefinitionCount)
	typeID := int(reservedTypeIDCount)
	for _, bucket := range buckets {
		for _, definition := range bucket {
			typeDefinitionMap[typeID] = offset
			typeID++
			offset += definition.ByteCount()
		}
	}

	bc.staticInstanceMapsByteOffset = offset
	offset += len(cr.staticInstanceMaps) * wordByteSize

	bc.staticImageByteOffset = offset
	offset += len(cr.staticImage)

	bc.nativesByteOffset = offset
	offset += countedByteCount(cr.nativeNames())

	bc.genericStaticMethodsByteOffset = offset
	offset += genericStaticMethodsByteCount(cr.genericStaticMethods)

	bc.operationsByteOffset = offset

	return &bc, typeDefinitionMap
}

// genericStaticMethodsByteCount is the serialized size of the registry: an
// entry count, then for each entry the arity and a counted list of concretes,
// then for each concrete its type arguments, slot, and function type id. The
// shape mirrors the counted-list idiom every other variable-length table in
// the image uses.
func genericStaticMethodsByteCount(entries []genericStaticMethodEntry) int {
	total := wordByteSize
	for _, entry := range entries {
		total += 2 * wordByteSize // arity, concreteCount
		for _, c := range entry.concretes {
			total += len(c.typeArgs) * (3 * wordByteSize)
			total += wordByteSize     // slot
			total += 3 * wordByteSize // fnType
			_ = c                     // sum includes every field; this guards against drift
		}
	}
	return total
}

// readGenericStaticMethods writes the registry: the entry count, then each
// entry's arity and concrete count, then every concrete laid as its type
// arguments followed by the slot and function type id. The runtime walks the
// section with the same shape — arity tells it how many type words to compare
// per concrete, and the two counts bound the walk.
func readGenericStaticMethods(p []byte, entries []genericStaticMethodEntry) (int, error) {
	totalBytes, err := readInt(p, len(entries))
	if err != nil {
		return 0, err
	}

	for _, entry := range entries {
		n, err := readAllWords(p[totalBytes:], entry.arity, uint64(len(entry.concretes)))
		if err != nil {
			return 0, err
		}
		totalBytes += n

		for _, c := range entry.concretes {
			for _, ta := range c.typeArgs {
				n, err = ta.Read(p[totalBytes:])
				if err != nil {
					return 0, err
				}
				totalBytes += n
			}

			n, err = readUint64(p[totalBytes:], c.slot)
			if err != nil {
				return 0, err
			}
			totalBytes += n

			n, err = c.fnType.Read(p[totalBytes:])
			if err != nil {
				return 0, err
			}
			totalBytes += n
		}
	}

	return totalBytes, nil
}

// countedByteCount and readCounted lay out a variable-length list the way every
// one inside a type definition is laid out: the number of elements, then the
// elements. The runtime cannot walk a list it does not first know the length of,
// since the elements are not all the same width.
func countedByteCount[T byteCountable](readers []T) int {
	return wordByteSize + byteCount(readers)
}

func readCounted[T byteCountable](p []byte, readers []T) (int, error) {
	totalBytes, err := readInt(p, len(readers))
	if err != nil {
		return 0, err
	}

	n, err := readAll(p[totalBytes:], readers)
	if err != nil {
		return 0, err
	}
	return totalBytes + n, nil
}

func readAll[T byteCountable](bs []byte, readers ...[]T) (int, error) {
	totalBytes := 0

	for _, readers := range readers {
		for _, reader := range readers {
			n, err := reader.Read(bs[totalBytes:])
			if err != nil {
				return 0, err
			}
			totalBytes += n
		}
	}

	return totalBytes, nil
}

// ByteCount is the size of the whole image. The offsets the header carries are
// measured from the end of the header, so the last section's offset plus its own
// size is everything that follows the header.
func (cr *codegenReader) ByteCount() int {
	bc, _ := cr.calculateByteCounts()
	return bc.ByteCount() + bc.operationsByteOffset + byteCount(cr.operations)
}

func (cr *codegenReader) Read(p []byte) (int, error) {
	totalBytes := 0

	bc, typeDefinitionMap := cr.calculateByteCounts()

	n, err := bc.Read(p[totalBytes:])
	if err != nil {
		return 0, err
	}
	totalBytes += n

	n, err = readFloat64(p[totalBytes:], cr.gcTriggerRatio)
	if err != nil {
		return 0, err
	}
	totalBytes += n

	n, err = readInt(p[totalBytes:], cr.initialStackSize)
	if err != nil {
		return 0, err
	}
	totalBytes += n

	n, err = readInt(p[totalBytes:], cr.initialHeapSize)
	if err != nil {
		return 0, err
	}
	totalBytes += n

	n, err = readAllWords(p[totalBytes:], typeDefinitionMap...)
	if err != nil {
		return 0, err
	}
	totalBytes += n

	n, err = readAll(p[totalBytes:], cr.definitionBuckets()...)
	if err != nil {
		return 0, err
	}
	totalBytes += n

	n, err = readAllWords(p[totalBytes:], cr.staticInstanceMaps...)
	if err != nil {
		return 0, err
	}
	totalBytes += n

	if len(p)-totalBytes < len(cr.staticImage) {
		return 0, io.ErrShortBuffer
	}
	totalBytes += copy(p[totalBytes:], cr.staticImage)

	n, err = readCounted(p[totalBytes:], cr.nativeNames())
	if err != nil {
		return 0, err
	}
	totalBytes += n

	n, err = readGenericStaticMethods(p[totalBytes:], cr.genericStaticMethods)
	if err != nil {
		return 0, err
	}
	totalBytes += n

	n, err = readAll(p[totalBytes:], cr.operations)
	if err != nil {
		return 0, err
	}
	totalBytes += n

	return totalBytes, nil
}

// Result is what codegen produces: the serialized bytecode image and the
// ordered list of native names the image's native table carries. The build
// driver reads both — the bytes to embed in the generated wrapper, and the
// names to generate the per-project ZamNativeEntry table against. Keeping
// them side by side keeps the two tables in agreement, since each name's
// index in the slice is the slot the image names it under.
type Result struct {
	Image       []byte
	NativeNames []string
}

func GenerateCode(cmd *command.Command, ast *syntax.AST) (*Result, error) {
	reader := &codegenReader{
		gcTriggerRatio:         cmd.GCTriggerRatio,
		initialStackSize:       cmd.InitialStackSize,
		initialHeapSize:        cmd.InitialHeapSize,
		genericStaticMethodIDs: map[*syntax.FieldMethod]uint16{},
	}

	// build type definition skeletons
	typeIndices := buildTypeDefinitionSkeletons(ast, reader)

	// intern every string literal into the head of the static region, so their
	// slots are known before the static fields are laid out after them
	staticByteOffset := populateStringLiterals(ast, reader)

	// run through type definitions again to populate referential ids/offsets and
	// lay out the static instance maps in the same traversal
	populateTypeDefinitions(ast, reader, typeIndices, staticByteOffset)

	// run through operations to populate operations
	if err := populateOperations(ast, reader, typeIndices); err != nil {
		return nil, err
	}

	// The image is serialized here rather than handed back as a reader over the
	// compiler's own state. Read writes the whole image wherever it is pointed
	// and reports no end, which is what every serializer in this package does,
	// but it is not what an io.Reader means: a consumer that reads in chunks
	// would be handed the image again on every call and never be told to stop.
	image := make([]byte, reader.ByteCount())
	if _, err := reader.Read(image); err != nil {
		return nil, err
	}

	nativeNames := make([]string, 0, reader.natives.Len())
	for _, entry := range reader.natives.All() {
		nativeNames = append(nativeNames, entry.Key)
	}

	return &Result{Image: image, NativeNames: nativeNames}, nil
}
