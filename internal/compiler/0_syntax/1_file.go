package syntax

import (
	"errors"
	"fmt"
	"strings"

	"github.com/zambozoo/zam/internal/base"
)

type (
	FileVisitor interface {
		VisitFile(f *File) error
		VisitImport(i *Import) error
	}

	// File is a file in the project.
	File struct {
		// Path is the path to the file.
		Path base.Path
		// Project is the project that the file is in.
		Project *Project
		// Start is the token that started the file.
		Start *Token
		// LineStarts is the offset each line of the file begins at, which is what
		// turns a token's offset into a line and a column. It is kept here because
		// the lexer that collected it is gone by the time anything downstream of
		// parsing has an error to report about a node.
		LineStarts []uint
		// HasSyntaxErrors says the parse recovered from errors in the file, so
		// what it failed to parse is missing from it. A file like this is only
		// ever part of an AST handed back alongside those errors.
		HasSyntaxErrors bool
		// BrokenImports says an import of the file failed to parse or to
		// resolve, so a name the file reaches through it cannot be resolved
		// whether or not it was written correctly.
		BrokenImports bool

		// Imports maps each id to the imports written under it. The common case
		// is one import per id, but a package name may hold several: a user who
		// writes `import std("Err.zam")` and `import std("Strs.zam")` has both
		// under the "std" key, and a lookup picks the one whose top-level decl
		// answers to the type name.
		Imports map[string][]*Import
		// Decl is the declaration of the file.
		Decl Decl
	}

	// Import is an import in the file.
	Import struct {
		// Start is the token that started the import.
		Start *Token
		// File is the file that the import is in.
		File *File

		// Package is the package of the import.
		Package *string
		// Alias is the alias of the import.
		Alias *string
		// Path is the path to the imported file.
		Path base.Path
		// PathToken is the string literal the path is written as.
		PathToken *Token

		// ImportedFile is the file that the import is importing.
		ImportedFile *File
	}

	// ImportScope is the imports a block has brought into scope by some point
	// in it. Each import statement in a block starts a new scope, so a type
	// written before the statement does not see what it imports.
	ImportScope struct {
		// Outer is the scope of the enclosing block, or nil for a block whose
		// only enclosing imports are the file's.
		Outer *ImportScope
		// Imports maps each id to the imports written under it in the block
		// up to and including the statement that started the scope.
		Imports map[string][]*Import
	}
)

// newFile creates a new file in the project.
func newFile(parser SyntaxParser, path base.Path) (*File, error) {
	start, err := parser.Peek()
	if err != nil {
		return nil, err
	}

	imports := make(map[string][]*Import)
	brokenImports := false
	for {
		tok, err := parser.Match(TTImport)
		if err != nil {
			return nil, err
		} else if tok == nil {
			break
		}

		importStmts, ok, err := parseImport(parser, tok)
		if err != nil {
			return nil, err
		}
		brokenImports = brokenImports || !ok
		addImports(parser, imports, importStmts)
	}

	f := File{
		Path:          path,
		Project:       parser.Project(),
		Start:         start,
		Imports:       imports,
		BrokenImports: brokenImports,
	}
	*parser.File() = &f
	for _, importStmts := range imports {
		for _, importStmt := range importStmts {
			importStmt.File = &f
		}
	}

	modifiers, err := newModifiers(parser)
	if err != nil {
		return nil, err
	} else if modifiers&^ModifierPublic != 0 {
		return nil, fmt.Errorf("a top-level declaration can only be public")
	}
	if f.Decl, err = newDecl(parser, modifiers); err != nil {
		return nil, err
	}

	return &f, nil
}

// parseImport reads the import statement tok starts, either one import or a
// braced group of them, answering false when any of it was broken and has been
// recovered from. What was read whole is answered either way.
func parseImport(parser SyntaxParser, tok *Token) ([]*Import, bool, error) {
	if lb, err := parser.Match(TTLeftBrace); err != nil {
		return nil, false, err
	} else if lb == nil {
		return parseImportSpec(parser, tok)
	}

	var imports []*Import
	ok := true
	for {
		if rb, err := parser.Match(TTRightBrace); err != nil {
			return nil, false, err
		} else if rb != nil {
			return imports, ok, nil
		}

		start, err := parser.Peek()
		if err != nil {
			return nil, false, err
		}
		specs, specOK, err := parseImportSpec(parser, start)
		if err != nil {
			return nil, false, err
		}
		imports = append(imports, specs...)
		ok = ok && specOK
	}
}

