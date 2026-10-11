package codegen

import (
	"encoding/binary"
	"io"

	syntax "github.com/zambozoo/zam/internal/2_syntax"
)

type (
	typeDefinition interface {
		byteCountable
	}

	typeInstanceShort struct {
		typeID    uint16
		genericID uint16
	}
	// typeInstance is the whole of what names a type on the wire: the short id,
	// how many elements a run of them is, and how long a tail an instance of it
	// carries. One encoding everywhere a type is named, so nothing walking a
	// definition has to know which of two widths the entry it is stepping over
	// was written as.
	typeInstance struct {
		typeInstanceShort
		arrayLength  uint64
		tailedLength uint64
	}
	typeListInstance struct {
		typeIDs []typeInstance
	}

	methodDefinition struct {
		instruction uint64
		typeListID  uint64
	}
	memberDefinition struct {
		Offset uint64
		Type   typeInstance
	}
	classOrAbstractInterfaceMethodKey struct {
		InterfaceType typeInstanceShort
		methodIndex   uint32
	}

	genericMapEntry struct {
		instanceByteSize     uint64
		instanceTailByteSize uint64
		genericTypes         typeListInstance
		// instanceMethodTypeIDs is the function type each method has once T is
		// bound, named rather than spelled out: C[int].f and C[string].f are
		// different signatures, but each of them is a ground function type the
		// program already uses and so already has a definition of its own.
		instanceMethodTypeIDs []uint64
		instanceMembers       []memberDefinition
		supertypeSet          map[typeInstanceShort]struct{}
	}
	genericClassMapEntry struct {
		genericMapEntry
		dispatchEntries map[uint64]uint32
	}

	// The four definitions below each carry an initializerInstruction: the
	// operations offset of the routine that fills a fresh instance of the type
	// in, which OpNew and OpDefaultValue run over what they produce. Zero names
	// no routine, since offset zero is where the program begins and so is never
	// an initializer.
	//
	// The routine is entered as a method is, with the instance in slot 0 and a
	// frame one word wide, and it returns nothing. There is one per declaration
	// rather than one per instantiation: it reaches every member by index, and
	// an index means the same thing in every instantiation, so a template's
	// concretes all name the same offset and the layout each index lands in is
	// recovered from the type id at run time.

	// An abstract is laid out nowhere, so no instance is ever one: a value of
	// abstract type is a reference to the class implementing it, and a call
	// through that value reads the record of the class the instance actually is.
	// Nothing therefore asks an abstract which method an interface method names
	// on it, and its record carries no dispatch table.
	//
	// It carries a supertype set all the same, because a subtype test can be
	// asked about an abstract rather than through one: an abstract's own type id
	// is a thing the source can name, and answering what stands above it means
	// reading the record that id points at.
	typeDefinitionAbstract struct {
		//instanceMethodsCount uint64
		instanceMethods []methodDefinition
		//	instanceMembersCount uint64
		instanceMembers        []memberDefinition
		instanceByteSize       uint64
		instanceTailByteSize   uint64
		initializerInstruction uint64
		supertypeSet           map[typeInstanceShort]struct{}
	}
	typeDefinitionClass struct {
		typeDefinitionAbstract
		dispatchEntries map[uint64]uint32
	}
	typeDefinitionGenericAbstract struct {
		// instanceMethodsCount       uint64
		// instanceMembersCount       uint64
		// concreteTypesCount         uint64
		concreteTypesSizeMap       []genericMapEntry
		instanceMethodInstructions []uint64
		initializerInstruction     uint64
	}
	typeDefinitionGenericClass struct {
		concreteTypesSizeMap       []genericClassMapEntry
		instanceMethodInstructions []uint64
		initializerInstruction     uint64
	}
	typeDefinitionEnum struct {
		// instanceMethodsCount uint64
		instanceMethods []methodDefinition
		// instanceMembersCount uint64
		instanceMembers        []memberDefinition
		instanceByteSize       uint64
		initializerInstruction uint64
	}
	typeDefinitionStruct struct {
		// instanceMethodsCount uint64
		instanceMethods []methodDefinition
		// instanceMembersCount uint64
		instanceMembers        []memberDefinition
		instanceByteSize       uint64
		instanceTailByteSize   uint64
		initializerInstruction uint64
	}

	// An interface is laid out nowhere and dispatched to through the record of
	// whatever class an instance turns out to be, so all it answers for is what
	// stands above it: the interfaces it extends, and itself. That is the whole
	// of its record.
	interfaceDefinition struct {
		supertypeSet map[typeInstanceShort]struct{}
	}
	// A generic interface holds one supertype set per instantiation, indexed by
	// generic id exactly as the concrete maps of a generic class or abstract are.
	// I[int] and I[string] are separate types and what stands above them is not
	// the same question, so one set between them would answer for neither.
	typeDefinitionGenericInterface struct {
		// concreteTypesCount uint64
		concreteSupertypeSets []map[typeInstanceShort]struct{}
	}

	// pointerDefinition names what a pointer points at. A pointer is a type of
	// its own rather than a depth counted alongside an array length, so E[5]*
	// points at an E carrying the array length of 5, while E*[5] is an array of
	// 5 whose element type is the pointer this defines.
	pointerDefinition struct {
		pointee typeInstance
	}
)

