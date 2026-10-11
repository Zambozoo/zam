package base

import (
	"slices"
	"sync"
)

// SyncMap is a typed wrapper around [sync.Map] for concurrent load/store by key.
// The zero value is ready to use.
type SyncMap[K comparable, V any] sync.Map

// Load returns the value stored for key, or the zero value of V and false if absent.
func (sm *SyncMap[K, V]) Load(key K) (v V, ok bool) {
	x, ok := (*sync.Map)(sm).Load(key)
	if !ok {
		return v, false
	}
	return x.(V), true
}

// Store sets key to value.
func (sm *SyncMap[K, V]) Store(key K, value V) {
	(*sync.Map)(sm).Store(key, value)
}

// LoadOrStore returns the existing value if present; otherwise it stores value and returns it,
// with loaded false. If loaded is true, the returned value was already stored (another caller won).
func (sm *SyncMap[K, V]) LoadOrStore(key K, value V) (actual V, loaded bool) {
	x, loaded := (*sync.Map)(sm).LoadOrStore(key, value)
	return x.(V), loaded
}

// Delete removes key from the map.
func (sm *SyncMap[K, V]) Delete(key K) {
	(*sync.Map)(sm).Delete(key)
}

// LoadAndDelete deletes the value for key and returns the previous value if any.
func (sm *SyncMap[K, V]) LoadAndDelete(key K) (v V, loaded bool) {
	x, loaded := (*sync.Map)(sm).LoadAndDelete(key)
	if !loaded {
		return v, false
	}
	return x.(V), true
}

// Range calls f for each key/value in the map until f returns false.
func (sm *SyncMap[K, V]) Range(f func(key K, value V) bool) {
	(*sync.Map)(sm).Range(func(key, value any) bool {
		return f(key.(K), value.(V))
	})
}

// ToMap copies entries into a new ordered map in the order cmp puts them,
// suitable for use after concurrent writes are done.
//
// The order is asked of the caller rather than inherited from Range because
// Range answers in whatever order the underlying sync.Map happens to hold, which
// is a different order run to run. Everything downstream reads the map in
// insertion order, so that ordering reaches type id assignment and the order
// static initializers run in; leaving it to chance would make a program's
// meaning depend on how the parsing threads interleaved.
func (sm *SyncMap[K, V]) ToMap(cmp func(a, b K) int) *StaticOrderedMap[K, V] {
	var keys []K
	values := map[K]V{}
	sm.Range(func(k K, v V) bool {
		keys = append(keys, k)
		values[k] = v
		return true
	})
	slices.SortFunc(keys, cmp)

	out := NewStaticOrderedMap[K, V]()
	for _, k := range keys {
		out.Set(k, values[k])
	}
	return out
}
