package syntax

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	base "github.com/zambozoo/zam/internal/0_base"
	"github.com/zambozoo/zam/internal/std"
)

// objectMethodParser is the parser an interface is declared through, which
// putObjectMethods reads the file from and asks for Object's path through.
type objectMethodParser struct {
	SyntaxParser
	file  *File
	paths []*base.Path
}

func (p *objectMethodParser) File() **File { return &p.file }

func (p *objectMethodParser) AddPath(path *base.Path) { p.paths = append(p.paths, path) }

func newTestInterface(file *File) *DeclInterface {
	return &DeclInterface{
		declGeneric: declGeneric{
			decl: decl{
				start:          newToken(TTInterface, 0, nil),
				file:           file,
				kind:           KindInterface,
				name:           "List",
				staticFields:   base.NewStaticOrderedMap[string, Field](),
				instanceFields: base.NewStaticOrderedMap[string, Field](),
			},
		},
		interfaces: NewTypeSet(),
	}
}

func newTestMethod(d Decl, name string, modifiers Modifier, signature *TypeFunction) *FieldMethod {
	return &FieldMethod{
		field:     field{start: newToken(TTFun, 0, nil), outerDecl: d, name: name},
		modifiers: modifiers,
		fieldType: signature,
	}
}

func newTestClass(file *File, name string, base_ *DeclClass) *DeclClass {
	c := &DeclClass{
		declGeneric: declGeneric{
			decl: decl{
				start:          newToken(TTClass, 0, nil),
				file:           file,
				kind:           KindClass,
				name:           name,
				staticFields:   base.NewStaticOrderedMap[string, Field](),
				instanceFields: base.NewStaticOrderedMap[string, Field](),
			},
		},
		interfaces: NewTypeSet(),
	}
	if base_ != nil {
		c.baseDeclType = &TypeWrapped{
			kind:      TypeKindPointer,
			outerDecl: c,
			Type:      &TypeReference{outerDecl: c, Ids: []string{base_.Name()}, decl: base_},
		}
	}
	return c
}

// TestPutInterfaceIfImplemented covers the test the dispatch tables are built
// from: a class implements an interface when it has a method for each of the
// interface's, however it came by them.
func TestPutInterfaceIfImplemented(t *testing.T) {
	signature := func() *TypeFunction {
		return &TypeFunction{ReturnType: TypeInt}
	}

	newGreeter := func(file *File) *DeclInterface {
		iface := newTestInterface(file)
		greet := newTestMethod(iface, "greet", ModifierVirtual|ModifierPublic, signature())
		iface.instanceFields.Set(greet.Name(), greet)
		return iface
	}

	t.Run("a class declaring the method implements it", func(t *testing.T) {
		file := &File{Path: base.NewPath("main.zam", ""), Imports: map[string][]*Import{}}
		iface := newGreeter(file)
		class := newTestClass(file, "Impl", nil)
		greet := newTestMethod(class, "greet", ModifierPublic, signature())
		class.instanceFields.Set(greet.Name(), greet)

		PutInterfaceIfImplemented(class, iface)

		assert.Equal(t, 1, class.InterfaceCount())
	})

	t.Run("a class without the method does not", func(t *testing.T) {
		file := &File{Path: base.NewPath("main.zam", ""), Imports: map[string][]*Import{}}
		iface := newGreeter(file)
		class := newTestClass(file, "Bare", nil)

		PutInterfaceIfImplemented(class, iface)

		assert.Zero(t, class.InterfaceCount())
	})

	// The case a subtype check on its own does not reach: converting a derived
	// instance through its base records the interface on the base, while a call
	// dispatches on the table built for the derived class. Nothing would have
	// put the interface's methods in that table.
	t.Run("a class inheriting the method implements it", func(t *testing.T) {
		file := &File{Path: base.NewPath("main.zam", ""), Imports: map[string][]*Import{}}
		iface := newGreeter(file)
		parent := newTestClass(file, "Base", nil)
		greet := newTestMethod(parent, "greet", ModifierPublic, signature())
		parent.instanceFields.Set(greet.Name(), greet)
		child := newTestClass(file, "Derived", parent)

		PutInterfaceIfImplemented(child, iface)

		assert.Equal(t, 1, child.InterfaceCount())
	})

	t.Run("a method under the name with another signature does not", func(t *testing.T) {
		file := &File{Path: base.NewPath("main.zam", ""), Imports: map[string][]*Import{}}
		iface := newGreeter(file)
		class := newTestClass(file, "Mismatch", nil)
		greet := newTestMethod(class, "greet", ModifierPublic,
			&TypeFunction{ParameterTypes: []Type{TypeInt}, ReturnType: TypeInt})
		class.instanceFields.Set(greet.Name(), greet)

		PutInterfaceIfImplemented(class, iface)

		assert.Zero(t, class.InterfaceCount())
	})
}