// parseImportSpec reads one import from tok to its `;`, answering false when
// it was broken and has been recovered from.
func parseImportSpec(parser SyntaxParser, tok *Token) ([]*Import, bool, error) {
	recoverFrom := parser.Checkpoint()
	importStmts, err := newImport(parser, tok)
	if err == nil {
		return importStmts, true, nil
	}

	// An import that was read whole but names nothing has no rest of the
	// statement to skip, and skipping to the next `;` would take the next
	// import with it.
	var unresolved *unresolvedImport
	if errors.As(err, &unresolved) {
		parser.ReportAt(unresolved.err, tok)
		return nil, false, nil
	}
	if err := recoverFrom(err); err != nil {
		return nil, false, err
	}
	return nil, false, nil
}

// addImports adds importStmts to imports, reporting any that duplicate a name
// already there.
//
// An import binds its alias or else its package name, followed by the name of
// the file it imports. An id may therefore hold many imports, as an alias over
// a glob does, but two different files of one name under one id are reached by
// the same name and collide, as does one file imported twice.
func addImports(parser SyntaxParser, imports map[string][]*Import, importStmts []*Import) {
	for _, importStmt := range importStmts {
		id := importStmt.ID()
		if collides := collidingImport(imports[id], importStmt); collides != nil {
			if collides.Path == importStmt.Path {
				parser.Report(fmt.Errorf("duplicate import %q of %q", importStmt.boundName(), importStmt.Path))
			} else {
				parser.Report(fmt.Errorf("duplicate import %q: %q collides with %q", importStmt.boundName(), importStmt.Path, collides.Path))
			}
			continue
		}
		imports[id] = append(imports[id], importStmt)
	}
}

// collidingImport answers the import among existing that binds the same name
// as importStmt, or nil when none does. All of existing share an id with
// importStmt.
func collidingImport(existing []*Import, importStmt *Import) *Import {
	for _, prev := range existing {
		if prev.fileName() == importStmt.fileName() {
			return prev
		}
	}
	return nil
}

// Accept implements the [FileVisitor] interface.
func (f *File) Accept(visitor FileVisitor) error {
	return visitor.VisitFile(f)
}

// Location answers where tok was written. Every node carries the token it starts
// at, so this is how a pass that runs long after parsing says where the source it
// is complaining about is. A nil token names the file and nothing finer.
func (f *File) Location(tok *Token) *base.Location {
	if tok == nil {
		return base.NewLocation(f.Path, nil)
	}
	return base.NewLocation(f.Path, tok.Position(f.LineStarts))
}

// globSuffix is the one spelling the import grammar reads as a directory glob:
// `path/*` says every code file sitting directly in `path/`. The suffix is
// matched as written, not parsed, since nothing else in an import path takes a
// `*` and treating one as a wildcard anywhere else would quietly change what a
// typo meant.
const globSuffix = "/*"

// newImport reads one import statement and answers the imports it stands for.
// A written path names one file and expands into one Import; a path ending in
// `/*` names the code files sitting directly in that directory and expands
// into one Import per match. Either way the caller sees a list, so the two
// forms are the same thing to downstream code.
func newImport(parser SyntaxParser, start *Token) ([]*Import, error) {
	var alias *string
	var pkg *string

	if tok, err := parser.Match(TTIdentifier); err != nil {
		return nil, err
	} else if tok != nil {
		name := tok.Value.(string)
		if eq, err := parser.Match(TTEqual); err != nil {
			return nil, err
		} else if eq != nil {
			alias = new(string)
			*alias = name

			if tok, err := parser.Match(TTIdentifier); err != nil {
				return nil, err
			} else if tok != nil {
				pkg = new(string)
				*pkg = tok.Value.(string)
			}
		} else {
			pkg = new(string)
			*pkg = name
		}
	}

	var pathTok *Token
	if pkg != nil {
		if _, err := parser.Consume(TTLeftParen); err != nil {
			return nil, err
		}
		tok, err := parser.Consume(TTStringLiteral)
		if err != nil {
			return nil, err
		}
		pathTok = tok
		if _, err := parser.Consume(TTRightParen); err != nil {
			return nil, err
		}
	} else {
		tok, err := parser.Consume(TTStringLiteral)
		if err != nil {
			return nil, err
		}
		pathTok = tok
	}

	if _, err := parser.Consume(TTSemiColon); err != nil {
		return nil, err
	}

	imports, err := resolveImport(parser, start, pathTok, pkg, alias)
	if err != nil {
		return nil, &unresolvedImport{err: err}
	}
	return imports, nil
}

// unresolvedImport is an import statement read whole that names nothing that
// can be imported.
type unresolvedImport struct {
	err error
}

