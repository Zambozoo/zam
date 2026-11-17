// Command nativereport summarizes a `go test -json` run of the end-to-end
// tests against the native backend: how many pass, and why each failing one
// failed.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
)

// event is one line of `go test -json` output.
type event struct {
	Action string
	Test   string
	Output string
}

func main() {
	outputs := map[string][]string{}
	results := map[string]string{}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1<<20), 1<<24)
	for scanner.Scan() {
		var e event
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil || e.Test == "" || strings.Contains(e.Test, "/") {
			continue
		}
		switch e.Action {
		case "run":
			if _, ok := results[e.Test]; !ok {
				results[e.Test] = "run"
			}
		case "output":
			outputs[e.Test] = append(outputs[e.Test], e.Output)
		case "pass", "fail", "skip":
			results[e.Test] = e.Action
		}
	}

	// A test that began and never ended was still running when the run was
	// cut off, as a hang is.
	var passed, failed []string
	for test, result := range results {
		switch result {
		case "pass":
			passed = append(passed, test)
		case "fail", "run":
			failed = append(failed, test)
		}
	}
	slices.Sort(failed)
	fmt.Printf("native: %d of %d end-to-end tests pass\n", len(passed), len(passed)+len(failed))
	for _, test := range failed {
		if results[test] == "run" {
			fmt.Printf("\nFAIL %s (never finished)\n", test)
			continue
		}
		fmt.Printf("\nFAIL %s\n", test)
		for _, line := range reasons(outputs[test]) {
			fmt.Printf("    %s\n", line)
		}
	}
}

// reasons picks the lines of a failing test's output that say why it failed:
// each assertion's error and what follows it, up to the assertion's next
// field.
func reasons(output []string) []string {
	var lines []string
	inError := false
	for _, out := range output {
		line := strings.TrimSpace(out)
		switch {
		case strings.HasPrefix(line, "Test:") || strings.HasPrefix(line, "Error Trace:"):
			inError = false
		case strings.Contains(line, "Error:"):
			inError = true
			lines = append(lines, line)
		case inError && line != "":
			lines = append(lines, line)
		case strings.Contains(line, "not supported by native") ||
			strings.Contains(line, "expected") || strings.Contains(line, "actual"):
			lines = append(lines, line)
		}
		if len(lines) == 6 {
			break
		}
	}
	return lines
}
