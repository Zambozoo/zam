package syntax

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/zambozoo/zam/internal/base"

	"go.yaml.in/yaml/v3"
	"golang.org/x/sync/errgroup"
)

// packageNamePattern is what a package name may be spelled as: an identifier.
var packageNamePattern = regexp.MustCompile(`^[\p{L}_][\p{L}\p{N}_]*$`)

type (

	// Project is a project.
	Project struct {
		// Path is the path to the project.
		Path base.Path
		// Archive is the repo@version the project was fetched as, and is empty
		// for the project being compiled. Two archives may declare the same name
		// and are deliberately not unified, so a diagnostic that named only the
		// declaration would read as though a type were not itself. This is what
		// it is qualified by.
		Archive string
		// Root is the directory a fetched project was extracted to, which its
		// files may not import from outside of. It is empty for a project on disk.
		Root base.Path
		// Name is the name of the project.
		Name string
		// Version is the version of the project.
		Version string
		// Native is the native functions of the project.
		Native Native
		// Dependencies is the library dependencies of the project.
		Dependencies map[string]base.Path
	}

	// Native is a native function definition.
	Native struct {
		// links is additional links for the native function.
		Links []string `yaml:"links"`
		// files is the files that the native function is defined in.
		Files []string `yaml:"files"`
	}

	// projectYAML is a project in YAML.
	projectYAML struct {
		// Name is the Name of the project.
		Name string `yaml:"name"`
		// Version is the Version of the project.
		Version string `yaml:"version"`
		// Native is the Native functions of the project.
		Native Native `yaml:"native"`
		// Dependencies is the library dependencies of the project.
		Dependencies map[string]dependencyYAML `yaml:"dependencies"`
	}

	// dependencyYAML is a library dependency in YAML.
	dependencyYAML struct {
		// Repo is the repository and version, spelled repo@version.
		Repo string `yaml:"repo"`
		// Hash is the hash of the dependency's files, which is checked on every
		// build once it is set.
		Hash string `yaml:"hash"`
	}

	// dependency is a library dependency.
	dependency struct {
		pkg  string
		path base.Path
		// hash is the hash of what was fetched, when the project file had none.
		hash string
	}
)

// newImplicitProject returns the project of an entry compiled without a project
// file: named for the entry, depending only on std, and shipping no natives.
func newImplicitProject(entry base.Path) *Project {
	return &Project{
		Path:         base.ImplicitProjectFilePath(entry),
		Name:         strings.TrimSuffix(filepath.Base(string(entry)), base.CodeFileExtension),
		Dependencies: map[string]base.Path{base.StdPackage: base.StdPath("")},
	}
}

// newProject reads the project file at path and fetches its dependencies. A
// dependency's hash is checked against the project file's; one the project file
// lacks is written into it when the command allows and the project is not
// itself a fetched one.
func (p *projectParser) newProject(path base.Path) (*Project, error) {
	document, err := readProject(path, p.fileProvider)
	if err != nil {
		return nil, err
	}
	var py projectYAML
	if err := document.Decode(&py); err != nil {
		return nil, err
	}

	if _, ok := py.Dependencies[base.StdPackage]; ok {
		return nil, fmt.Errorf("%q is a reserved package name", base.StdPackage)
	}

	archive, root, _ := p.fetchedRoot(path)

	dependenciesSlice := make([]dependency, len(py.Dependencies))
	var eg errgroup.Group
	var i int
	for key, value := range py.Dependencies {
		j := i
		eg.Go(func() error {
			if value.Repo == "" {
				return fmt.Errorf("dependency %q has no repo", key)
			}
			path, hash, err := p.packageProvider.Fetch(value.Repo)
			if err != nil {
				return fmt.Errorf("failed to fetch dependency %q: %w", key, err)
			}
			p.fetched.Store(path, value.Repo)
			d := dependency{pkg: key, path: path}
			switch value.Hash {
			case hash:
			case "":
				d.hash = hash
			default:
				return fmt.Errorf("dependency %q does not match its hash: the project file has %s, %s has %s",
					key, value.Hash, value.Repo, hash)
			}
			dependenciesSlice[j] = d
			return nil
		})
		i++
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}

	dependencies := make(map[string]base.Path, len(dependenciesSlice)+1)
	missing := make(map[string]dependencyYAML)
	for _, d := range dependenciesSlice {
		dependencies[d.pkg] = d.path
		if d.hash != "" {
			missing[d.pkg] = dependencyYAML{Hash: d.hash}
		}
	}

	if p.command.WriteProject && root == "" && len(missing) > 0 {
		if err := writeDependencies(path, &document, missing); err != nil {
			return nil, fmt.Errorf("cannot record dependency hashes: %w", err)
		}
	}

	// std sits in every project's dependencies without the yaml having to say
	// so, which is what the reserved-name check above refuses to let the yaml
	// also say: writing it out would ask whether the yaml's spelling or the one
	// added here was meant. The std project itself is left out: a dependency on
	// oneself would make the graph cyclic for no reason.
	if !base.IsStdPath(path) {
		dependencies[base.StdPackage] = base.StdPath("")
	}

	return &Project{
		Path:         path,
		Archive:      archive,
		Root:         root,
		Name:         py.Name,
		Version:      py.Version,
		Native:       py.Native,
		Dependencies: dependencies,
	}, nil
}

