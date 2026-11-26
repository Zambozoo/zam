package selfhost_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zambozoo/zam/internal/base"
	"github.com/zambozoo/zam/internal/cli"
	syntax "github.com/zambozoo/zam/internal/compiler/0_syntax"
	semantic "github.com/zambozoo/zam/internal/compiler/1_semantic"
)

// ported is the sections of the semantic dump the compiler written in zam is
// expected to fill as Go does.
var ported = map[string]bool{"instance": true, "method": true, "open": true, "ground": true, "implements": true}

// TestDumpMatchesGo validates every valid program in the Go semantic testdata,
// and the compiler written in zam itself, with that compiler and with the Go
// one, and expects the same tables out of monomorphization and interface
// collection in every ported section.
func TestDumpMatchesGo(t *testing.T) {
	zam, err := compiler()
	require.NoError(t, err)

	entries, err := filepath.Glob(filepath.Join(root, "internal", "compiler", "1_semantic", "testdata", "valid", "*", "Main.zam"))
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	entries = append(entries, filepath.Join(root, "src", "Main.zam"))
	for _, entry := range entries {
		name, err := filepath.Rel(root, filepath.Dir(entry))
		require.NoError(t, err)
		t.Run(filepath.ToSlash(name), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, portedLines(goDump(t, entry)), portedLines(zamDump(t, zam, entry)))
		})
	}
}

// portedLines answers the lines of a dump whose section is ported.
func portedLines(lines []string) []string {
	got := []string{}
	for _, line := range lines {
		if ported[section(line)] {
			got = append(got, line)
		}
	}
	return got
}

// section answers the section a dump line belongs to: its first word, with
// the four ground type sets together as ground.
func section(line string) string {
	word, _, _ := strings.Cut(line, " ")
	switch word {
	case "function", "thread", "pointer", "tuple":
		return "ground"
	}
	return word
}

// zamDump answers the semantic dump the compiler at zam writes for the program
// entered at entry, sorted.
func zamDump(t *testing.T, zam, entry string) []string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(zam, arguments(entry, false)[1:]...)
	cmd.Env = append(os.Environ(), "ZAM_STD="+filepath.Join(root, "internal", "std"), "ZAM_DUMP=semantic")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		require.Failf(t, "zam failed", "%s", stderr.String())
	}
	require.NoError(t, err)
	got := []string{}
	for line := range strings.SplitSeq(strings.TrimSpace(stdout.String()), "\n") {
		if line != "" {
			got = append(got, relative(line))
		}
	}
	slices.Sort(got)
	return got
}

// goDump answers the tables Go validation fills for the program entered at
// entry, written as the compiler written in zam dumps them, sorted.
func goDump(t *testing.T, entry string) []string {
	t.Helper()
	files := &base.FileProvider{}
	cmd, err := cli.New(arguments(entry, false), files)
	require.NoError(t, err)
	ast, errs := syntax.NewAST(cmd, files, base.NewPackageProvider(t.TempDir()))
	require.Empty(t, errs)
	require.Empty(t, semantic.ValidateAST(ast))

	d := &dumper{}
	for _, entry := range ast.CodeFiles.All() {
		d.decl(entry.Value.Decl)
	}
	for _, set := range []struct {
		name  string
		types *syntax.TypeSet
	}{
		{"function", &ast.FunctionTypes},
		{"thread", &ast.ThreadTypes},
		{"pointer", &ast.PointerTypes},
		{"tuple", &ast.TupleTypes},
	} {
		for t := range set.types.Values() {
			d.lines = append(d.lines, set.name+" "+canonical(t))
		}
	}
	for _, implementor := range d.implementors {
		for _, iface := range d.interfaces {
			if implementor.HasInterface(iface) {
				d.lines = append(d.lines, "implements "+canonical(implementorType(implementor))+" "+canonical(iface))
			}
		}
	}
	for i, line := range d.lines {
		d.lines[i] = relative(line)
	}
	slices.Sort(d.lines)
	return d.lines
}

// dumper gathers the lines of a Go dump.
type dumper struct {
	lines        []string
	implementors []syntax.DeclInheritable
	interfaces   []syntax.Type
}

