package syntax

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"

	base "github.com/zambozoo/zam/internal/0_base"
	command "github.com/zambozoo/zam/internal/1_command"
)

type (
	fileProvider interface {
		FileExists(path *base.Path) (bool, error)
		ProjectFilePath(path *base.Path) (*base.Path, error)
		Reader(path *base.Path) (io.ReadCloser, error)
		NewPath(pathStr string) (*base.Path, error)
		ListCodeFiles(dir *base.Path) ([]*base.Path, error)
	}

	gitProvider interface {
		Download(archiveURL string) (*base.Path, error)
		ArchiveName(file string) string
	}

	lexer interface {
		Next() (*Token, error)
	}

	// AST is the abstract syntax tree of the project.
	AST struct {
		// MainCodeDecl is the main code declaration of the AST.
		MainCodeDecl Decl
		// Projects are the projects in the AST.
		Projects *base.StaticOrderedMap[base.Path, *Project]
		// Files are the files in the AST.
		CodeFiles *base.StaticOrderedMap[base.Path, *File]
		// ThreadTypes is the set of thread types.
		ThreadTypes TypeSet
		// FunctionTypes is the set of function types.
		FunctionTypes TypeSet
		// PointerTypes is the set of pointer types. Each is named by an id of
		// its own, so a pointer to an array and an array of pointers stay
		// distinct types rather than collapsing into one depth and one length.
		PointerTypes TypeSet
	}

	// syntaxParser is a parser for a single code file.
	syntaxParser struct {
		projectParser *projectParser
		lexer         lexer
		project       *Project
		// path is where the file being parsed was read from. It is held here
		// rather than taken from file because imports are read before there is a
		// file to take it from.
		path *base.Path
		file *File

		currentToken  *Token
		currentDecl   Decl
		currentMethod *FieldMethod
	}

	// projectParser is the parser for an entire project.
	projectParser struct {
		// command is the parsed command line arguments
		command *command.Command
		// fileProvider provides access to the file system
		fileProvider fileProvider
		// gitProvider provides access to git repositories
		gitProvider gitProvider

		// pathWG counts the paths that have been discovered but not yet parsed
		pathWG sync.WaitGroup
		// threadWG is the wait group for the parsing threads and the pump
		threadWG sync.WaitGroup
		// errWG is the wait group for the error drainer
		errWG sync.WaitGroup

		// projectSG is the singleflight group for parsing project files
		projectFileSG base.SingleflightGroup[*Project]
		// codeFileSG is the singleflight group for parsing code files
		codeFileSG base.SingleflightGroup[*File]

		// discoveredPaths carries a path from the parse that found it to the pump
		discoveredPaths chan *base.Path
		// pathsToParse is where the parsing threads take their work from
		pathsToParse chan *base.Path
		// errChan is the channel for the errors
		errChan chan error

		// projects is the map of projects
		projects *base.SyncMap[base.Path, *Project]
		// codeFiles is the map of code files
		codeFiles *base.SyncMap[base.Path, *File]
		// errs is the resulting list of errors
		errs []error
	}
)

func newSyntaxParser(projectParser *projectParser, lexer lexer, project *Project, path *base.Path) *syntaxParser {
	return &syntaxParser{
		projectParser: projectParser,
		lexer:         lexer,
		project:       project,
		path:          path,
	}
}

func (p *syntaxParser) initCurrentToken() error {
	if p.currentToken != nil {
		return nil
	}

	tok, err := p.lexer.Next()
	if err != nil {
		return err
	}
	p.currentToken = tok

	return nil
}

func (p *syntaxParser) UnexpectedToken(got TokenKind, want ...TokenKind) error {
	wantStrings := make([]string, 0, len(want))
	for _, w := range want {
		wantStrings = append(wantStrings, fmt.Sprintf("%q", w))
	}

	if len(wantStrings) == 1 {
		return fmt.Errorf("unexpected token: %q, expected %s", got, wantStrings[0])
	}

	return fmt.Errorf("unexpected token: %q, expected one of %s, or %s", got, strings.Join(wantStrings[:len(wantStrings)-1], ", "), wantStrings[len(wantStrings)-1])
}

func (p *syntaxParser) Match(kinds ...TokenKind) (*Token, error) {
	if err := p.initCurrentToken(); err != nil {
		return nil, err
	}

	if !slices.Contains(kinds, p.currentToken.Kind) {
		return nil, nil
	}
	oldToken := p.currentToken

	tok, err := p.lexer.Next()
	if err != nil {
		return nil, err
	}
	p.currentToken = tok
	return oldToken, nil
}