// packed encodes a type the way the runtime names it: the definition's type id
// in the high word and the instantiation's generic id in the low word, so
// LinkedList[int] is LinkedList's id shifted over that instantiation's id.
func (t typeInstanceShort) packed() uint32 {
	return uint32(t.typeID)<<16 | uint32(t.genericID)
}

// packed encodes a type as the three words the runtime names it by: the short
// id in the high half of the first, then the array length, then the tail length.
func (t typeInstance) packed() (uint64, uint64, uint64) {
	return uint64(t.typeInstanceShort.packed()) << 32, t.arrayLength, t.tailedLength
}

// packed encodes a dispatch key as the one word the runtime hashes. The short id
// occupies the high half, exactly as it does everywhere else a type is named,
// which leaves the low half free for the method index to fill without either
// having to be masked against the other.
func (k classOrAbstractInterfaceMethodKey) packed() uint64 {
	return uint64(k.InterfaceType.packed())<<32 | uint64(k.methodIndex)
}

func (t typeInstance) ByteCount() int { return 3 * wordByteSize }

func (t typeInstance) Read(p []byte) (int, error) {
	if len(p) < t.ByteCount() {
		return 0, io.ErrShortBuffer
	}
	word, arrayLength, tailedLength := t.packed()
	binary.LittleEndian.PutUint64(p, word)
	binary.LittleEndian.PutUint64(p[wordByteSize:], arrayLength)
	binary.LittleEndian.PutUint64(p[2*wordByteSize:], tailedLength)
	return t.ByteCount(), nil
}

// A member is where it lies within an instance, and what lies there.
func (m memberDefinition) ByteCount() int { return wordByteSize + m.Type.ByteCount() }

func (m memberDefinition) Read(p []byte) (int, error) {
	if len(p) < m.ByteCount() {
		return 0, io.ErrShortBuffer
	}
	totalBytes, _ := readUint64(p, m.Offset)
	n, err := m.Type.Read(p[totalBytes:])
	if err != nil {
		return 0, err
	}
	return totalBytes + n, nil
}

// A method is where its body begins, and the id of the function type naming its
// signature.
func (m methodDefinition) ByteCount() int { return 2 * wordByteSize }

func (m methodDefinition) Read(p []byte) (int, error) {
	if len(p) < m.ByteCount() {
		return 0, io.ErrShortBuffer
	}
	totalBytes, _ := readUint64(p, m.instruction)
	n, _ := readUint64(p[totalBytes:], m.typeListID)
	return totalBytes + n, nil
}

func (t typeListInstance) ByteCount() int { return countedByteCount(t.typeIDs) }

