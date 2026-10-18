package codegen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// harnessCache memoizes each compiled harness binary by its config, so a
// package-wide run compiles each one once rather than once per test. The
// subprocess cc is ~800ms a call and the conformance tests make 40-plus
// calls, so caching is where most of the suite's wall time lives.
//
// Entries live for the test binary's whole run — the binary itself sits in a
// package-level temp dir that the OS cleans up — so a later test that asks
// for the same harness gets back the path the first test already compiled to.
var (
	harnessCacheMu sync.Mutex
	harnessCache   = map[string]*harnessEntry{}
	harnessDir     string
	harnessDirOnce sync.Once
)

// harnessEntry holds one harness's build, guarded by a sync.Once so parallel
// tests that ask for the same binary wait for one compile rather than racing
// the compiler over the same output path.
type harnessEntry struct {
	once sync.Once
	path string
	err  error
}

// buildHarness compiles one of the readers in testdata against the runtime's
// own headers and sources, so what it reads with is what the runtime reads with
// rather than a copy that could be updated separately.
//
// The runtime sources a harness needs beyond the four every one of them reads
// with are named by the caller. main.c is never among them: a harness brings its
// own entry point, which is the whole of what makes it a harness rather than the
// runtime.
func buildHarness(t *testing.T, source, name string, extra ...string) string {
	t.Helper()

	runtime := filepath.Join("..", "5_build", "runtime")
	sources := []string{
		filepath.Join("testdata", source),
		filepath.Join(runtime, "src", "definition.c"),
		filepath.Join(runtime, "src", "instance.c"),
		filepath.Join(runtime, "src", "image.c"),
		filepath.Join(runtime, "src", "common.c"),
	}
	for _, extraSrc := range extra {
		sources = append(sources, filepath.Join(runtime, "src", extraSrc))
	}

	// Opening the sources here is what puts them in each test's own cache key:
	// the compiler runs in a subprocess, so nothing it opens is visible to go
	// test, and a run whose only change was on the C side would be answered
	// from a previous one. Every test does this read even when the compile
	// itself is served from the package cache, so test caching invalidates for
	// every test alike when a .c or .h is touched.
	for _, s := range append(sources, headersIn(t, filepath.Join(runtime, "src"))...) {
		_, err := os.ReadFile(s)
		require.NoError(t, err, "the harness cannot be held to a source it cannot read")
	}

	// Cache the compile by the tuple that decides it — the entry source, the
	// output name, and the extra runtime .c files — so two tests asking for
	// the same harness share one build.
	key := strings.Join(append([]string{source, name}, extra...), "|")

	harnessCacheMu.Lock()
	entry, ok := harnessCache[key]
	if !ok {
		entry = &harnessEntry{}
		harnessCache[key] = entry
	}
	harnessCacheMu.Unlock()

	entry.once.Do(func() {
		entry.path, entry.err = compileHarness(name, sources, runtime)
	})
	require.NoError(t, entry.err, "building the %s harness", name)
	return entry.path
}

// compileHarness runs cc with the same flags the runtime is built with — plus
// the undefined behaviour sanitizer, which the runtime is not. These harnesses
// are the only place the C side is ever run, so they are the only place UB can
// be caught at all, and the kind this catches is the kind no assertion would:
// an image is read in place, so a field reached at an address the type may not
// be read from is a bug that gives the right answer on x86-64 and arm64 and
// would fault elsewhere. Recovery is off so that the first one fails the test
// it is in rather than printing into output nothing reads.
func compileHarness(name string, sources []string, runtime string) (string, error) {
	compiler, err := exec.LookPath("cc")
	if err != nil {
		return "", fmt.Errorf("the layout cannot be held to the runtime without a C compiler: %w", err)
	}

	harnessDirOnce.Do(func() {
		harnessDir, _ = os.MkdirTemp("", "zam-harness-")
	})
	if harnessDir == "" {
		return "", fmt.Errorf("could not create a scratch directory for harness binaries")
	}

	harness := filepath.Join(harnessDir, name)
	build := exec.Command(compiler, append([]string{
		"-std=gnu11", "-Wall", "-Wextra", "-Wpedantic", "-Werror", "-O1",
		"-fsanitize=undefined", "-fno-sanitize-recover=all",
		"-I" + filepath.Join(runtime, "src"),
		"-I" + filepath.Join(runtime, "include"),
		"-o", harness,
	}, sources...)...)
	out, err := build.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, out)
	}
	return harness, nil
}

// headersIn is every header beside the sources, which the compiler reads but
// never names on the command line.
func headersIn(t *testing.T, directory string) []string {
	t.Helper()

	headers, err := filepath.Glob(filepath.Join(directory, "*.h"))
	require.NoError(t, err)
	return headers
}

