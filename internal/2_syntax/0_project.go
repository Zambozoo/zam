package syntax

import (
	"fmt"

	base "github.com/zambozoo/zam/internal/0_base"

	"go.yaml.in/yaml/v3"
	"golang.org/x/sync/errgroup"
)

type (

	// Project is a project.
	Project struct {
		// Path is the path to the project.
		Path *base.Path
		// Archive is the URL tail the project was downloaded from, and is empty
		// for the project being compiled. Two archives may declare the same name
		// and are deliberately not unified, so a diagnostic that named only the
		// declaration would read as though a type were not itself. This is what
		// it is qualified by.
		Archive string
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
		Dependencies map[string]string `yaml:"dependencies"`
	}

	// dependency is a library dependency.
	dependency struct {
		pkg  string
		path base.Path
	}
)

// newProject resolves path via streamer, reads YAML from Reader, and returns a populated Project.
func newProject(path *base.Path, fp fileProvider, git gitProvider) (*Project, error) {
	r, err := fp.Reader(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	var py projectYAML
	if err := yaml.NewDecoder(r).Decode(&py); err != nil {
		return nil, err
	}

	if _, ok := py.Dependencies[base.StdPackage]; ok {
		return nil, fmt.Errorf("%q is a reserved package name", base.StdPackage)
	}

	dependenciesSlice := make([]dependency, len(py.Dependencies))
	var eg errgroup.Group
	var i int
	for key, value := range py.Dependencies {
		j := i
		eg.Go(func() error {
			path, err := git.Download(value)
			if err != nil {
				return fmt.Errorf("failed to download dependency %q: %w", key, err)
			}
			dependenciesSlice[j] = dependency{
				pkg:  key,
				path: *path,
			}
			return nil
		})
		i++
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}

	dependencies := make(map[string]base.Path, len(dependenciesSlice)+1)
	for _, d := range dependenciesSlice {
		dependencies[d.pkg] = d.path
	}

	// std sits in every project's dependencies without the yaml having to say
	// so, which is what the reserved-name check above refuses to let the yaml
	// also say: writing it out would ask whether the yaml's spelling or the one
	// added here was meant. The std project itself is left out: a dependency on
	// oneself would make the graph cyclic for no reason.
	if !base.IsStdPath(path) {
		dependencies[base.StdPackage] = *base.StdPath("")
	}

	// Only a project read out of an archive was downloaded, and only a download
	// has a URL to be named by.
	var archive string
	if path.InZip() {
		archive = git.ArchiveName(path.Outer)
	}

	return &Project{
		Path:         path,
		Archive:      archive,
		Name:         py.Name,
		Version:      py.Version,
		Native:       py.Native,
		Dependencies: dependencies,
	}, nil
}
