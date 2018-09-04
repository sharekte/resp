package resp

import (
	"bufio"
	"io"
)

// Reader wraps an io.Reader and provides methods for reading the RESP protocol.
type Reader struct {
	br *bufio.Reader

	// ownbr holds a *bufio.Reader that is reused when calling Reset. This is used in cases the io.Reader given to
	// Reset is already a *bufio.Reader to avoid reusing the user given *bufio.Reader when calling Reset.
	ownbr *bufio.Reader
}

// NewReader returns a *Reader that uses the given io.Reader for reads.
//
// See Reset for more information on buffering on the given io.Reader works.
func NewReader(r io.Reader) *Reader {
	var rr Reader
	rr.Reset(r)
	return &rr
}

var _ io.Reader = (*Reader)(nil)

// Reset sets the underlying io.Reader tor and resets all internal state.
//
// If the given io.Reader is an *bufio.Reader it is used directly without additional buffering.
func (rr *Reader) Reset(r io.Reader) {
	if br, ok := r.(*bufio.Reader); ok {
		rr.br = br
		return
	}

	if rr.ownbr == nil {
		rr.ownbr = bufio.NewReader(r)
	} else {
		rr.ownbr.Reset(r)
	}

	rr.br = rr.ownbr
}

// Peek looks at the next byte in the underlying reader and returns the Type of the response.
func (rr *Reader) Peek() (Type, error) {
	b, err := rr.br.Peek(1)
	if err != nil {
		return TypeInvalid, err
	}

	return types[b[0]], nil
}

func (rr *Reader) expect(t Type) error {
	g, err := rr.Peek()
	if err != nil {
		return err
	}
	if g != t {
		return ErrUnexpectedType
	}
	_, err = rr.br.Discard(1)
	return err
}

func (rr *Reader) readNumberLine() (int, error) {
	var n int
	var neg bool

loop:
	for i := 0; ; i++ {
		b, err := rr.br.ReadByte()
		if err != nil {
			return 0, err
		}

		switch {
		case b == '-' && i == 0:
			neg = true
		case b >= '0' && b <= '9':
			n *= 10
			n += int(b - '0')
		case b == '\r':
			b1, err := rr.br.ReadByte()
			if err == io.EOF {
				return 0, ErrUnexpectedEOL
			}
			if err != nil {
				return 0, err
			}

			if b1 == '\n' {
				break loop
			}

			_ = rr.br.UnreadByte()
			_ = rr.br.UnreadByte()
			return 0, ErrUnexpectedEOL
		case b == '\n':
			_ = rr.br.UnreadByte()
			return 0, ErrUnexpectedEOL
		default:
			_ = rr.br.UnreadByte()
			return 0, ErrInvalidInteger
		}
	}

	if neg {
		n *= -1
	}

	return n, nil
}

func (rr *Reader) readLine(dst []byte) (int, []byte, error) {
	line, err := rr.br.ReadSlice('\n')
	if err == io.EOF {
		return 0, nil, ErrUnexpectedEOL
	}
	if err != nil {
		return 0, nil, err
	}
	if len(line) < 2 || line[len(line)-2] != '\r' {
		return 0, nil, ErrUnexpectedEOL
	}
	if dst = append(dst, line[:len(line)-2]...); dst == nil {
		dst = []byte{} // make sure we don't return nil, so we can better distinguish this from a NULL response
	}
	return len(line) - 2, dst, nil
}

func (rr *Reader) readLineN(dst []byte, n int) (int, []byte, error) {
	line, err := rr.br.Peek(n + 2)
	if err == io.EOF {
		return 0, nil, ErrUnexpectedEOL
	}
	if err != nil {
		return 0, nil, err
	}
	if len(line) != n+2 || line[len(line)-2] != '\r' || line[len(line)-1] != '\n' {
		return 0, nil, ErrUnexpectedEOL
	}
	if dst = append(dst, line[:len(line)-2]...); dst == nil {
		dst = []byte{} // make sure we don't return nil, so we can better distinguish this from a NULL response
	}
	if _, err := rr.br.Discard(len(line)); err != nil {
		return 0, nil, err
	}
	return len(line) - 2, dst, nil
}

// Read reads raw data from the underlying io.Reader into dst.
//
// It implements the io.Reader interface.
func (rr *Reader) Read(dst []byte) (n int, err error) {
	return rr.br.Read(dst)
}

// ReadArrayHeader reads an array header, returning the array length.
//
// If the next type in the response is not an array, ErrUnexpectedType is returned.
func (rr *Reader) ReadArrayHeader() (int, error) {
	if err := rr.expect(TypeArray); err != nil {
		return 0, err
	}
	n, err := rr.readNumberLine()
	if n < -1 || err == ErrInvalidInteger {
		n, err = 0, ErrInvalidArrayLength
	}
	return n, err
}

// ReadBulkStringHeader reads a bulk string header, returning the bulk string length, without reading the bulk string itself.
//
// If the next type in the response is not a bulk string, ErrUnexpectedType is returned.
func (rr *Reader) ReadBulkStringHeader() (int, error) {
	if err := rr.expect(TypeBulkString); err != nil {
		return 0, err
	}
	n, err := rr.readNumberLine()
	if n < -1 || err == ErrInvalidInteger {
		n, err = 0, ErrInvalidBulkStringLength
	}
	return n, err
}

// ReadBulkString reads a bulk string into the byte slice dst, returning the bulk string length and the resulting byte slice.
//
// If the next type in the response is not a bulk string, ErrUnexpectedType is returned.
func (rr *Reader) ReadBulkString(dst []byte) (int, []byte, error) {
	n, err := rr.ReadBulkStringHeader()
	if err != nil {
		return 0, nil, err
	}
	if n == -1 {
		return -1, nil, err
	}
	return rr.readLineN(dst, n)
}

// ReadError reads an error into the byte slice dst, returning the length and the resulting byte slice.
//
// If the next type in the response is not an error, ErrUnexpectedType is returned.
func (rr *Reader) ReadError(dst []byte) (int, []byte, error) {
	if err := rr.expect(TypeError); err != nil {
		return 0, nil, err
	}
	return rr.readLine(dst)
}

// ReadInteger reads a single RESP integer.
//
// If the next type in the response is not an integer, ErrUnexpectedType is returned.
func (rr *Reader) ReadInteger() (int, error) {
	if err := rr.expect(TypeInteger); err != nil {
		return 0, err
	}
	return rr.readNumberLine()
}

// ReadSimpleString reads a simple string into the byte slice dst, returning the length and the resulting byte slice.
//
// If the next type in the response is not a simple string, ErrUnexpectedType is returned.
func (rr *Reader) ReadSimpleString(dst []byte) (int, []byte, error) {
	if err := rr.expect(TypeSimpleString); err != nil {
		return 0, nil, err
	}
	return rr.readLine(dst)
}