func (t typeListInstance) Read(p []byte) (int, error) {
	if len(p) < t.ByteCount() {
		return 0, io.ErrShortBuffer
	}
	return readCounted(p, t.typeIDs)
}

// The supertypes a type may be read as are held as a set while they are
// collected, since a base chain hands the same interface down more than once,
// and go out as a perfect hash so a cast costs a lookup rather than a search.
//
// Every key is its own value. A perfect hash records nothing about which keys
// were built into it, so a key that was not lands in some other key's slot and
// reads that one's value as though it were its own — which is what makes the
// table safe for dispatch, where the type checker has already proven the method
// is there, and unsafe for a membership question, which is asked precisely
// because the answer is not known. Storing the key in its own slot is what
// closes that: the runtime looks up the id it is asking about and believes the
// answer only if what comes back is the id it asked with.
//
// A miss can also land in a slot no key was ever placed in, which reads as zero.
// Zero is a primitive's type id and no declaration's, so it can never be what
// was asked about, and the comparison rejects it along with every other wrong
// answer.
func supertypeSetByteCount(set map[typeInstanceShort]struct{}) int {
	return perfectHashByteCount(len(set))
}

func readSupertypeSet(p []byte, set map[typeInstanceShort]struct{}) (int, error) {
	entries := make(map[uint64]uint32, len(set))
	for short := range set {
		packed := short.packed()
		entries[uint64(packed)] = packed
	}
	return buildPerfectHash(entries).Read(p)
}

// The dispatch table is held as the entries it will be built from rather than as
// a built table, and is constructed here, on the way out. Nothing before this
// point asks it anything: the compiler never resolves a key, it only collects
// them, and the runtime is what does the looking up.
//
// Deferring costs no certainty about the width because the width is not
// something the search settles on — perfectHashByteCount answers it from the key
// count alone, so the header offsets can be committed while the table is still
// only a map.
func dispatchByteCount(entries map[uint64]uint32) int {
	return perfectHashByteCount(len(entries))
}

func readDispatch(p []byte, entries map[uint64]uint32) (int, error) {
	return buildPerfectHash(entries).Read(p)
}

// Every definition below is written head first: all of its fixed-width fields,
// counts and scalars alike, and only then the runs those counts measure. The
// runtime does not parse a definition, it casts the image's own bytes to a
// struct of the head followed by a trailing array, so a field it names has to
// sit at an offset that is the same for every definition of that kind. A count
// written just before the run it measures would put every field after the first
// run at an offset that depends on the data, which is a walk rather than a cast.
//
// Among the runs, the ones whose elements are all the same width come first, so
// that reaching the run after them is arithmetic on the head. What is left —
// perfect hashes, and lists of lists — describes its own width in its first
// words, so it can be stepped over but only by reading it.
//
// A struct is its methods, its members, how much room an instance takes, and the
// routine that fills a fresh one in.
func (t *typeDefinitionStruct) ByteCount() int {
	return countedByteCount(t.instanceMethods) +
		countedByteCount(t.instanceMembers) +
		3*wordByteSize
}

func (t *typeDefinitionStruct) Read(p []byte) (int, error) {
	if len(p) < t.ByteCount() {
		return 0, io.ErrShortBuffer
	}

	totalBytes, _ := readAllWords(p, len(t.instanceMethods), len(t.instanceMembers))
	n, _ := readAllWords(p[totalBytes:],
		t.instanceByteSize, t.instanceTailByteSize, t.initializerInstruction)
	totalBytes += n
	n, _ = readAll(p[totalBytes:], t.instanceMethods)
	totalBytes += n
	n, _ = readAll(p[totalBytes:], t.instanceMembers)
	return totalBytes + n, nil
}

// An enum is a struct that carries no tail: its instances are the values named
// in the declaration, and none of them is variable-length.
func (t *typeDefinitionEnum) ByteCount() int {
	return countedByteCount(t.instanceMethods) +
		countedByteCount(t.instanceMembers) +
		2*wordByteSize
}