// The lines every dump that carries methods, members or a supertype set ends up
// with, written once so a fixture change moves one place.
var (
	wantMethods = []string{
		"method[0].instruction=10",
		"method[0].typeListID=11",
		"method[1].instruction=20",
		"method[1].typeListID=21",
	}
	wantMembers = []string{
		"member[0].offset=0",
		"member[0].shortID=196608",
		"member[0].arrayLength=0",
		"member[0].tailLength=0",
		"member[1].offset=8",
		"member[1].shortID=262145",
		"member[1].arrayLength=3",
		"member[1].tailLength=5",
	}
	// The set holds {9,0} and {7,2} and not {11,4}. The miss is the line worth
	// having: a perfect hash resolves a key it was never built over to some
	// other key's slot, so a set that reported it as present would mean the
	// membership check had stopped comparing the answer to the question.
	wantSupertypes = []string{
		"supertype[589824].contains=1",
		"supertype[458754].contains=1",
		"supertype[720900].contains=0",
	}
	wantDispatch = []string{
		"dispatch[589824,0]=1",
		"dispatch[458754,1]=0",
	}
	wantGenericEntry = concat(
		[]string{
			"instanceByteSize=32",
			"instanceTailByteSize=8",
			"genericTypeCount=1",
			"memberCount=2",
			"methodTypeCount=3",
			"genericType[0].shortID=65536",
			"genericType[0].arrayLength=0",
			"genericType[0].tailLength=0",
		},
		wantMembers,
		[]string{
			"methodType[0].typeListID=100",
			"methodType[1].typeListID=101",
			"methodType[2].typeListID=102",
		},
		wantSupertypes,
	)
)

func concat(groups ...[]string) []string {
	var all []string
	for _, group := range groups {
		all = append(all, group...)
	}
	return all
}

