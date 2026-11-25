package lsp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/zambozoo/zam/internal/base"
)

// testsOf asks for the tests of the file at path, without opening it.
func testsOf(t *testing.T, path base.Path) []testItem {
	t.Helper()
	return newClient(t).call(methodTests, testsParams{URI: fileURI(path.String())}).([]testItem)
}

func TestTestsOfAFileOnDisk(t *testing.T) {
	t.Parallel()
	source := "public namespace XTest {\n" +
		"    public fun void(uint, str...) First = (argC, argV) {\n" +
		"    }\n" +
		"\n" +
		"    public fun void(uint, str...) Second = (argC, argV) {\n" +
		"    }\n" +
		"}\n"
	paths := project(t, map[string]string{"X.test.zam": source})

	assert.Equal(t, []testItem{
		{Name: "", Range: rangeOf(0, 17, 0, 22)},
		{Name: "XTest.First", Range: rangeOf(1, 34, 1, 39)},
		{Name: "XTest.Second", Range: rangeOf(4, 34, 4, 40)},
	}, testsOf(t, paths["X.test.zam"]))
}

func TestTestsOnlyOfTestFiles(t *testing.T) {
	t.Parallel()
	main := "namespace Main {\n    fun void(uint, str...) main = (argC, argV) {\n    }\n}\n"
	wrong := "public namespace Wrong {\n    public fun void(uint, str...) First = (argC, argV) {\n    }\n}\n"
	paths := project(t, map[string]string{"Main.zam": main, "Y.test.zam": wrong})

	assert.Empty(t, testsOf(t, paths["Main.zam"]), "a file that is not a test file")
	assert.Empty(t, testsOf(t, paths["Y.test.zam"]), "a test file the runner would refuse")
}
