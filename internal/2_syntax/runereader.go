package syntax

//go:generate go run github.com/vektra/mockery/v2@v2.52.2

import (
	"errors"
	"io"
	"unicode/utf8"
)

const (
	utf8ASCIIHi      = 0x7F // 1-byte header upper bound
	utf8Lead2ValidLo = 0xC2 // 2-byte header lower bound
	utf8Lead3Lo      = 0xE0 // 3-byte header lower bound
	utf8Lead4Lo      = 0xF0 // 4-byte header lower bound
	utf8Lead4ValidHi = 0xF4 // 4-byte header upper bound
)

var (
	errIncompleteUTF8EOF  = errors.New("incomplete UTF-8 at end of input")
	errInvalidUTF8Input   = errors.New("invalid UTF-8")
	errReadZeroWithoutEOF = errors.New("reader returned 0 bytes without EOF")
)

type RuneReader struct {
	// reader is the underlying reader
	reader io.ReadCloser
	// buf is a buffer of peeked runes
	buf []rune
	// offset is the number of runes consumed from the stream start (via Advance).
	offset uint
}

func newRuneReader(r io.ReadCloser) *RuneReader {
	return &RuneReader{reader: r}
}

// Offset returns the number of runes consumed from the stream start.
func (rr *RuneReader) Offset() uint {
	return rr.offset
}

// Peek returns the i-th upcoming rune from the stream, or an error.
// The error is io.EOF if the stream is drained before the i-th rune is available.
func (rr *RuneReader) Peek(i uint) (rune, error) {
	for uint(len(rr.buf)) <= i {
		before := len(rr.buf)
		if err := rr.decodeOneRune(); err != nil {
			return 0, err
		} else if len(rr.buf) == before && rr.reader == nil {
			return 0, io.EOF
		}
	}

	return rr.buf[i], nil
}

func (rr *RuneReader) Close() {
	if rr.reader == nil {
		return
	}
	_ = rr.reader.Close()
	rr.reader = nil
}

// pollByte reads one byte from the stream, returns the byte, the frame length, or an error.
func (rr *RuneReader) pollByte() (b byte, frame int, err error) {
	var one [1]byte
	nr, rerr := rr.reader.Read(one[:])
	if nr == 0 && rerr == nil {
		rr.Close()
		return 0, 0, errReadZeroWithoutEOF
	} else if nr != 1 || rerr != nil {
		rr.Close()
		if nr != 1 || rerr != io.EOF {
			return 0, 0, rerr
		}
	}
	b = one[0]
	switch {
	case b <= utf8ASCIIHi:
		frame = 1
	case b < utf8Lead2ValidLo:
		rr.Close()
		return b, 0, errInvalidUTF8Input
	case b < utf8Lead3Lo:
		frame = 2
	case b < utf8Lead4Lo:
		frame = 3
	case b <= utf8Lead4ValidHi:
		frame = 4
	default:
		rr.Close()
		return b, 0, errInvalidUTF8Input
	}
	return b, frame, nil
}

// decodeOneRune reads one UTF-8 character from the reader and appends it to buf.
func (rr *RuneReader) decodeOneRune() error {
	if rr.reader == nil {
		return nil
	}

	b0, n, err := rr.pollByte()
	if err != nil {
		return err
	}
	var utf [utf8.UTFMax]byte
	utf[0] = b0
	if n > 1 {
		if _, err := io.ReadFull(rr.reader, utf[1:n]); err != nil {
			rr.Close()
			if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
				return errIncompleteUTF8EOF
			}
			return err
		}
	}
	r, rw := utf8.DecodeRune(utf[:n])
	if (r == utf8.RuneError && rw == 1) || rw != n {
		rr.Close()
		return errInvalidUTF8Input
	}
	rr.buf = append(rr.buf, r)
	return nil
}

// Advance drops the next n runes from the stream.
func (rr *RuneReader) Advance(n uint) {
	for i := uint(0); i < n; i++ {
		if len(rr.buf) == 0 {
			_ = rr.decodeOneRune() //nolint:errcheck
		}
		rr.buf = rr.buf[1:]
		rr.offset++
	}
	if n > 0 {
		compacted := make([]rune, len(rr.buf))
		copy(compacted, rr.buf)
		rr.buf = compacted
	}
}
