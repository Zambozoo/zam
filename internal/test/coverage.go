package test

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/zambozoo/zam/internal/base"
	codegen "github.com/zambozoo/zam/internal/compiler/2_codegen"
)

// coverageVariable names the file a program compiled for coverage writes
// which of its statements ran to.
const coverageVariable = "ZAM_COVERAGE"

// coverFiles answers the file each of count test files' processes writes its
// coverage to in dir.
func coverFiles(dir string, count int) []string {
	files := make([]string, count)
	for i := range files {
		files[i] = filepath.Join(dir, "cover-"+strconv.Itoa(i))
	}
	return files
}

// writeCoverage writes the coverage of statements that the processes recorded
// in runs to path as LCOV, a line covered when a statement starting on it ran,
// and how much of them ran to out. A process that recorded nothing, as one
// ended by a fault does, adds nothing.
func writeCoverage(path string, statements []codegen.CoveredStatement, runs []string, out io.Writer) error {
	ran := make([]bool, len(statements))
	for _, run := range runs {
		counters, err := os.ReadFile(run)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return fmt.Errorf("cannot read coverage: %w", err)
		}
		if len(counters) != len(statements) {
			return fmt.Errorf("coverage of %d statements recorded for %d", len(counters), len(statements))
		}
		for i, counter := range counters {
			ran[i] = ran[i] || counter != 0
		}
	}

	lines := map[base.Path]map[int]bool{}
	covered := 0
	for i, statement := range statements {
		if lines[statement.Path] == nil {
			lines[statement.Path] = map[int]bool{}
		}
		lines[statement.Path][statement.Line] = lines[statement.Path][statement.Line] || ran[i]
		if ran[i] {
			covered++
		}
	}

	var b strings.Builder
	for _, file := range slices.Sorted(maps.Keys(lines)) {
		fmt.Fprintf(&b, "SF:%s\n", file)
		hit := 0
		for _, line := range slices.Sorted(maps.Keys(lines[file])) {
			count := 0
			if lines[file][line] {
				count = 1
				hit++
			}
			fmt.Fprintf(&b, "DA:%d,%d\n", line, count)
		}
		fmt.Fprintf(&b, "LF:%d\nLH:%d\nend_of_record\n", len(lines[file]), hit)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("cannot write coverage: %w", err)
	}

	if len(statements) == 0 {
		fmt.Fprintln(out, "coverage: [no statements]")
	} else {
		fmt.Fprintf(out, "coverage: %.1f%% of statements\n", 100*float64(covered)/float64(len(statements)))
	}
	return nil
}
