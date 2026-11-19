package riff

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"sync"
)

// maxChannels is the highest channel count accepted in a WAVE fmt chunk.
// WAVE supports many channels but anything above this is either corrupted or
// not audio.
const maxChannels = 64

// Chunk represents the header and containt of a sub block
// See https://tech.ebu.ch/docs/tech/tech3285.pdf to see how
// audio content is stored in a BWF/WAVE file.
type Chunk struct {
	ID [4]byte
	// Size is the size used to skip the chunk: the declared size plus the
	// single padding byte required when the declared size is odd. The size
	// written in the chunk header is available via DeclaredSize.
	Size int
	// DeclaredSize is the size written in the chunk header. It doesn't include
	// the alignment padding byte of odd sized chunks. A declared size of 0 is
	// valid and meaningful, so Size remains the authoritative padded length.
	DeclaredSize uint32
	// Pos is the number of bytes of the chunk content read so far. It never
	// includes the padding byte.
	Pos int
	R   io.Reader
	// Offset is the absolute offset of the chunk header in the container.
	Offset int64
	// BigEndian reports whether the container (and therefore the chunk
	// numeric fields) is big endian. Sample data is never byte swapped by the
	// parser.
	BigEndian bool
	okChan    chan bool
	Wg        *sync.WaitGroup
}

// contentSize returns the number of bytes of chunk content, excluding the
// alignment padding byte of odd sized chunks. Size is only used as the fallback
// for chunks built by hand without a DeclaredSize; the parser always sets
// DeclaredSize (0 is a valid declared size for an empty chunk).
func (ch *Chunk) contentSize() int {
	if ch.DeclaredSize != 0 {
		return int(ch.DeclaredSize)
	}
	if ch.Size == 0 {
		return 0
	}
	return ch.Size
}

func (ch *Chunk) DecodeWavHeader(p *Parser) (err error) {
	if ch == nil {
		return fmt.Errorf("can't decode a nil chunk")
	}
	if ch.ID != FmtID {
		return nil
	}
	if p == nil {
		return fmt.Errorf("can't decode the fmt chunk of a nil parser")
	}
	if ch.contentSize() < 16 {
		return &ParseError{Chunk: ch.ID, Offset: ch.Offset, Err: ErrTruncated}
	}

	p.wavHeaderSize = uint32(ch.contentSize())
	order := ch.byteOrder()
	readField := func(dst interface{}) error {
		if err := ch.read(order, dst); err != nil {
			return &ParseError{Chunk: ch.ID, Offset: ch.Offset, Err: ErrTruncated}
		}
		return nil
	}
	if err := readField(&p.WavAudioFormat); err != nil {
		return err
	}
	if err := readField(&p.NumChannels); err != nil {
		return err
	}
	if err := readField(&p.SampleRate); err != nil {
		return err
	}
	if err := readField(&p.AvgBytesPerSec); err != nil {
		return err
	}
	if err := readField(&p.BlockAlign); err != nil {
		return err
	}
	if err := readField(&p.BitsPerSample); err != nil {
		return err
	}

	// The rest of the fmt chunk (cbSize and the extension of non PCM formats)
	// is skipped without allocating a buffer sized from the untrusted header.
	if rest := ch.contentSize() - ch.Pos; rest > 0 {
		before := ch.Pos
		ch.Pos += rest
		if _, err := io.CopyN(ioutil.Discard, ch.R, int64(rest)); err != nil {
			ch.Pos = before
			return &ParseError{Chunk: ch.ID, Offset: ch.Offset, Err: ErrTruncated}
		}
	}

	if p.Format == WavFormatID {
		if err := ch.validateFmt(p); err != nil {
			return err
		}
	}
	return nil
}

// validateFmt rejects fmt chunks that wouldn't allow a safe traversal of the
// sound data. Non PCM formats are only checked for their channel count.
//
// When the fmt fields, read with the container byte order, don't form a valid
// PCM header but do form one once byte swapped, the chunk was written with the
// other byte order and ErrMixedByteOrder is reported.
func (ch *Chunk) validateFmt(p *Parser) error {
	invalid := ch.checkFmt(p.WavAudioFormat, p.NumChannels, p.BitsPerSample)
	if invalid == nil {
		return nil
	}

	// Try the other byte order: a header that is only valid once swapped is a
	// byte order mix up rather than corruption.
	if ch.checkFmt(swap16(p.WavAudioFormat), swap16(p.NumChannels), swap16(p.BitsPerSample)) == nil {
		order := "big endian"
		if p.BigEndian {
			order = "little endian"
		}
		return &ParseError{Chunk: FmtID, Offset: ch.Offset,
			Err: fmt.Errorf("%w: fmt fields are %s while the container is %s", ErrMixedByteOrder, order, containerOrderName(p.BigEndian))}
	}
	return &ParseError{Chunk: FmtID, Offset: ch.Offset, Err: invalid}
}

