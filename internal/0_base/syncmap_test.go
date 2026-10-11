package base

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The ordered map a SyncMap is drained into decides type id assignment and the
// order static initializers run in, so what it must not do is answer differently
// for maps that were filled differently but hold the same entries.
func TestSyncMapToMapAnswersInComparatorOrder(t *testing.T) {
	paths := []Path{
		{Outer: "/b/one.zam"},
		{Outer: "/a/two.zam"},
		{Outer: "/a/one.zam"},
		{Outer: "/a/lib.zip", Inner: "b.zam"},
		{Outer: "/a/lib.zip", Inner: "a.zam"},
	}

	orders := func() []string {
		var sm SyncMap[Path, string]
		for _, p := range paths {
			sm.Store(p, p.String())
		}

		var got []string
		for _, entry := range sm.ToMap(ComparePath).All() {
			got = append(got, entry.Value)
		}
		return got
	}

	expected := []string{
		"/a/lib.zip#a.zam",
		"/a/lib.zip#b.zam",
		"/a/one.zam",
		"/a/two.zam",
		"/b/one.zam",
	}

	// Repeated because a sync.Map fills its read map lazily: a single drain can
	// come out sorted by luck, where a run of them coming out sorted cannot.
	for range 16 {
		assert.Equal(t, expected, orders())
		slices.Reverse(paths)
	}
}