// TestPutObjectMethods covers what an interface asks of an implementor. Every
// implementor descends from Object, so the interface asks for Object's methods
// too, except where it has said for itself what it wants under one of those
// names.
func TestPutObjectMethods(t *testing.T) {
	objectSignatures := map[string]string{
		base.EqualMethodName:   "bool(std.Obj*)",
		base.CompareMethodName: "int(std.Obj*)",
		base.HashMethodName:    "int()",
		base.StringMethodName:  "str()",
		base.MarkMethodName:    "void()",
	}

	t.Run("an interface asks for Object's methods", func(t *testing.T) {
		file := &File{Path: base.NewPath("main.zam", ""), Imports: map[string][]*Import{}}
		iface := newTestInterface(file)
		add := newTestMethod(iface, "Add", ModifierVirtual|ModifierPublic,
			&TypeFunction{ParameterTypes: []Type{TypeInt}, ReturnType: TypeVoid})
		iface.instanceFields.Set(add.Name(), add)

		parser := &objectMethodParser{file: file}
		putObjectMethods(parser, iface)

		for name, signature := range objectSignatures {
			method, ok := iface.GetInstanceMethod(name)
			if !assert.True(t, ok, "interface is missing %s", name) {
				continue
			}
			assert.Equal(t, signature, method.FieldType().String())
			// A method the interface asks for is one an implementor answers,
			// reached from wherever the interface is.
			assert.True(t, method.Modifiers().Has(ModifierVirtual), "%s is not virtual", name)
			assert.True(t, method.Modifiers().Has(ModifierPublic), "%s is not public", name)
		}

		// Obj is named by the methods now asked for, so the file reads it.
		assert.Contains(t, file.Imports, base.StdPackage)
		assert.Equal(t, []*base.Path{base.StdPath("lang/Obj" + base.CodeFileExtension)}, parser.paths)
	})

	t.Run("what the interface declares itself stands", func(t *testing.T) {
		file := &File{Path: base.NewPath("main.zam", ""), Imports: map[string][]*Import{}}
		iface := newTestInterface(file)
		str := newTestMethod(iface, base.StringMethodName, ModifierVirtual,
			&TypeFunction{ParameterTypes: []Type{TypeInt}, ReturnType: TypeString})
		iface.instanceFields.Set(str.Name(), str)

		putObjectMethods(&objectMethodParser{file: file}, iface)

		method, ok := iface.GetInstanceMethod(base.StringMethodName)
		assert.True(t, ok)
		assert.Same(t, str, method)
	})

	t.Run("a static field holds the name", func(t *testing.T) {
		file := &File{Path: base.NewPath("main.zam", ""), Imports: map[string][]*Import{}}
		iface := newTestInterface(file)
		hash := newTestMethod(iface, base.HashMethodName, ModifierStatic,
			&TypeFunction{ReturnType: TypeInt})
		iface.staticFields.Set(hash.Name(), hash)

		putObjectMethods(&objectMethodParser{file: file}, iface)

		_, ok := iface.GetInstanceMethod(base.HashMethodName)
		assert.False(t, ok)
	})

	t.Run("the std.Obj file asks for nothing", func(t *testing.T) {
		// Obj is the root of the base-class chain and must not descend from
		// itself. Every other file — including other stdlib files like Pair
		// or Err — reaches Obj the usual way.
		file := &File{Path: base.StdPath("lang/Obj.zam"), Imports: map[string][]*Import{}}
		iface := newTestInterface(file)

		parser := &objectMethodParser{file: file}
		putObjectMethods(parser, iface)

		assert.Zero(t, iface.instanceFields.Len())
		assert.Empty(t, parser.paths)
	})
}

// objectEthMethodNames are the instance methods std.Obj declares, read off the
// source in the order it declares them.
//
// Lexed rather than parsed, because what is being read is the order of the
// declarations and not what any of them means: a declaration's name is the
// identifier before its `=`, and the ones belonging to Obj itself are the ones
// at brace depth one. Parsing the file would settle far more than the question
// and would need the whole pipeline the guard is meant to sit beneath.
func objectEthMethodNames(t *testing.T) []string {
	t.Helper()

	source, err := std.FS.ReadFile("lang/Obj" + base.CodeFileExtension)
	if err != nil {
		t.Fatalf("reading Obj source: %v", err)
	}

	lexer, err := NewLexer(newRuneReader(io.NopCloser(bytes.NewReader(source))))
	if err != nil {
		t.Fatalf("lexing Obj source: %v", err)
	}
	defer lexer.Close()

	var names []string
	var depth int
	var previous *Token
	var isStatic bool
	for {
		token, err := lexer.Next()
		if err != nil {
			t.Fatalf("lexing Object source: %v", err)
		}

		switch token.Kind {
		case TTEOF:
			return names
		case TTLeftBrace:
			depth++
		case TTRightBrace:
			depth--
		case TTStatic:
			if depth == 1 {
				isStatic = true
			}
		case TTEqual:
			if depth == 1 && previous != nil && previous.Kind == TTIdentifier && !isStatic {
				names = append(names, previous.Value.(string))
			}
			isStatic = false
		}
		previous = token
	}
}

// TestObjectMethodSlots holds the slot constants to the one thing that actually
// decides them: the order std.Object declares its methods in. A method's slot is
// its position in a flattened table and nothing else, so reordering the
// declarations silently renumbers every object's table — and the runtime calls
// Eq by the number rather than by the name, so nothing downstream would fail
// loudly. This is what fails instead.
//
// objectMethods is checked against the same list because an interface is given
// Object's methods from there rather than from the file, the file not being
// parsed yet at the point one is declared. Two orders written in two places are
// two chances to disagree.
func TestObjectMethodSlots(t *testing.T) {
	expected := base.ObjectMethodNames[:]

	t.Run("the declaration order is the slot order", func(t *testing.T) {
		assert.Equal(t, expected, objectEthMethodNames(t))
	})

	t.Run("the interface's own copy agrees", func(t *testing.T) {
		object := &TypeWrapped{kind: TypeKindPointer}
		declared := make([]string, 0, len(expected))
		for _, method := range objectMethods(object) {
			declared = append(declared, method.name)
		}
		assert.Equal(t, expected, declared)
	})
}