// decl dumps decl and the declarations nested in it.
func (d *dumper) decl(decl syntax.Decl) {
	if _, ok := decl.(*syntax.DeclAlias); ok {
		return
	}
	id := declID(decl)

	if generic, ok := decl.(syntax.DeclGeneric); ok && generic.Generics() != nil {
		for _, entry := range generic.Concretes().All() {
			if !entry.Key.ContainsGenericArg() {
				d.lines = append(d.lines, "instance "+id+" "+canonical(entry.Key))
			}
		}
		if generic.GenericTypes() != nil {
			for t := range generic.GenericTypes().Values() {
				d.lines = append(d.lines, "open "+id+" "+canonical(t))
			}
		}
	}
	for _, field := range decl.StaticFields() {
		method, ok := field.(*syntax.FieldMethod)
		if !ok || method.Generics() == nil {
			continue
		}
		for _, concrete := range method.Concretes() {
			d.lines = append(d.lines, "method "+id+"::"+method.Name()+" ["+typeList(concrete.TypeArgs())+"]")
		}
		if method.GenericTypes() != nil {
			for t := range method.GenericTypes().Values() {
				d.lines = append(d.lines, "open "+id+"::"+method.Name()+" "+canonical(t))
			}
		}
	}
	d.collect(decl)
	for child := range decl.Decls() {
		d.decl(child)
	}
}

// collect gathers decl and its ground instantiations as interface collection
// gathers them: implementors from every class, abstract and interface, and
// interfaces from every non-generic interface and every ground instantiation of
// a generic one.
func (d *dumper) collect(decl syntax.Decl) {
	inheritable, ok := decl.(syntax.DeclInheritable)
	if !ok {
		return
	}
	var concretes []syntax.DeclTypeable
	if inheritable.Generics() != nil {
		for _, entry := range inheritable.Concretes().All() {
			if !entry.Key.ContainsGenericArg() {
				concretes = append(concretes, entry.Value)
			}
		}
	}
	d.implementors = append(d.implementors, inheritable)
	for _, concrete := range concretes {
		if c, ok := concrete.(syntax.DeclInheritable); ok {
			d.implementors = append(d.implementors, c)
		}
	}
	if decl.Kind() != syntax.KindInterface {
		return
	}
	if inheritable.Generics() == nil {
		d.interfaces = append(d.interfaces, inheritable.(syntax.DeclTypeable).DeclType())
		return
	}
	for _, concrete := range concretes {
		d.interfaces = append(d.interfaces, concrete.DeclType())
	}
}

// implementorType answers the type a dump names an implementor by.
func implementorType(d syntax.DeclInheritable) syntax.Type {
	return d.(syntax.DeclTypeable).DeclType()
}

// typeList writes ts separated by commas.
func typeList(ts []syntax.Type) string {
	texts := make([]string, len(ts))
	for i, t := range ts {
		texts[i] = canonical(t)
	}
	return strings.Join(texts, ", ")
}

// declID names d by the file it is written in and the declarations it is
// nested in, which tells apart declarations that share a name.
func declID(d syntax.Decl) string {
	name := d.Name()
	for outer := d.OuterDecl(); outer != nil; outer = outer.OuterDecl() {
		name = outer.Name() + "." + name
	}
	return d.File().Path.String() + "|" + name
}

// canonical writes t with every declaration it names written as its declID,
// since a type's own text is however it happened to be written first.
func canonical(t syntax.Type) string {
	switch t := t.(type) {
	case *syntax.TypeReference:
		return "{" + declID(t.Decl()) + "}"
	case *syntax.TypeGeneric:
		return canonical(t.Type) + "[" + typeList(t.ParameterTypes) + "]"
	case *syntax.TypeTailed:
		if t.Size == nil {
			return canonical(t.Type) + "~"
		}
		return canonical(t.Type) + "~" + strconv.FormatUint(*t.Size, 10)
	case *syntax.TypeArray:
		if t.Size == nil {
			return canonical(t.Type) + "[~]"
		}
		return canonical(t.Type) + "[" + strconv.FormatUint(*t.Size, 10) + "]"
	case *syntax.TypeTuple:
		return "(" + typeList(t.Types) + ")"
	case *syntax.TypeFunction:
		return canonical(t.ReturnType) + "(" + typeList(t.ParameterTypes) + ")"
	case *syntax.TypeWrapped:
		switch t.Kind() {
		case syntax.TypeKindPointer:
			return canonical(t.Type) + "*"
		case syntax.TypeKindThread:
			return "thread[" + canonical(t.Type) + "]"
		case syntax.TypeKindVariadic:
			return canonical(t.Type) + "..."
		}
		return canonical(t.Type) + "$"
	}
	return t.String()
}

// relative answers line with the repository root taken out of every path in it.
func relative(line string) string {
	return strings.ReplaceAll(line, filepath.ToSlash(root)+"/", "")
}
