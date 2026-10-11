package syntax

import (
	"testing"

	"github.com/stretchr/testify/assert"
	base "github.com/zambozoo/zam/internal/0_base"
)

func TestToken_New(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		kind   TokenKind
		offset uint
		value  any
		want   *Token
	}{
		{
			name:   "new token",
			kind:   TTIdentifier,
			offset: 0,
			value:  "test",
			want: &Token{
				Kind:   TTIdentifier,
				Offset: 0,
				Value:  "test",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := newToken(test.kind, test.offset, test.value)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestToken_Position(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		token      *Token
		lineStarts []uint
		want       *base.Position
	}{
		{
			name:       "token at line start",
			token:      &Token{Offset: 0},
			lineStarts: []uint{0},
			want:       &base.Position{Line: 1, Column: 1},
		},
		{
			name:       "token at line middle",
			token:      &Token{Offset: 10},
			lineStarts: []uint{0, 5},
			want:       &base.Position{Line: 2, Column: 6},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := test.token.Position(test.lineStarts)
			assert.Equal(t, test.want, got)
		})
	}
}
