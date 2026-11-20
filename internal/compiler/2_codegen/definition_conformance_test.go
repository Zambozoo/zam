package codegen

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The lines every dump that carries methods, members or a supertype set ends up
// with, written once so a fixture change moves one place.
var (
	wantMethods = []string{
		"method[0].typeListID=11",
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
			},
			want: concat([]string{
				"methodCount=2",
				"memberCount=2",
				"instanceByteSize=24",
				"instanceTailByteSize=8",
			}, wantMethods, wantMembers),
		},
		{
			kind: "enum",
			definition: &typeDefinitionEnum{
				instanceMethods: testMethods(), instanceMembers: testMembers(),
				instanceByteSize: 16,
				valueCount:       3, valueSlot: 12, valueType: 7 << 16,
			},
			want: concat([]string{
				"methodCount=2",
				"memberCount=2",
				"instanceByteSize=16",
				"valueCount=3",
				"valueSlot=12",
				"valueType=458752",
			}, wantMethods, wantMembers),
		},
		{
			kind: "enum",
			definition: &typeDefinitionEnum{
				instanceMethods: testMethods(), instanceMembers: testMembers(),
				instanceByteSize: 16,
			},
			want: concat([]string{
				"methodCount=2",
				"memberCount=2",
				"instanceByteSize=16",
				"valueCount=0",
				"valueSlot=0",
				"valueType=0",
			}, wantMethods, wantMembers),
		},
		{
			kind: "abstract",
			definition: &typeDefinitionAbstract{
				instanceMethods: testMethods(), instanceMembers: testMembers(),
				instanceByteSize: 24, instanceTailByteSize: 0,
				supertypeSet: testSupertypeSet(),
			},
			want: concat([]string{
				"methodCount=2",
				"memberCount=2",
				"instanceByteSize=24",
				"instanceTailByteSize=0",
			}, wantMethods, wantMembers, wantSupertypes),
		},
		{
			kind: "class",
			definition: &typeDefinitionClass{
				typeDefinitionAbstract: typeDefinitionAbstract{
					instanceMethods: testMethods(), instanceMembers: testMembers(),
					instanceByteSize: 24, instanceTailByteSize: 8,
					supertypeSet: testSupertypeSet(),
				},
				dispatchEntries: testDispatch(),
			},
			want: concat([]string{
				"methodCount=2",
				"memberCount=2",
				"instanceByteSize=24",
				"instanceTailByteSize=8",
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
				concreteTypesSizeMap: []genericMapEntry{testGenericEntry()},
				methodCount:          3,
			},
			want: concat([]string{
				"methodCount=3",
				"concreteCount=1",
				"concrete=0",
			}, wantGenericEntry),
		},
		{
			kind: "generic-class",
			definition: &typeDefinitionGenericClass{
				concreteTypesSizeMap: []genericClassMapEntry{testGenericClassEntry()},
				methodCount:          3,
			},
			want: concat([]string{
				"methodCount=3",
				"concreteCount=1",
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
			kind: "tuple",
			definition: &tupleDefinition{
				instanceByteSize: 24,
				types: typeListInstance{typeIDs: []typeInstance{
					{typeInstanceShort: typeInstanceShort{typeID: 4}},
					{typeInstanceShort: typeInstanceShort{typeID: 6, genericID: 1}, arrayLength: 2},
				}},
				offsets: []uint64{0, 8},
			},
			want: []string{
				"instanceByteSize=24",
				"types[0].count=2",
				"types[0].type[0].shortID=262144",
				"types[0].type[0].arrayLength=0",
				"types[0].type[0].tailLength=0",
				"types[0].type[1].shortID=393217",
				"types[0].type[1].arrayLength=2",
				"types[0].type[1].tailLength=0",
				"offset=0",
				"offset=8",
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