func (t *typeDefinitionEnum) Read(p []byte) (int, error) {
	if len(p) < t.ByteCount() {
		return 0, io.ErrShortBuffer
	}

	totalBytes, _ := readAllWords(p, len(t.instanceMethods), len(t.instanceMembers))
	n, _ := readAllWords(p[totalBytes:], t.instanceByteSize, t.initializerInstruction)
	totalBytes += n
	n, _ = readAll(p[totalBytes:], t.instanceMethods)
	totalBytes += n
	n, _ = readAll(p[totalBytes:], t.instanceMembers)
	return totalBytes + n, nil
}

// An abstract is its methods, its members, how much room an instance of a class
// implementing it takes, the routine that fills a fresh one in, and what stands
// above it.
func (t *typeDefinitionAbstract) ByteCount() int {
	return countedByteCount(t.instanceMethods) +
		countedByteCount(t.instanceMembers) +
		3*wordByteSize +
		supertypeSetByteCount(t.supertypeSet)
}

func (t *typeDefinitionAbstract) Read(p []byte) (int, error) {
	if len(p) < t.ByteCount() {
		return 0, io.ErrShortBuffer
	}

	totalBytes, _ := readAllWords(p, len(t.instanceMethods), len(t.instanceMembers))
	n, _ := readAllWords(p[totalBytes:],
		t.instanceByteSize, t.instanceTailByteSize, t.initializerInstruction)
	totalBytes += n
	n, _ = readAll(p[totalBytes:], t.instanceMethods)
	totalBytes += n
	n, _ = readAll(p[totalBytes:], t.instanceMembers)
	totalBytes += n
	n, _ = readSupertypeSet(p[totalBytes:], t.supertypeSet)
	return totalBytes + n, nil
}

// A class is an abstract that also answers which of its methods an interface
// method names, which is the one question an abstract is never asked: an
// abstract is not what any instance is, so nothing ever dispatches through one.
func (t *typeDefinitionClass) ByteCount() int {
	return t.typeDefinitionAbstract.ByteCount() +
		dispatchByteCount(t.dispatchEntries)
}

func (t *typeDefinitionClass) Read(p []byte) (int, error) {
	if len(p) < t.ByteCount() {
		return 0, io.ErrShortBuffer
	}

	totalBytes, _ := t.typeDefinitionAbstract.Read(p)
	n, _ := readDispatch(p[totalBytes:], t.dispatchEntries)
	return totalBytes + n, nil
}

// A generic class or abstract holds one entry per instantiation, and above them
// the method bodies the instantiations share. A method is compiled once however
// many times its declaration is instantiated: it reaches members by index, and
// an index means the same thing in every instantiation.
func (t *typeDefinitionGenericAbstract) ByteCount() int {
	byteCount := 3*wordByteSize + len(t.instanceMethodInstructions)*wordByteSize
	for i := range t.concreteTypesSizeMap {
		byteCount += t.concreteTypesSizeMap[i].ByteCount()
	}
	return byteCount
}

func (t *typeDefinitionGenericAbstract) Read(p []byte) (int, error) {
	if len(p) < t.ByteCount() {
		return 0, io.ErrShortBuffer
	}

	// The initializer is written in the head rather than after the entries. It is
	// one word whatever the instantiations are, and leaving it at the end would
	// mean walking every entry — each of them variable-width — to reach it.
	totalBytes, _ := readAllWords(p,
		len(t.instanceMethodInstructions), len(t.concreteTypesSizeMap))
	n, _ := readUint64(p[totalBytes:], t.initializerInstruction)
	totalBytes += n

	n, _ = readAllWords(p[totalBytes:], t.instanceMethodInstructions...)
	totalBytes += n
	for i := range t.concreteTypesSizeMap {
		n, _ = t.concreteTypesSizeMap[i].Read(p[totalBytes:])
		totalBytes += n
	}
	return totalBytes, nil
}