func containerOrderName(bigEndian bool) string {
	if bigEndian {
		return "big endian (RIFX)"
	}
	return "little endian (RIFF)"
}

// checkFmt validates the fmt values needed to walk the sound data safely: the
// channel count and, for integer PCM, the sample size.
func (ch *Chunk) checkFmt(wavFormat, channels, bitsPerSample uint16) error {
	if channels == 0 {
		return fmt.Errorf("%w: got 0", ErrInvalidChannels)
	}
	if channels > maxChannels {
		return fmt.Errorf("%w: got %d (max %d)", ErrInvalidChannels, channels, maxChannels)
	}
	switch wavFormat {
	case 1, 0x0003, 0xfffe: // integer PCM, IEEE float, extensible
	default:
		// The other formats (compressed audio, ...) don't use the sample
		// size the same way.
		return nil
	}
	if wavFormat != 1 {
		return nil
	}
	switch bitsPerSample {
	case 8, 16, 24, 32:
	default:
		return fmt.Errorf("%w: got %d", ErrInvalidBitsPerSample, bitsPerSample)
	}
	return nil
}

// Done signals the parent parser that we are done reading the chunk
// if the chunk isn't fully read, this code will do so before signaling.
// The alignment padding byte of an odd sized chunk is always skipped, even
// when the content was consumed exactly.
func (ch *Chunk) Done() {
	ch.Drain()
	if ch.Wg != nil {
		ch.Wg.Done()
	}
}

// IsFullyRead checks if we're finished reading the chunk content. The padding
// byte of an odd sized chunk is not content and is skipped by Drain.
func (ch *Chunk) IsFullyRead() bool {
	if ch == nil || ch.R == nil {
		return true
	}
	return ch.contentSize() <= ch.Pos
}

// Read implements the reader interface. The padding byte of an odd sized
// chunk is never exposed as content: the reader reports io.EOF once the
// declared size has been consumed.
func (ch *Chunk) Read(p []byte) (n int, err error) {
	if ch == nil || ch.R == nil {
		return 0, errors.New("nil chunk/reader pointer")
	}
	remaining := ch.contentSize() - ch.Pos
	if remaining <= 0 {
		return 0, io.EOF
	}
	if len(p) > remaining {
		p = p[:remaining]
	}
	n, err = ch.R.Read(p)
	ch.Pos += n
	return n, err
}

// byteOrder returns the byte order of the chunk numeric fields.
func (ch *Chunk) byteOrder() binary.ByteOrder {
	if ch.BigEndian {
		return binary.BigEndian
	}
	return binary.LittleEndian
}

// read decodes the next binary.Size(dst) bytes with the given byte order,
// refusing to cross the end of the chunk content.
func (ch *Chunk) read(order binary.ByteOrder, dst interface{}) error {
	if ch == nil || ch.R == nil {
		return errors.New("nil chunk/reader pointer")
	}
	n := binary.Size(dst)
	if n < 0 {
		return fmt.Errorf("can't read into %T", dst)
	}
	if ch.contentSize()-ch.Pos < n {
		return io.EOF
	}
	if err := binary.Read(ch.R, order, dst); err != nil {
		return err
	}
	ch.Pos += n
	return nil
}

// ReadLE reads the Little Endian chunk data into the passed struct
func (ch *Chunk) ReadLE(dst interface{}) error {
	return ch.read(binary.LittleEndian, dst)
}

// ReadBE reads the Big Endian chunk data into the passed struct
func (ch *Chunk) ReadBE(dst interface{}) error {
	return ch.read(binary.BigEndian, dst)
}

// ReadSample reads the sound data into dst using the byte order of the
// container. A RIFX container stores its sample values in big endian order
// (for 16 bits and wider samples), so callers iterating over the samples
// should use this method instead of ReadLE.
func (ch *Chunk) ReadSample(dst interface{}) error {
	return ch.read(ch.byteOrder(), dst)
}

// ReadByte reads and returns a single byte
func (ch *Chunk) ReadByte() (byte, error) {
	if ch.IsFullyRead() {
		return 0, io.EOF
	}
	var r byte
	err := ch.read(ch.byteOrder(), &r)
	return r, err
}

// Drain discards the rest of the chunk content and the alignment padding byte
// of an odd sized chunk.
func (ch *Chunk) Drain() {
	bytesAhead := ch.contentSize() - ch.Pos
	for bytesAhead > 0 {
		n, err := io.CopyN(ioutil.Discard, ch.R, int64(bytesAhead))
		ch.Pos += int(n)
		bytesAhead -= int(n)
		if err != nil {
			return
		}
	}
	// Chunks are word aligned: an odd sized chunk is followed by a single zero
	// padding byte that isn't part of its declared size. Skip it so the next
	// chunk header is aligned.
	for ch.Pos < ch.Size {
		n, err := io.CopyN(ioutil.Discard, ch.R, int64(ch.Size-ch.Pos))
		ch.Pos += int(n)
		if err != nil {
			return
		}
	}
}
