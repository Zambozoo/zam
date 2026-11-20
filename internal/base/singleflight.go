package base

import "golang.org/x/sync/singleflight"

type (
	SingleflightGroup[T any]  singleflight.Group
	SingleflightResult[T any] struct {
		Val    T
		Err    error
		Shared bool
	}
)

func (sg *SingleflightGroup[T]) Do(key string, fn func() (T, error)) (v T, err error, shared bool) {
	val, err, shared := (*singleflight.Group)(sg).Do(key, func() (any, error) {
		return fn()
	})
	v, _ = val.(T)
	return v, err, shared
}

func (sg *SingleflightGroup[T]) DoChan(key string, fn func() (T, error)) <-chan SingleflightResult[T] {
	in := (*singleflight.Group)(sg).DoChan(key, func() (any, error) { return fn() })
	out := make(chan SingleflightResult[T], 1)
	go func() {
		r := <-in
		v, _ := r.Val.(T)
		out <- SingleflightResult[T]{
			Val:    v,
			Err:    r.Err,
			Shared: r.Shared,
		}
	}()
	return out
}

func (sg *SingleflightGroup[T]) Forget(key string) {
	(*singleflight.Group)(sg).Forget(key)
}