func (t *typeDefinitionGenericClass) ByteCount() int {
	byteCount := 3*wordByteSize + len(t.instanceMethodInstructions)*wordByteSize
	for i := range t.concreteTypesSizeMap {
		byteCount += t.concreteTypesSizeMap[i].ByteCount()
	}
	return byteCount
}

func (t *typeDefinitionGenericClass) Read(p []byte) (int, error) {
	if len(p) < t.ByteCount() {
		return 0, io.ErrShortBuffer
	}

	// The initializer is written in the head rather than after the entries. It is
	// one word whatever the instantiations are, and leaving it at the end would
	// mean walking every entry — each of them variable-width — to reach it.
	totalBytes, _ := readAllWords(p,
		len(t.instanceMethodInstructions), len(t.concreteTypesSizeMap))
	n, _ := readUint64(p[totalBytes:], t.initializerInstruction)
	totalBytes += n

	n, _ = readAllWords(p[totalBytes:], t.instanceMethodInstructions...)
	totalBytes += n
	for i := range t.concreteTypesSizeMap {
		n, _ = t.concreteTypesSizeMap[i].Read(p[totalBytes:])
		totalBytes += n
	}
	return totalBytes, nil
}

// A generic map entry is everything about an instantiation that its declaration
// cannot say: how wide its instances are, what it was instantiated over, the
// members and method signatures that follow from that, and what stands above it.
// The supertypes are the instantiation's own rather than the template's, since a
// method implementing an interface only once T is bound makes C[int] implement
// what C[string] does not.
func (g *genericMapEntry) ByteCount() int {
	return 2*wordByteSize +
		g.genericTypes.ByteCount() +
		wordByteSize + len(g.instanceMethodTypeIDs)*wordByteSize +
		countedByteCount(g.instanceMembers) +
		supertypeSetByteCount(g.supertypeSet)
}

func (g *genericMapEntry) Read(p []byte) (int, error) {
	if len(p) < g.ByteCount() {
		return 0, io.ErrShortBuffer
	}

	// Every run here is fixed-width, so the supertype set is the only thing an
	// entry holds whose end has to be read rather than computed — which is what
	// puts it last, and what lets the dispatch table a class's entry carries
	// begin directly after it.
	totalBytes, _ := readAllWords(p, g.instanceByteSize, g.instanceTailByteSize)
	n, _ := readAllWords(p[totalBytes:], len(g.genericTypes.typeIDs),
		len(g.instanceMembers), len(g.instanceMethodTypeIDs))
	totalBytes += n

	n, _ = readAll(p[totalBytes:], g.genericTypes.typeIDs)
	totalBytes += n
	n, _ = readAll(p[totalBytes:], g.instanceMembers)
	totalBytes += n
	n, _ = readAllWords(p[totalBytes:], g.instanceMethodTypeIDs...)
	totalBytes += n
	n, _ = readSupertypeSet(p[totalBytes:], g.supertypeSet)
	return totalBytes + n, nil
}

func (g *genericClassMapEntry) ByteCount() int {
	return g.genericMapEntry.ByteCount() +
		dispatchByteCount(g.dispatchEntries)
}

func (g *genericClassMapEntry) Read(p []byte) (int, error) {
	if len(p) < g.ByteCount() {
		return 0, io.ErrShortBuffer
	}

	totalBytes, _ := g.genericMapEntry.Read(p)
	n, _ := readDispatch(p[totalBytes:], g.dispatchEntries)
	return totalBytes + n, nil
}

func (t *interfaceDefinition) ByteCount() int {
	return supertypeSetByteCount(t.supertypeSet)
}

func (t *interfaceDefinition) Read(p []byte) (int, error) {
	if len(p) < t.ByteCount() {
		return 0, io.ErrShortBuffer
	}
	return readSupertypeSet(p, t.supertypeSet)
}