// Get adds each of refs to the project file at projectFile, under as when it is
// given and under the last element of its repository otherwise, replacing what
// a dependency of the same name had. It then fetches every dependency the
// project has, and every dependency those have, recording the hashes the
// project file lacks.
func Get(projectFile base.Path, refs []string, as string, fileProvider fileProvider, packages packageProvider) error {
	p := &projectParser{
		command:         &base.Command{WriteProject: true},
		fileProvider:    fileProvider,
		packageProvider: packages,
		fetched:         &base.SyncMap[base.Path, string]{},
	}

	if len(refs) > 0 {
		added := make(map[string]dependencyYAML, len(refs))
		for _, ref := range refs {
			name := as
			if name == "" {
				name = packageName(ref)
			}
			if err := checkPackageName(name); err != nil {
				return err
			}
			if _, ok := added[name]; ok {
				return fmt.Errorf("package %q is named by more than one dependency", name)
			}
			_, hash, err := packages.Fetch(ref)
			if err != nil {
				return fmt.Errorf("failed to fetch %s: %w", ref, err)
			}
			added[name] = dependencyYAML{Repo: ref, Hash: hash}
		}

		document, err := readProject(projectFile, fileProvider)
		if err != nil {
			return err
		}
		if err := writeDependencies(projectFile, &document, added); err != nil {
			return err
		}
	}

	visited := make(base.Set[base.Path])
	var visit func(path base.Path) error
	visit = func(path base.Path) error {
		if _, ok := visited[path]; ok {
			return nil
		}
		visited[path] = struct{}{}

		project, err := p.newProject(path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		for pkg, dir := range project.Dependencies {
			if base.IsStdPath(dir) {
				continue
			}
			dependencyFile, err := fileProvider.ProjectFilePath(base.Path(filepath.Join(string(dir), "zam.yml")))
			if err != nil || !dir.Contains(dependencyFile) {
				return fmt.Errorf("dependency %q has no project file", pkg)
			}
			if err := visit(dependencyFile); err != nil {
				return err
			}
		}
		return nil
	}
	return visit(projectFile)
}

// packageName returns the package name a dependency is added under by default:
// the last element of its repository.
func packageName(ref string) string {
	repo, _, _ := strings.Cut(ref, "@")
	return path.Base(repo)
}

// checkPackageName refuses a package name an import could not be written with.
func checkPackageName(name string) error {
	_, keyword := keywords[name]
	switch {
	case name == base.StdPackage:
		return fmt.Errorf("%q is a reserved package name", name)
	case keyword || name == "_" || !packageNamePattern.MatchString(name):
		return fmt.Errorf("%q is not a valid package name: name it with --as", name)
	}
	return nil
}

// readProject reads the project file at path as a YAML document.
func readProject(path base.Path, fileProvider fileProvider) (yaml.Node, error) {
	var document yaml.Node
	r, err := fileProvider.Reader(path)
	if err != nil {
		return document, err
	}
	source, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		return document, err
	}
	err = yaml.Unmarshal(source, &document)
	return document, err
}

// writeDependencies sets the fields each of dependencies has on the dependency
// of the same name in document, adding those it lacks, and writes document
// back to the project file at path.
func writeDependencies(path base.Path, document *yaml.Node, dependencies map[string]dependencyYAML) error {
	if len(document.Content) == 0 {
		return fmt.Errorf("%s is empty", path)
	}
	mapping, err := mappingEntry(document.Content[0], "dependencies")
	if err != nil {
		return err
	}

	names := make([]string, 0, len(dependencies))
	for name := range dependencies {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry, err := mappingEntry(mapping, name)
		if err != nil {
			return fmt.Errorf("dependency %w", err)
		}
		if repo := dependencies[name].Repo; repo != "" {
			setScalar(entry, "repo", repo)
		}
		if hash := dependencies[name].Hash; hash != "" {
			setScalar(entry, "hash", hash)
		}
	}

	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}

	info, err := os.Stat(string(path))
	if err != nil {
		return err
	}
	return os.WriteFile(string(path), buffer.Bytes(), info.Mode().Perm())
}

// mappingEntry returns the mapping under key in the mapping node, adding an
// empty one when key is missing or holds nothing.
func mappingEntry(node *yaml.Node, key string) (*yaml.Node, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%q is not inside a mapping", key)
	}
	value := mappingValue(node, key)
	switch {
	case value == nil:
		value = &yaml.Node{Kind: yaml.MappingNode}
		node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, value)
	case value.Kind == yaml.ScalarNode && value.Tag == "!!null":
		value.Kind, value.Tag, value.Value = yaml.MappingNode, "", ""
	case value.Kind != yaml.MappingNode:
		return nil, fmt.Errorf("%q is not a mapping", key)
	}
	return value, nil
}

// setScalar sets key in the mapping node to value, adding it when missing.
func setScalar(node *yaml.Node, key, value string) {
	if existing := mappingValue(node, key); existing != nil {
		existing.Kind, existing.Tag, existing.Value = yaml.ScalarNode, "", value
		return
	}
	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Value: value})
}

// mappingValue returns the value under key in the mapping node, or nil.
func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
