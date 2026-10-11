package syntax

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	mock "github.com/stretchr/testify/mock"
)

func assertErrorIs(want error) assert.ErrorAssertionFunc {
	return func(t assert.TestingT, err error, a ...any) bool {
		return assert.ErrorIs(t, err, want, a...)
	}
}

func TestRuneReader_Peek(t *testing.T) {
	t.Parallel()

	type want struct {
		r       rune
		errFunc assert.ErrorAssertionFunc
	}
	tests := []struct {
		name   string
		n      uint
		reader io.ReadCloser
		wants  []want
	}{
		{
			name:   "empty",
			reader: io.NopCloser(bytes.NewReader([]byte{})),
			wants: []want{
				{
					r:       0,
					errFunc: assertErrorIs(io.EOF),
				},
			},
		},
		{
			name:   "peek 1 past EOF",
			reader: io.NopCloser(bytes.NewReader([]byte{'a'})),
			n:      1,
			wants: []want{
				{
					r:       0,
					errFunc: assertErrorIs(io.EOF),
				},
			},
		},
		{
			name:   "peek 1",
			reader: io.NopCloser(bytes.NewReader([]byte{'a', 'b', 'c'})),
			n:      1,
			wants: []want{
				{
					r:       'b',
					errFunc: assert.NoError,
				},
				{
					r:       'c',
					errFunc: assert.NoError,
				},
				{
					r:       0,
					errFunc: assertErrorIs(io.EOF),
				},
			},
		},
		{
			name:   "non-empty",
			reader: io.NopCloser(bytes.NewReader([]byte{'a', 'b', 'c'})),
			wants: []want{
				{
					r:       'a',
					errFunc: assert.NoError,
				},
				{
					r:       'b',
					errFunc: assert.NoError,
				},
				{
					r:       'c',
					errFunc: assert.NoError,
				},
				{
					r:       0,
					errFunc: assertErrorIs(io.EOF),
				},
			},
		},
		{
			name: "Read returns zero bytes without EOF",
			reader: func() io.ReadCloser {
				m := newMock_ReadCloser(t)
				m.On("Read", mock.Anything).Return(0, nil)
				m.On("Close").Return(nil)
				return m
			}(),
			wants: []want{
				{
					r:       0,
					errFunc: assertErrorIs(errReadZeroWithoutEOF),
				},
			},
		},
		{
			name: "non-EOF error",
			reader: func() io.ReadCloser {
				m := newMock_ReadCloser(t)
				m.On("Read", mock.Anything).Return(0, assert.AnError)
				m.On("Close").Return(nil)
				return m
			}(),
			wants: []want{
				{
					r:       0,
					errFunc: assertErrorIs(assert.AnError),
				},
			},
		},
		{
			name: "non-EOF error on ReadFull with multiple bytes",
			reader: func() io.ReadCloser {
				m := newMock_ReadCloser(t)
				m.On("Read", mock.Anything).Once().Run(func(args mock.Arguments) {
					p := args.Get(0).([]byte)
					p[0] = 0xE2
				}).Return(1, nil)
				m.On("Read", mock.Anything).Once().Return(0, assert.AnError)
				m.On("Close").Return(nil)
				return m
			}(),
			wants: []want{
				{
					r:       0,
					errFunc: assertErrorIs(assert.AnError),
				},
			},
		},
		{
			name:   "incomplete UTF-8",
			reader: io.NopCloser(bytes.NewReader([]byte{0x80})),
			wants: []want{
				{
					r:       0,
					errFunc: assertErrorIs(errInvalidUTF8Input),
				},
			},
		},
		{
			name:   "invalid UTF-8: utf8Lead2ValidLo",
			reader: io.NopCloser(bytes.NewReader([]byte{0x80})),
			wants: []want{
				{
					r:       0,
					errFunc: assertErrorIs(errInvalidUTF8Input),
				},
			},
		},
		{
			name:   "invalid UTF-8: utf8Lead3Lo",
			reader: io.NopCloser(bytes.NewReader([]byte{0xE0 - 1})),
			wants: []want{
				{
					r:       0,
					errFunc: assertErrorIs(errIncompleteUTF8EOF),
				},
			},
		},
		{
			name:   "invalid UTF-8: utf8Lead4Lo",
			reader: io.NopCloser(bytes.NewReader([]byte{0xF0 - 1})),
			wants: []want{
				{
					r:       0,
					errFunc: assertErrorIs(errIncompleteUTF8EOF),
				},
			},
		},
		{
			name:   "invalid UTF-8: utf8Lead4ValidHi",
			reader: io.NopCloser(bytes.NewReader([]byte{0xF4})),
			wants: []want{
				{
					r:       0,
					errFunc: assertErrorIs(errIncompleteUTF8EOF),
				},
			},
		},
		{
			name:   "invalid UTF-8: utf8Lead4ValidHi+1",
			reader: io.NopCloser(bytes.NewReader([]byte{0xF4 + 1})),
			wants: []want{
				{
					r:       0,
					errFunc: assertErrorIs(errInvalidUTF8Input),
				},
			},
		},
		{
			name:   "invalid UTF-8: utf8Lead4ValidHi, missing 3rd byte",
			reader: io.NopCloser(bytes.NewReader([]byte{0xF4, 0x80})),
			wants: []want{
				{
					r:       0,
					errFunc: assertErrorIs(errIncompleteUTF8EOF),
				},
			},
		},
		{
			name:   "invalid UTF-8: utf8Lead4ValidHi, bad 2nd byte",
			reader: io.NopCloser(bytes.NewReader([]byte{0xF4, 0xFF, 0x80, 0x80})),
			wants: []want{
				{
					r:       0,
					errFunc: assertErrorIs(errInvalidUTF8Input),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rr := newRuneReader(tt.reader)
			defer rr.Close()

			for _, want := range tt.wants {
				got, err := rr.Peek(tt.n)
				want.errFunc(t, err)
				assert.Equal(t, want.r, got)
				if err == nil {
					rr.Advance(1)
				}
			}

			got, err := rr.Peek(tt.n)
			assert.Equal(t, rune(0), got)
			assert.ErrorIs(t, err, io.EOF)
		})
	}
}

func TestRuneReader_Advance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      []byte
		n          uint
		wantOffset uint
	}{
		{
			name:       "advance one decodes on demand",
			input:      []byte{'a', 'b', 'c'},
			n:          1,
			wantOffset: 1,
		},
		{
			name:       "advance multiple decodes each rune",
			input:      []byte{'a', 'b', 'c'},
			n:          3,
			wantOffset: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rr := newRuneReader(io.NopCloser(bytes.NewReader(tt.input)))
			defer rr.Close()

			rr.Advance(tt.n)
			assert.Equal(t, tt.wantOffset, rr.Offset())
		})
	}
}