func (t *typeDefinitionGenericInterface) ByteCount() int {
	byteCount := wordByteSize
	for _, set := range t.concreteSupertypeSets {
		byteCount += supertypeSetByteCount(set)
	}
	return byteCount
}

func (t *typeDefinitionGenericInterface) Read(p []byte) (int, error) {
	if len(p) < t.ByteCount() {
		return 0, io.ErrShortBuffer
	}

	totalBytes, _ := readInt(p, len(t.concreteSupertypeSets))
	for _, set := range t.concreteSupertypeSets {
		n, _ := readSupertypeSet(p[totalBytes:], set)
		totalBytes += n
	}
	return totalBytes, nil
}

func (t *pointerDefinition) ByteCount() int { return t.pointee.ByteCount() }

// Read implements the [byteCountable] interface.
func (t *pointerDefinition) Read(p []byte) (int, error) { return t.pointee.Read(p) }

// buildTypeDefinitionSkeletons walks every declaration in the AST and appends a
// correctly-sized, but otherwise empty, type definition to the matching bucket.
// Referential fields (type ids, offsets, instructions) are left zeroed for the
// later passes to populate.
//
// Instance fields are renumbered first, since a definition is sized by how many
// fields an instance has rather than by how many its declaration writes, and
// every index the later passes key a table by has to agree with that.
func buildTypeDefinitionSkeletons(ast *syntax.AST, reader *codegenReader) *syntax.TypeMap[int] {
	visitor := &typeDefinitionsInstantiatorVisitor{
		reader:      reader,
		ast:         ast,
		typeIndices: syntax.NewTypeMap[int](),
	}

	for _, entry := range ast.CodeFiles.All() {
		renumberInstanceFields(entry.Value.Decl)
	}

	// A thread's definition is the one type it yields, and a function's is its
	// signature, so neither needs a definition type of its own: the type
	// instance and the type list already serialize as the runtime reads them.
	var i int
	for t := range ast.ThreadTypes.Values() {
		visitor.typeIndices.Set(t, i)
		i++
		reader.threadTypeDefinitions = append(reader.threadTypeDefinitions, &typeInstance{})
	}
	for t := range ast.FunctionTypes.Values() {
		visitor.typeIndices.Set(t, i)
		i++
		reader.functionTypeDefinitions = append(reader.functionTypeDefinitions, &typeListInstance{})
	}
	for t := range ast.PointerTypes.Values() {
		visitor.typeIndices.Set(t, i)
		i++
		reader.pointerDefinitions = append(reader.pointerDefinitions, &pointerDefinition{})
	}

	for _, entry := range ast.CodeFiles.All() {
		entry.Value.Decl.Accept(visitor)
	}

	visitor.computeStartIndices()

	return visitor.typeIndices
}

func (cv *typeDefinitionsInstantiatorVisitor) visitNestedDecls(d syntax.Decl) {
	for nested := range d.Decls() {
		nested.Accept(cv)
	}
}

func (cv *typeDefinitionsInstantiatorVisitor) VisitDeclStruct(d *syntax.DeclStruct) error {
	r := cv.reader
	if d.Generics() != nil {
		// Generic structs are flattened: one definition per concrete, each with
		// its own unique index.
		for _, entry := range d.Concretes().All() {
			def := &typeDefinitionStruct{
				instanceMethods: make([]methodDefinition, instanceFieldCount[*syntax.FieldMethod](entry.Value)),
				instanceMembers: make([]memberDefinition, instanceFieldCount[*syntax.FieldMember](entry.Value)),
			}
			if !entry.Key.ContainsGenericArg() {
				def.instanceByteSize, def.instanceTailByteSize, _ = layoutOf(entry.Value)
			}
			*entry.Value.TypeIndex() = len(r.genericStructDefinitions)
			r.genericStructDefinitions = append(r.genericStructDefinitions, def)
		}
	} else {
		def := &typeDefinitionStruct{
			instanceMethods: make([]methodDefinition, instanceFieldCount[*syntax.FieldMethod](d)),
			instanceMembers: make([]memberDefinition, instanceFieldCount[*syntax.FieldMember](d)),
		}
		def.instanceByteSize, def.instanceTailByteSize, _ = layoutOf(d)
		*d.TypeIndex() = len(r.structDefinitions)
		r.structDefinitions = append(r.structDefinitions, def)
	}
	cv.visitNestedDecls(d)
	return nil
}

