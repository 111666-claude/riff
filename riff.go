package riff

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

var (
	RiffID = [4]byte{'R', 'I', 'F', 'F'}
	// RifxID identifies a big endian RIFF container. RIFX uses the same chunk
	// layout as RIFF but stores the container size, the chunk sizes and the
	// WAV fmt fields in big endian order. The sound data itself is left as-is.
	RifxID = [4]byte{'R', 'I', 'F', 'X'}
	FmtID  = [4]byte{'f', 'm', 't', ' '}
	// To align RIFF chunks to certain boundaries (i.e. 2048bytes for CD-ROMs) the RIFF specification includes a JUNK chunk.
	// Its contents are to be skipped when reading. When writing RIFFs, JUNK chunks should not have odd number as Size.
	junkID      = [4]byte{'J', 'U', 'N', 'K'}
	WavFormatID = [4]byte{'W', 'A', 'V', 'E'}
	// DataFormatID is the Wave Data Chunk ID, it contains the digital audio sample data which can be decoded using the format
	// and compression method specified in the Wave Format Chunk. If the Compression Code is 1 (uncompressed PCM), then the Wave Data contains raw sample values.
	DataFormatID = [4]byte{'d', 'a', 't', 'a'}
	rmiFormatID  = [4]byte{'R', 'M', 'I', 'D'}
	aviFormatID  = [4]byte{'A', 'V', 'I', ' '}
	// ErrFmtNotSupported is a generic error reporting an unknown format.
	ErrFmtNotSupported = errors.New("format not supported")
	// ErrUnexpectedData is a generic error reporting that the parser encountered unexpected data.
	ErrUnexpectedData = errors.New("unexpected data content")

	// ErrTruncated reports a header or a chunk that ends before its declared
	// size. It is also used for a chunk header that is cut short.
	ErrTruncated = errors.New("truncated data")
	// ErrSizeOutOfBounds reports a container or chunk size that doesn't fit in
	// the container / reader.
	ErrSizeOutOfBounds = errors.New("size out of bounds")
	// ErrMixedByteOrder reports a container whose data is written with the
	// byte order of the other container kind (for instance little endian fmt
	// fields in a RIFX container).
	ErrMixedByteOrder = errors.New("mixed byte order")
	// ErrInvalidChannels reports a PCM fmt chunk with an invalid channel count.
	ErrInvalidChannels = errors.New("invalid number of channels")
	// ErrInvalidBitsPerSample reports a PCM fmt chunk with an unsupported
	// sample size.
	ErrInvalidBitsPerSample = errors.New("invalid bits per sample")
	// ErrMissingFmt reports a WAVE container without a fmt chunk.
	ErrMissingFmt = errors.New("missing fmt chunk")
	// ErrMissingData reports a WAVE container without a data chunk.
	ErrMissingData = errors.New("missing data chunk")
	// ErrChunkTooLarge reports a chunk whose declared size is above the
	// parser limit. The check happens before any buffer is allocated so an
	// untrusted header can't trigger a giant allocation.
	ErrChunkTooLarge = errors.New("chunk too large")
)

// ParseError adds the position of the offending chunk or field to an error.
// Offset is an absolute byte offset from the start of the container (the
// "RIFF"/"RIFX" ID is at offset 0). Chunk is zero when the problem is at the
// container level.
type ParseError struct {
	// Chunk is the ID of the chunk that triggered the error, if any.
	Chunk [4]byte
	// Offset is the absolute byte offset of the offending chunk or field.
	Offset int64
	// Err is the underlying error.
	Err error
}

// Error implements the error interface.
func (e *ParseError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Chunk != [4]byte{} {
		return fmt.Sprintf("riff: chunk %q at offset %d: %v", string(e.Chunk[:]), e.Offset, e.Err)
	}
	return fmt.Sprintf("riff: offset %d: %v", e.Offset, e.Err)
}

// Unwrap returns the underlying error so errors.Is/As can be used with the
// exported sentinel errors of this package.
func (e *ParseError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// New creates a parser wrapper for a reader.
// Note that the reader doesn't get rewinded as the container is processed.
// Both RIFF (little endian) and RIFX (big endian) containers are supported;
// the byte order is detected when ParseHeaders is called.
func New(r io.Reader) *Parser {
	p := &Parser{Wg: &sync.WaitGroup{}}
	p.setReader(r)
	return p
}

// Duration returns the time duration of the passed reader if the sub format is supported.
func Duration(r io.Reader) (time.Duration, error) {
	c := New(r)
	if err := c.ParseHeaders(); err != nil {
		return 0, err
	}
	return c.Duration()
}