func (p *syntaxParser) ConsumeAny(kinds ...TokenKind) (*Token, error) {
	tok, err := p.Match(kinds...)
	if err != nil {
		return nil, err
	} else if tok == nil {
		return nil, p.UnexpectedToken(p.currentToken.Kind, kinds...)
	}
	return tok, nil
}

func (p *syntaxParser) ConsumeAll(kinds ...TokenKind) ([]Token, error) {
	toks := make([]Token, 0, len(kinds))
	for _, kind := range kinds {
		tok, err := p.Consume(kind)
		if err != nil {
			return nil, err
		}
		toks = append(toks, *tok)
	}

	return toks, nil
}

func (p *syntaxParser) Consume(kind TokenKind) (*Token, error) {
	tok, err := p.Match(kind)
	if err != nil {
		return nil, err
	} else if tok == nil {
		return nil, p.UnexpectedToken(p.currentToken.Kind, kind)
	}
	return tok, nil
}

func (p *syntaxParser) Peek() (*Token, error) {
	if err := p.initCurrentToken(); err != nil {
		return nil, err
	}
	return p.currentToken, nil
}

func (p *syntaxParser) Decl() *Decl {
	return &p.currentDecl
}

// Method returns a pointer to the method currently being parsed. The pointer
// is held by the parser so a generic argument named inside a method's
// signature can resolve to the method's own parameter list.
func (p *syntaxParser) Method() **FieldMethod {
	return &p.currentMethod
}

// NewPath answers the path an import names, read as it was written: beside the
// file it appears in. A file's imports mean the same thing wherever the compiler
// is run from, and moving a directory moves what is inside it intact.
func (p *syntaxParser) NewPath(pathStr string) (*base.Path, error) {
	return p.projectParser.fileProvider.NewPath(p.path.Resolve(pathStr))
}

func (p *syntaxParser) AddPath(path *base.Path) {
	p.projectParser.addPath(path)
}

// ListCodeFiles answers the code files sitting directly in dir, in the shape
// a glob import expects: one Path per child, non-recursive. The call is a
// pass-through to the file provider, kept on the parser so that the glob
// expansion in [newImport] has the same reach as the single-file resolution
// through [AddPath].
func (p *syntaxParser) ListCodeFiles(dir *base.Path) ([]*base.Path, error) {
	return p.projectParser.fileProvider.ListCodeFiles(dir)
}

func (p *syntaxParser) Project() *Project {
	return p.project
}

func (p *syntaxParser) File() **File {
	return &p.file
}

func newProjectParser(cmd *command.Command, fileProvider fileProvider, gitProvider gitProvider) *projectParser {
	p := &projectParser{
		command:      cmd,
		fileProvider: fileProvider,
		gitProvider:  gitProvider,

		discoveredPaths: make(chan *base.Path),
		pathsToParse:    make(chan *base.Path),
		errChan:         make(chan error, cmd.MaxThreads),

		projects:  &base.SyncMap[base.Path, *Project]{},
		codeFiles: &base.SyncMap[base.Path, *File]{},
	}

	// The drainer and the pump go first: the entry path and any error found
	// before something is reading would otherwise have nowhere to go.
	p.errWG.Go(func() {
		for err := range p.errChan {
			p.errs = append(p.errs, err)
		}
	})
	p.threadWG.Go(p.pump)

	p.addPath(cmd.InputPath)

	return p
}

// addPath hands path to the pump to be parsed.
//
// A file's imports are found while it is being parsed, so the thread that finds
// them is one of the threads that would have to take them off a queue. Handing
// them to a queue of a fixed size deadlocks the moment a file has more imports
// than the queue has room for — which, at the default of one thread and so a
// queue of one, is any file with two imports. The pump's queue grows instead of
// filling, so a thread is never made to wait on itself.
func (p *projectParser) addPath(path *base.Path) {
	p.pathWG.Add(1)
	p.discoveredPaths <- path
}