func (cv *typeDefinitionsInstantiatorVisitor) VisitDeclEnum(d *syntax.DeclEnum) error {
	def := &typeDefinitionEnum{
		instanceMethods: make([]methodDefinition, instanceFieldCount[*syntax.FieldMethod](d)),
		instanceMembers: make([]memberDefinition, instanceFieldCount[*syntax.FieldMember](d)),
	}
	def.instanceByteSize, _, _ = layoutOf(d)
	*d.TypeIndex() = len(cv.reader.enumDefinitions)
	cv.reader.enumDefinitions = append(cv.reader.enumDefinitions, def)
	cv.visitNestedDecls(d)
	return nil
}

func (cv *typeDefinitionsInstantiatorVisitor) VisitDeclClass(d *syntax.DeclClass) error {
	r := cv.reader
	if d.Generics() != nil {
		*d.TypeIndex() = len(r.genericClassDefinitions)
		r.genericClassDefinitions = append(r.genericClassDefinitions, &typeDefinitionGenericClass{
			concreteTypesSizeMap:       make([]genericClassMapEntry, d.Concretes().Len()),
			instanceMethodInstructions: make([]uint64, instanceFieldCount[*syntax.FieldMethod](d)),
		})
		cv.assignConcreteIDs(d)
	} else {
		def := &typeDefinitionClass{
			typeDefinitionAbstract: typeDefinitionAbstract{
				instanceMethods: make([]methodDefinition, instanceFieldCount[*syntax.FieldMethod](d)),
				instanceMembers: make([]memberDefinition, instanceFieldCount[*syntax.FieldMember](d)),
			},
		}
		def.instanceByteSize, def.instanceTailByteSize, _ = layoutOf(d)
		*d.TypeIndex() = len(r.classDefinitions)
		r.classDefinitions = append(r.classDefinitions, def)
	}
	cv.visitNestedDecls(d)
	return nil
}

func (cv *typeDefinitionsInstantiatorVisitor) VisitDeclAbstract(d *syntax.DeclAbstract) error {
	r := cv.reader
	if d.Generics() != nil {
		*d.TypeIndex() = len(r.genericAbstractDefinitions)
		r.genericAbstractDefinitions = append(r.genericAbstractDefinitions, &typeDefinitionGenericAbstract{
			concreteTypesSizeMap:       make([]genericMapEntry, d.Concretes().Len()),
			instanceMethodInstructions: make([]uint64, instanceFieldCount[*syntax.FieldMethod](d)),
		})
		cv.assignConcreteIDs(d)
	} else {
		def := &typeDefinitionAbstract{
			instanceMethods: make([]methodDefinition, instanceFieldCount[*syntax.FieldMethod](d)),
			instanceMembers: make([]memberDefinition, instanceFieldCount[*syntax.FieldMember](d)),
		}
		def.instanceByteSize, def.instanceTailByteSize, _ = layoutOf(d)
		*d.TypeIndex() = len(r.abstractDefinitions)
		r.abstractDefinitions = append(r.abstractDefinitions, def)
	}
	cv.visitNestedDecls(d)
	return nil
}

func (cv *typeDefinitionsInstantiatorVisitor) VisitDeclInterface(d *syntax.DeclInterface) error {
	r := cv.reader
	if d.Generics() != nil {
		*d.TypeIndex() = len(r.genericInterfaceDefinitions)
		r.genericInterfaceDefinitions = append(r.genericInterfaceDefinitions, &typeDefinitionGenericInterface{
			concreteSupertypeSets: make([]map[typeInstanceShort]struct{}, d.Concretes().Len()),
		})
		cv.assignConcreteIDs(d)
	} else {
		*d.TypeIndex() = len(r.interfaceDefinitions)
		r.interfaceDefinitions = append(r.interfaceDefinitions, &interfaceDefinition{})
	}
	cv.visitNestedDecls(d)
	return nil
}

