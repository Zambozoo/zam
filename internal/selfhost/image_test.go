package selfhost_test

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	codegen "github.com/zambozoo/zam/internal/compiler/2_codegen"
)

const (
	// imageWord is how wide every word of the image is.
	imageWord = 8
	// headerWords is how many words open the image, ahead of what its offsets
	// are measured from.
	headerWords = 21
	// configurationWords is how many words of runtime configuration follow
	// the header.
	configurationWords = 10
)

// definitionBuckets names the type definition buckets in the order the header
// gives their start indices and the image lays them out.
var definitionBuckets = []string{
	"thread", "function", "pointer", "tuple", "struct", "genericStruct", "enum",
	"class", "genericClass", "abstract", "genericAbstract", "interface", "genericInterface",
}

// TestImageMatchesGo generates the runtime image of every program in the
// codegen corpus, and of the compiler written in zam itself, with that compiler
// and with the Go one, and expects the same bytes, naming the first section that
// differs when they are not.
func TestImageMatchesGo(t *testing.T) {
	zam, err := compiler()
	require.NoError(t, err)

	for _, entry := range imageEntries(t) {
		name, err := filepath.Rel(root, filepath.Dir(entry))
		require.NoError(t, err)
		t.Run(filepath.ToSlash(name), func(t *testing.T) {
			t.Parallel()
			want, got := goImage(t, entry), zamImage(t, zam, entry)
			if bytes.Equal(want, got) {
				return
			}
			wantSections, gotSections := imageSections(t, want), imageSections(t, got)
			for _, section := range slices.Sorted(maps.Keys(wantSections)) {
				if !bytes.Equal(wantSections[section], gotSections[section]) {
					assert.Equal(t, wantSections[section], gotSections[section], section)
					return
				}
			}
			assert.Equal(t, want, got)
		})
	}
}

// TestCodegenErrorsMatchGo generates the runtime image of every program in the
// codegen error corpus with the compiler written in zam and with the Go one, and
// expects both to refuse it with the same error.
func TestCodegenErrorsMatchGo(t *testing.T) {
	zam, err := compiler()
	require.NoError(t, err)

	entries, err := filepath.Glob(filepath.Join(root, "internal", "selfhost", "testdata", "codegenerrors", "*", "Main.zam"))
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	for _, entry := range entries {
		t.Run(filepath.Base(filepath.Dir(entry)), func(t *testing.T) {
			t.Parallel()
			cmd, ast := parsed(t, entry)
			_, goErr := codegen.GenerateCode(cmd, ast)
			require.Error(t, goErr)

			var stderr bytes.Buffer
			run := exec.Command(zam, arguments(entry, false)[1:]...)
			run.Env = append(os.Environ(), "ZAM_STD="+filepath.Join(root, "src", "std"), "ZAM_DUMP=image")
			run.Stderr = &stderr
			require.Error(t, run.Run())
			assert.Equal(t, "[codegen] "+goErr.Error(), strings.TrimSpace(stderr.String()))
		})
	}
}

// TestImageSections splits the images Go generates and expects the sections to
// cover each image exactly, so that what the comparison reads is the whole image.
func TestImageSections(t *testing.T) {
	for _, entry := range imageEntries(t) {
		name, err := filepath.Rel(root, filepath.Dir(entry))
		require.NoError(t, err)
		t.Run(filepath.ToSlash(name), func(t *testing.T) {
			t.Parallel()
			image := goImage(t, entry)
			total := 0
			for name, section := range imageSections(t, image) {
				if name != "header.counts" {
					total += len(section)
				}
			}
			assert.Equal(t, len(image), total)
		})
	}
}

// imageEntries is every program in the codegen corpus and the compiler written
// in zam.
func imageEntries(t *testing.T) []string {
	t.Helper()
	return append(codegenEntries(t), filepath.Join(root, "src", "Main.zam"))
}

// goImage answers the runtime image Go generates for the program entered at
// entry.
func goImage(t *testing.T, entry string) []byte {
	t.Helper()
	cmd, ast := parsed(t, entry)
	result, err := codegen.GenerateCode(cmd, ast)
	require.NoError(t, err)
	return result.Image
}

// zamImage answers the runtime image the compiler at zam writes for the program
// entered at entry.
func zamImage(t *testing.T, zam, entry string) []byte {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(zam, arguments(entry, false)[1:]...)
	cmd.Env = append(os.Environ(), "ZAM_STD="+filepath.Join(root, "src", "std"), "ZAM_DUMP=image")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		require.Failf(t, "zam failed", "%s", stderr.String())
	}
	require.NoError(t, err)
	image, err := hex.DecodeString(strings.TrimSpace(stdout.String()))
	require.NoError(t, err)
	return image
}

// imageSections splits image into its named sections by what its header says
// of where each one lies. The header's offsets are measured from its own end,
// and the definition map gives where each definition begins. Every cut is
// clamped to the image, so an image whose header and body disagree compares
// unequal rather than failing to split.
func imageSections(t *testing.T, image []byte) map[string][]byte {
	t.Helper()
	require.GreaterOrEqual(t, len(image), headerWords*imageWord)
	word := func(p []byte, i int) int {
		if (i+1)*imageWord > len(p) {
			return 0
		}
		return int(binary.LittleEndian.Uint64(p[i*imageWord:]))
	}
	body := image[headerWords*imageWord:]
	cut := func(from, to int) []byte {
		from, to = min(max(from, 0), len(body)), min(max(to, 0), len(body))
		return body[from:max(from, to)]
	}
	count := word(image, 2)
	staticInstanceMaps, staticImage, natives, staticRoots := word(image, 16), word(image, 18), word(image, 19), word(image, 20)

	mapStart := configurationWords * imageWord
	mapEnd := mapStart + count*imageWord
	definitionMap := cut(mapStart, mapEnd)
	sections := map[string][]byte{
		"header":             image[:headerWords*imageWord],
		"header.counts":      image[:16*imageWord],
		"configuration":      cut(0, mapStart),
		"definitionMap":      definitionMap,
		"staticInstanceMaps": cut(staticInstanceMaps, staticImage),
		"staticImage":        cut(staticImage, natives),
		"natives":            cut(natives, staticRoots),
		"staticRoots":        cut(staticRoots, len(body)),
	}

	// definitionAt is where the definition of the type id i begins, the end of
	// the last one being where the static instance maps begin.
	definitionAt := func(i int) int {
		if i >= count {
			return staticInstanceMaps
		}
		return word(definitionMap, i)
	}
	for k, bucket := range definitionBuckets {
		start := word(image, 3+k)
		end := count
		if k+1 < len(definitionBuckets) {
			end = word(image, 3+k+1)
		}
		sections["definitions."+bucket] = cut(definitionAt(start), definitionAt(end))
	}
	return sections
}