// Error implements the [error] interface.
func (u *unresolvedImport) Error() string {
	return u.err.Error()
}

// resolveImport answers the imports a whole import statement names.
func resolveImport(parser SyntaxParser, start, pathTok *Token, pkg, alias *string) ([]*Import, error) {
	pathStr := pathTok.Value.(string)
	// The std package is reachable like any other dependency, so pkg == "std"
	// is a legitimate import now; what is still refused is naming a file or a
	// different package under the alias `std`, since that would shadow the
	// standard library's namespace locally and leave `std.Object` meaning two
	// things in one file.
	if alias != nil && *alias == base.StdPackage {
		return nil, fmt.Errorf("%q is a reserved alias", base.StdPackage)
	}

	if strings.HasSuffix(pathStr, globSuffix) || pathStr == "*" {
		return newGlobImport(parser, start, pathTok, pkg, alias)
	}

	path, err := resolveImportPath(parser, pkg, pathStr)
	if err != nil {
		return nil, err
	}
	parser.AddPath(path)

	return []*Import{{
		Start: start,
		File:  *parser.File(),

		Package:   pkg,
		Alias:     alias,
		Path:      path,
		PathToken: pathTok,
	}}, nil
}

// resolveImportPath answers the one path a non-glob import names, read as it
// was written: a package import against the dependency the project declared
// under that name, a plain import beside the importing file.
//
// A package name that was not declared is an error rather than a silent fall
// back to a relative path, because the two answer with different files and
// only the project file says which of them was meant. A relative import that
// wants a name of its own writes the alias form instead. A fetched project's
// plain imports may not leave the directory it was extracted to.
func resolveImportPath(parser SyntaxParser, pkg *string, pathStr string) (base.Path, error) {
	project := parser.Project()
	if pkg != nil {
		dependency, ok := project.Dependencies[*pkg]
		if !ok {
			return "", fmt.Errorf("no dependency %q declared by project %q", *pkg, project.Name)
		}
		resolved, err := dependency.ResolveInto(pathStr)
		if err != nil {
			return "", err
		}
		return parser.NewPath(resolved)
	}

	path, err := parser.NewPath(pathStr)
	if err != nil {
		return "", err
	}
	if project.Root != "" && !base.IsStdPath(path) && !project.Root.Contains(path) {
		return "", fmt.Errorf("import %q leaves %s", pathStr, project.Archive)
	}
	return path, nil
}

// newGlobImport expands a `dir/*` import into one Import per code file sitting
// directly inside dir. The glob is read before the path is resolved so the `*`
// never reaches [base.ParsePath], which would turn it into part of a filename
// and leave a path nothing can open.
//
// An alias on a glob names the whole set, as a package name does, and each
// file is reached through it by its own name. A glob that matches nothing is
// refused for the same reason naming a missing file is: the import claims a dependency that the next build will not find, and
// catching it here keeps the mistake close to its source.
func newGlobImport(parser SyntaxParser, start, pathTok *Token, pkg, alias *string) ([]*Import, error) {
	pathStr := pathTok.Value.(string)
	dirPathStr := strings.TrimSuffix(pathStr, globSuffix)
	if dirPathStr == "" || pathStr == "*" {
		dirPathStr = "."
	}

	dirPath, err := resolveImportPath(parser, pkg, dirPathStr)
	if err != nil {
		return nil, err
	}

	children, err := parser.ListCodeFiles(dirPath)
	if err != nil {
		return nil, err
	}
	if len(children) == 0 {
		return nil, fmt.Errorf("import %q matched no %s files", pathStr, base.CodeFileExtension)
	}

	imports := make([]*Import, 0, len(children))
	for _, child := range children {
		parser.AddPath(child)
		imports = append(imports, &Import{
			Start: start,
			File:  *parser.File(),

			Package:   pkg,
			Alias:     alias,
			Path:      child,
			PathToken: pathTok,
		})
	}
	return imports, nil
}

// Accept implements the [ImportVisitor] interface.
func (i *Import) Accept(visitor FileVisitor) error {
	return visitor.VisitImport(i)
}

// fileName answers the name of the file the import names, without its
// extension.
func (i *Import) fileName() string {
	return strings.TrimSuffix(i.Path.Base(), base.CodeFileExtension)
}

// boundName answers the name the import is reached by in its scope.
func (i *Import) boundName() string {
	if i.Alias != nil || i.Package != nil {
		return i.ID() + "." + i.fileName()
	}
	return i.fileName()
}

func (i *Import) ID() string {
	if i.Alias != nil {
		return *i.Alias
	} else if i.Package != nil {
		return *i.Package
	}
	return i.fileName()
}