func (cv *typeDefinitionsInstantiatorVisitor) VisitDeclNamespace(d *syntax.DeclNamespace) error {
	cv.visitNestedDecls(d)
	return nil
}

func (cv *typeDefinitionsInstantiatorVisitor) VisitFieldMember(f *syntax.FieldMember) error {
	return nil
}
func (cv *typeDefinitionsInstantiatorVisitor) VisitFieldMethod(f *syntax.FieldMethod) error {
	return nil
}
func (cv *typeDefinitionsInstantiatorVisitor) VisitFieldEnum(f *syntax.FieldEnum) error { return nil }

// populateTypeDefinitions fills the referential fields of the skeletons: member
// and method type instances, interface lookup tables, and generic concrete
// maps. In the same traversal it lays out every declaration's static members and
// methods into the static region; this is safe because the static sizes come
// from the skeleton pass, not from the referential fields filled here.
func populateTypeDefinitions(ast *syntax.AST, reader *codegenReader, typeIndices *syntax.TypeMap[int], staticByteOffset uint64) {
	populator := &staticTypesandTypeDefinitionsPopulatorVisitor{
		typeResolver: typeResolver{typeIndices: typeIndices, counts: reader.counts},
		ast:          ast,
		reader:       reader,
		offset:       staticByteOffset,
	}
	for _, entry := range ast.CodeFiles.All() {
		entry.Value.Decl.Accept(populator)
	}

	// Every static has been laid out by here, so where the cursor stopped is how
	// much region they occupy between them. It is recorded now rather than
	// derived later because a static's width is known only while it is being
	// placed: what is kept is its offset, and the last offset is the start of
	// the last static rather than the end of it.
	reader.counts.staticRegionByteSize = int(populator.offset)

	// These definitions were built in these same iteration orders, so the nth
	// type describes the nth definition. They are filled here rather than where
	// they were built because each one names other types, and a type cannot be
	// resolved to an id until every declaration has been given one.
	var i int
	for t := range ast.ThreadTypes.Values() {
		// A thread is interned by the type it runs, so the set already holds the
		// yielded type rather than the thread wrapping it. A pointer is interned
		// whole, which is why the loop below unwraps and this one does not.
		def := reader.threadTypeDefinitions[i].(*typeInstance)
		*def = populator.typeInstanceOf(t)
		i++
	}

	i = 0
	for t := range ast.FunctionTypes.Values() {
		def := reader.functionTypeDefinitions[i].(*typeListInstance)
		*def = populator.functionTypeList(t)
		i++
	}

	i = 0
	for t := range ast.PointerTypes.Values() {
		def := reader.pointerDefinitions[i].(*pointerDefinition)
		def.pointee = populator.typeInstanceOf(t.(*syntax.TypeWrapped).Type)
		i++
	}
}

// computeStartIndices assigns each type bucket its global type-id start index
// and stores the result on the reader as the single source of truth for both
// referential resolution and the serialized counts header. Buckets follow
// primitives, then thread types, then function types, then the declaration
// buckets in serialization order.
func (cv *typeDefinitionsInstantiatorVisitor) computeStartIndices() {
	r := cv.reader
	bc := &counts{}

	// Each bucket is counted by the definitions it holds rather than by the types
	// it was built from, and by walking the same list the offset map is laid out
	// from. Counting a bucket any other way would leave the ids the header
	// advertises and the ids the map is indexed by describing different things.
	base := int(reservedTypeIDCount)
	starts := bc.startIndices()
	for i, bucket := range r.definitionBuckets() {
		*starts[i] = base
		base += len(bucket)
	}

	r.counts = bc
}
