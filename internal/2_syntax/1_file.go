package syntax

import (
	"fmt"
	"strings"

	base "github.com/zambozoo/zam/internal/0_base"
)

type (
	FileVisitor interface {
		VisitFile(f *File) error
		VisitImport(i *Import) error
	}

	// File is a file in the project.
	File struct {
		// Path is the path to the file.
		Path *base.Path
		// Project is the project that the file is in.
		Project *Project
		// Start is the token that started the file.
		Start *Token
		// LineStarts is the offset each line of the file begins at, which is what
		// turns a token's offset into a line and a column. It is kept here because
		// the lexer that collected it is gone by the time anything downstream of
		// parsing has an error to report about a node.
		LineStarts []uint

		// Imports maps each id to the imports written under it. The common case
		// is one import per id, but a package name may hold several: a user who
		// writes `import std("Pair.zam")` has reached one file in the stdlib
		// through the std package, and the implicit `std.Object` auto-import
		// still has to find its own, so both sit here under the "std" key and a
		// lookup picks the one whose top-level decl answers to the type name.
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
		Path *base.Path

		// ImportedFile is the file that the import is importing.
		ImportedFile *File
	}
)

// newFile creates a new file in the project.
func newFile(parser SyntaxParser, path *base.Path) (*File, error) {
	start, err := parser.Peek()
	if err != nil {
		return nil, err
	}

	imports := make(map[string][]*Import)
	for {
		tok, err := parser.Match(TTImport)
		if err != nil {
			return nil, err
		} else if tok == nil {
			break
		}

		importStmts, err := newImport(parser, tok)
		if err != nil {
			return nil, err
		}

		for _, importStmt := range importStmts {
			id := importStmt.ID()
			// Two imports under one id are allowed only when neither carries
			// an alias: a package name may hold more than one file, since
			// every file in the package has a decl of its own to be reached
			// by name, but an alias is a renaming and renaming the same
			// thing twice is the error the duplicate check caught in the
			// first place. A glob expands into unaliased imports under one
			// id per file it matched, which the same rule covers.
			if existing := imports[id]; len(existing) > 0 {
				if importStmt.Alias != nil {
					return nil, fmt.Errorf("duplicate import %q", id)
				}
				for _, prev := range existing {
					if prev.Alias != nil {
						return nil, fmt.Errorf("duplicate import %q", id)
					}
				}
			}
			imports[id] = append(imports[id], importStmt)
		}
	}

	f := File{
		Path:    path,
		Project: parser.Project(),
		Start:   start,
		Imports: imports,
	}
	*parser.File() = &f

	if f.Decl, err = newDecl(parser, ModifierPublic); err != nil {
		return nil, err
	}

	return &f, nil
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
		return base.NewLocation(*f.Path, nil)
	}
	return base.NewLocation(*f.Path, tok.Position(f.LineStarts))
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

	var pathStr string
	if pkg != nil {
		if _, err := parser.Consume(TTLeftParen); err != nil {
			return nil, err
		}
		tok, err := parser.Consume(TTStringLiteral)
		if err != nil {
			return nil, err
		}
		pathStr = tok.Value.(string)
		if _, err := parser.Consume(TTRightParen); err != nil {
			return nil, err
		}
	} else {
		tok, err := parser.Consume(TTStringLiteral)
		if err != nil {
			return nil, err
		}
		pathStr = tok.Value.(string)
	}

	if _, err := parser.Consume(TTSemiColon); err != nil {
		return nil, err
	}

	// The std package is reachable like any other dependency, so pkg == "std"
	// is a legitimate import now; what is still refused is naming a file or a
	// different package under the alias `std`, since that would shadow the
	// standard library's namespace locally and leave `std.Object` meaning two
	// things in one file.
	if alias != nil && *alias == base.StdPackage {
		return nil, fmt.Errorf("%q is a reserved alias", base.StdPackage)
	}

	if strings.HasSuffix(pathStr, globSuffix) || pathStr == "*" {
		return newGlobImport(parser, start, pkg, alias, pathStr)
	}

	path, err := resolveImportPath(parser, pkg, pathStr)
	if err != nil {
		return nil, err
	}
	parser.AddPath(path)

	return []*Import{{
		Start: start,
		File:  *parser.File(),

		Package: pkg,
		Alias:   alias,
		Path:    path,
	}}, nil
}

// resolveImportPath answers the one path a non-glob import names, read as it
// was written: a package import against the dependency the project declared
// under that name, a plain import beside the importing file.
//
// A package name that was not declared is an error rather than a silent fall
// back to a relative path, because the two answer with different files and
// only the project file says which of them was meant. A relative import that
// wants a name of its own writes the alias form instead.
func resolveImportPath(parser SyntaxParser, pkg *string, pathStr string) (*base.Path, error) {
	resolved := pathStr
	if pkg != nil {
		project := parser.Project()
		dependency, ok := project.Dependencies[*pkg]
		if !ok {
			return nil, fmt.Errorf("no dependency %q declared by project %q", *pkg, project.Name)
		}
		resolved = dependency.ResolveInto(pathStr)
	}
	return parser.NewPath(resolved)
}

// newGlobImport expands a `dir/*` import into one Import per code file sitting
// directly inside dir. The glob is read before the path is resolved so the `*`
// never reaches [base.ParsePath], which would turn it into part of a filename
// and leave a path nothing can open.
//
// Aliasing a glob is refused rather than treated as a prefix: the alias names
// one thing in the local scope, and a glob names many; there is no mapping
// from one name to many files the rest of the compiler would read. A glob
// that matches nothing is refused for the same reason naming a missing file
// is: the import claims a dependency that the next build will not find, and
// catching it here keeps the mistake close to its source.
func newGlobImport(parser SyntaxParser, start *Token, pkg, alias *string, pathStr string) ([]*Import, error) {
	if alias != nil {
		return nil, fmt.Errorf("alias %q cannot be used with a glob import %q", *alias, pathStr)
	}

	dirPathStr := strings.TrimSuffix(pathStr, globSuffix)
	if dirPathStr == "" {
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

			Package: pkg,
			Path:    child,
		})
	}
	return imports, nil
}

// Accept implements the [ImportVisitor] interface.
func (i *Import) Accept(visitor FileVisitor) error {
	return visitor.VisitImport(i)
}

func (i *Import) ID() string {
	if i.Alias != nil {
		return *i.Alias
	} else if i.Package != nil {
		return *i.Package
	}
	return strings.TrimSuffix(i.Path.Base(), base.CodeFileExtension)
}