// TestDefinitionLayoutCConformance writes each kind of definition exactly as it
// goes into an image and has the runtime's own accessors read it back.
//
// The runtime does not parse a definition, it casts the image's bytes to a
// struct, which means there is no point on the read path at which a layout
// disagreement could be noticed. A count the compiler writes where the runtime
// expects a scalar does not fail: every field after it reads its neighbour and
// comes back looking like a number. Reading the fields back out to the side
// that wrote them is the only moment the two can be compared.
//
// The width each dump ends with is the same invariant the whole-image check
// enforces, asked one kind at a time: definitions are laid out back to back, so
// a kind the runtime measures differently than the compiler wrote it is a kind
// that leaves every definition after it being read from the wrong offset.
func TestDefinitionLayoutCConformance(t *testing.T) {
	t.Parallel()
	harness := buildHarness(t, "definition_harness.c", "definitions")

	entry := testGenericEntry()
	classEntry := testGenericClassEntry()

	cases := []struct {
		kind       string
		definition typeDefinition
		want       []string
	}{
		{
			kind: "struct",
			definition: &typeDefinitionStruct{
				instanceMethods: testMethods(), instanceMembers: testMembers(),
				instanceByteSize: 24, instanceTailByteSize: 8,
				initializerInstruction: 40,
			},
			want: concat([]string{
				"methodCount=2",
				"memberCount=2",
				"instanceByteSize=24",
				"instanceTailByteSize=8",
				"initializerInstruction=40",
			}, wantMethods, wantMembers),
		},
		{
			kind: "enum",
			definition: &typeDefinitionEnum{
				instanceMethods: testMethods(), instanceMembers: testMembers(),
				instanceByteSize: 16, initializerInstruction: 41,
				valueCount: 3, valueSlot: 12,
			},
			want: concat([]string{
				"methodCount=2",
				"memberCount=2",
				"instanceByteSize=16",
				"initializerInstruction=41",
				"valueCount=3",
				"valueSlot=12",
			}, wantMethods, wantMembers),
		},
		{
			kind: "enum",
			definition: &typeDefinitionEnum{
				instanceMethods: testMethods(), instanceMembers: testMembers(),
				instanceByteSize: 16, initializerInstruction: 41,
			},
			want: concat([]string{
				"methodCount=2",
				"memberCount=2",
				"instanceByteSize=16",
				"initializerInstruction=41",
				"valueCount=0",
				"valueSlot=0",
			}, wantMethods, wantMembers),
		},
		{
			kind: "abstract",
			definition: &typeDefinitionAbstract{
				instanceMethods: testMethods(), instanceMembers: testMembers(),
				instanceByteSize: 24, instanceTailByteSize: 0,
				initializerInstruction: 42, supertypeSet: testSupertypeSet(),
			},
			want: concat([]string{
				"methodCount=2",
				"memberCount=2",
				"instanceByteSize=24",
				"instanceTailByteSize=0",
				"initializerInstruction=42",
			}, wantMethods, wantMembers, wantSupertypes),
		},
		{
			kind: "class",
			definition: &typeDefinitionClass{
				typeDefinitionAbstract: typeDefinitionAbstract{
					instanceMethods: testMethods(), instanceMembers: testMembers(),
					instanceByteSize: 24, instanceTailByteSize: 8,
					initializerInstruction: 42, supertypeSet: testSupertypeSet(),
				},
				dispatchEntries: testDispatch(),
			},
			want: concat([]string{
				"methodCount=2",
				"memberCount=2",
				"instanceByteSize=24",
				"instanceTailByteSize=8",
				"initializerInstruction=42",
			}, wantMethods, wantMembers, wantSupertypes, wantDispatch),
		},
		{
			kind:       "interface",
			definition: &interfaceDefinition{supertypeSet: testSupertypeSet()},
			want:       wantSupertypes,
		},
		{
			kind: "generic-abstract",
			definition: &typeDefinitionGenericAbstract{
				concreteTypesSizeMap:       []genericMapEntry{testGenericEntry()},
				instanceMethodInstructions: []uint64{50, 51, 52},
				initializerInstruction:     43,
			},
			want: concat([]string{
				"methodCount=3",
				"concreteCount=1",
				"initializerInstruction=43",
				"methodInstruction[0].at=50",
				"methodInstruction[1].at=51",
				"methodInstruction[2].at=52",
				"concrete=0",
			}, wantGenericEntry),
		},
		{
			kind: "generic-class",
			definition: &typeDefinitionGenericClass{
				concreteTypesSizeMap:       []genericClassMapEntry{testGenericClassEntry()},
				instanceMethodInstructions: []uint64{50, 51, 52},
				initializerInstruction:     43,
			},
			want: concat([]string{
				"methodCount=3",
				"concreteCount=1",
				"initializerInstruction=43",
				"methodInstruction[0].at=50",
				"methodInstruction[1].at=51",
				"methodInstruction[2].at=52",
				"concrete=0",
			}, wantGenericEntry, wantDispatch),
		},
		{
			kind: "generic-interface",
			definition: &typeDefinitionGenericInterface{
				concreteSupertypeSets: []map[typeInstanceShort]struct{}{testSupertypeSet()},
			},
			want: concat([]string{
				"concreteCount=1",
				"concrete=0",
			}, wantSupertypes),
		},
		{
			kind:       "generic-entry",
			definition: &entry,
			want:       wantGenericEntry,
		},
		{
			kind:       "generic-class-entry",
			definition: &classEntry,
			want:       concat(wantGenericEntry, wantDispatch),
		},
		{
			kind: "type-list",
			definition: typeListInstance{typeIDs: []typeInstance{{
				typeInstanceShort: typeInstanceShort{typeID: 1},
				arrayLength:       4, tailedLength: 2,
			}}},
			want: []string{
				"list[0].count=1",
				"list[0].type[0].shortID=65536",
				"list[0].type[0].arrayLength=4",
				"list[0].type[0].tailLength=2",
			},
		},
		{
			// The three fields are given distinct values on purpose. Every type
			// reference on the wire is these same three words, so two of them
			// being swapped is a thing nothing else here would notice.
			kind: "pointer",
			definition: &pointerDefinition{pointee: typeInstance{
				typeInstanceShort: typeInstanceShort{typeID: 3, genericID: 1},
				arrayLength:       5, tailedLength: 7,
			}},
			want: []string{
				"pointee.shortID=196609",
				"pointee.arrayLength=5",
				"pointee.tailLength=7",
			},
		},
		{
			kind: "thread-type",
			definition: typeInstance{
				typeInstanceShort: typeInstanceShort{typeID: 4, genericID: 2},
				arrayLength:       6, tailedLength: 9,
			},
			want: []string{
				"result.shortID=262146",
				"result.arrayLength=6",
				"result.tailLength=9",
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.kind, func(t *testing.T) {
			p := make([]byte, testCase.definition.ByteCount())
			n, err := testCase.definition.Read(p)
			require.NoError(t, err)
			require.Equal(t, len(p), n)

			run := exec.Command(harness, testCase.kind)
			run.Stdin = strings.NewReader(string(p))
			out, err := run.Output()
			require.NoError(t, err)

			// The width comes last, and is the compiler's own answer rather
			// than a number written down here: the two sides have to measure
			// the definition the same or everything behind it moves.
			want := append(concat(testCase.want),
				fmt.Sprintf("wordCount=%d", testCase.definition.ByteCount()/wordByteSize))

			assert.Equal(t, want, strings.Split(strings.TrimSpace(string(out)), "\n"))
		})
	}
}