// pump holds the paths waiting to be parsed and feeds them to the parsing
// threads one at a time.
//
// The select is what keeps a parsing thread from ever blocking on addPath: the
// receive from discoveredPaths stays open whether or not any thread is ready to
// take work, so there is always somewhere for a newly found import to go.
func (p *projectParser) pump() {
	var queue []*base.Path
	for {
		// A nil channel is never chosen, so with nothing queued the select waits
		// only on the paths coming in.
		var out chan *base.Path
		var next *base.Path
		if len(queue) > 0 {
			out, next = p.pathsToParse, queue[0]
		}

		select {
		case path, ok := <-p.discoveredPaths:
			if !ok {
				// discoveredPaths closes once every added path has been parsed,
				// so nothing is left queued to hand on.
				close(p.pathsToParse)
				return
			}
			queue = append(queue, path)
		case out <- next:
			queue = queue[1:]
		}
	}
}

func (p *projectParser) AddError(err error) {
	p.errChan <- err
}

func (p *projectParser) parseFile(path *base.Path) (*File, error) {
	defer p.pathWG.Done()

	file, err, _ := p.codeFileSG.Do(path.String(), func() (*File, error) {
		if file, loaded := p.codeFiles.Load(*path); loaded {
			return file, nil
		}

		projectFilePath, err := p.fileProvider.ProjectFilePath(path)
		if err != nil {
			return nil, base.NewError(err, base.NewLocation(*path, nil))
		}

		project, err, _ := p.projectFileSG.Do(projectFilePath.String(), func() (*Project, error) {
			if proj, ok := p.projects.Load(*projectFilePath); ok {
				return proj, nil
			}

			project, err := newProject(projectFilePath, p.fileProvider, p.gitProvider)
			if err != nil {
				return nil, err
			}

			p.projects.Store(*projectFilePath, project)
			return project, nil
		})
		if err != nil {
			return nil, base.NewError(err, base.NewLocation(*projectFilePath, nil))
		}

		reader, err := p.fileProvider.Reader(path)
		if err != nil {
			return nil, base.NewError(err, base.NewLocation(*path, nil))
		}

		lexer, err := NewLexer(newRuneReader(reader))
		if err != nil {
			return nil, base.NewError(err, base.NewLocation(*path, nil))
		}

		// Every error a parse reports is reported here, so where the parse stopped
		// is where all of them are located from. That is the token the parser had
		// reached: a parse gives up where it fails, and the errors it raises are
		// about the token it could not go past. A parse that failed before reading
		// a token has only the file to name.
		syntaxParser := newSyntaxParser(p, lexer, project, path)
		file, err := newFile(syntaxParser, path)
		if err != nil {
			var position *base.Position
			if syntaxParser.currentToken != nil {
				position = syntaxParser.currentToken.Position(lexer.LineStarts)
			}
			return nil, base.NewError(err, base.NewLocation(*path, position))
		}
		file.LineStarts = lexer.LineStarts

		p.codeFiles.Store(*path, file)
		return file, nil
	})
	return file, err
}

// Run starts the parsing threads. How many files are read at once is how many
// threads there are, which is what the thread count on the command line asks for.
func (p *projectParser) Run() {
	for range p.command.MaxThreads {
		p.threadWG.Go(func() {
			for path := range p.pathsToParse {
				if _, err := p.parseFile(path); err != nil {
					p.AddError(err)
				}
			}
		})
	}
}

// Wait blocks until every path reachable from the entry file has been parsed.
// A path is added from inside a parse that is itself still counted, so the group
// cannot reach zero while a file remains to be discovered.
func (p *projectParser) Wait() {
	p.pathWG.Wait()
	close(p.discoveredPaths)
	p.threadWG.Wait()
	close(p.errChan)
	p.errWG.Wait()
}

// NewAST creates a new AST from the command line arguments and file provider.
func NewAST(cmd *command.Command, fileProvider fileProvider, gitProvider gitProvider) (*AST, []error) {
	parser := newProjectParser(cmd, fileProvider, gitProvider)
	parser.Run()
	parser.Wait()

	if len(parser.errs) > 0 {
		return nil, parser.errs
	}

	// The entry file is not a special case of parsing, only of the tree: it is
	// the one the run begins in. Naming it by path is what keeps which file that
	// is from depending on which parse happened to go first.
	mainCodeFile, _ := parser.codeFiles.Load(*cmd.InputPath)

	return &AST{
		MainCodeDecl: mainCodeFile.Decl,
		Projects:     parser.projects.ToMap(base.ComparePath),
		CodeFiles:    parser.codeFiles.ToMap(base.ComparePath),
	}, nil
}
