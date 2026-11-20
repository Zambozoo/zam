package syntax

type (
	// Target is where something a name refers to is declared.
	Target struct {
		// File is the file it is declared in.
		File *File
		// Name is the token its name is written at there.
		Name *Token
		// Start is the token its declaration begins at, which its doc comment
		// precedes, or nil for one that has no doc comment, such as a
		// parameter.
		Start *Token
	}

	// Reference is a name written in the source and what it refers to.
	Reference struct {
		// File is the file the name is written in.
		File *File
		// Name is the token the name is written at.
		Name *Token
		// Target is where what the name refers to is declared.
		Target Target
		// Type is the type the name comes to where it is written, or nil for a
		// name that is not a value, such as a type's.
		Type Type
	}

	// References records what each name in an AST refers to, as semantic
	// validation resolves it. A declaration's own name is recorded as referring
	// to itself, so that asking about a declaration and asking about a use of
	// it answer alike.
	References struct {
		byFile map[*File]map[uint]Reference
	}
)

// NewReferences returns an empty record of references.
func NewReferences() *References {
	return &References{byFile: map[*File]map[uint]Reference{}}
}

// Add records what the name written at a token in a file refers to. A name
// with no token, as a node built by the compiler rather than written has, is not
// recorded, and a name recorded once keeps what it was first recorded as: a
// type shared among the instantiations of a generic is resolved once for each.
func (r *References) Add(file *File, name *Token, target Target, t Type) {
	if file == nil || name == nil || target.File == nil || target.Name == nil {
		return
	}
	names, ok := r.byFile[file]
	if !ok {
		names = map[uint]Reference{}
		r.byFile[file] = names
	}
	if _, ok := names[name.Offset]; ok {
		return
	}
	names[name.Offset] = Reference{File: file, Name: name, Target: target, Type: t}
}

// At returns the reference whose name begins at an offset, in runes, in a file.
func (r *References) At(file *File, offset uint) (Reference, bool) {
	ref, ok := r.byFile[file][offset]
	return ref, ok
}

// To returns every reference to a target, in no particular order.
func (r *References) To(target Target) []Reference {
	var refs []Reference
	for _, names := range r.byFile {
		for _, ref := range names {
			if ref.Target == target {
				refs = append(refs, ref)
			}
		}
	}
	return refs
}

// TargetOf returns where a field or declaration is declared.
func TargetOf(f Field) Target {
	if d, ok := f.(Decl); ok {
		return Target{File: d.File(), Name: d.NameToken(), Start: d.Start()}
	}
	if f.OuterDecl() == nil {
		return Target{}
	}
	return Target{File: f.OuterDecl().File(), Name: f.NameToken(), Start: f.Start()}
}
