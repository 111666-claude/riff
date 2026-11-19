package riff

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"math/bits"
	"sync"
	"time"
)

// DefaultMaxChunkSize is the default limit applied to a chunk size before the
// parser hands the chunk to a caller. It protects callers against a corrupted
// or malicious header asking for a huge allocation. It can be changed with
// Parser.MaxChunkSize.
const DefaultMaxChunkSize int64 = 1 << 30 // 1 GiB

// countingReader keeps track of the number of bytes consumed from the
// underlying reader so errors can be reported with an absolute container
// offset.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// Parser is a struct containing the overall container information.
type Parser struct {
	r io.Reader
	// cr is the counting wrapper around r. All the reads of the parser and of
	// its chunks go through it so byte offsets are always accurate.
	cr *countingReader
	// Chan is an Optional channel of chunks that is used to parse chunks
	Chan chan *Chunk
	// ChunkParserTimeout is the duration after which the main parser keeps going
	// if the dev hasn't reported the chunk parsing to be done.
	// By default: 2s
	ChunkParserTimeout time.Duration
	// The waitgroup is used to let the parser that it's ok to continue
	// after a chunk was passed to the optional parser channel.
	Wg *sync.WaitGroup

	// BigEndian reports whether the container is a RIFX container. RIFX uses
	// the same layout as RIFF but stores the container size, the chunk sizes
	// and the WAV fmt fields in big endian order. The sample data of a data
	// chunk is never byte swapped: it is exposed as the original byte stream.
	BigEndian bool

	// MaxChunkSize is the maximum accepted chunk size. Chunks declaring a
	// bigger size are rejected with ErrChunkTooLarge before any buffer is
	// allocated. Zero means DefaultMaxChunkSize.
	MaxChunkSize int64

	// Must match RIFF or RIFX
	ID [4]byte
	// This size is the size of the block
	// controlled by the RIFF header. Normally this equals the file size.
	Size uint32
	// Format name.
	// The representation of data in <wave-data>, and the content of the <format-specific-fields>
	// of the ‘fmt’ chunk, depend on the format category.
	// 0001h => Microsoft Pulse Code Modulation (PCM) format
	// 0050h => MPEG-1 Audio (audio only)
	Format [4]byte

	// WAV stuff
	// size of the wav specific fmt header
	wavHeaderSize uint32
	// A number indicating the WAVE format category of the file. The content of the
	// <format-specific-fields> portion of the ‘fmt’ chunk, and the interpretation of
	// the waveform data, depend on this value.
	// PCM = 1 (i.e. Linear quantization) Values other than 1 indicate some form of compression.
	WavAudioFormat uint16
	// The number of channels represented in the waveform data: 1 for mono or 2 for stereo.
	// Audio: Mono = 1, Stereo = 2, etc.
	// The EBU has defined the Multi-channel Broadcast Wave
	// Format [4] where more than two channels of audio are required.
	NumChannels uint16
	// The sampling rate (in sample per second) at which each channel should be played.
	// 8000, 44100, etc.
	SampleRate uint32
	// The average number of bytes per second at which the waveform data should be
	// transferred. Playback software can estimate the buffer size using this value.
	// SampleRate * NumChannels * BitsPerSample/8
	AvgBytesPerSec uint32
	// BlockAlign = SignificantBitsPerSample / 8 * NumChannels
	// It is the number of bytes per sample slice. This value is not affected by the number of channels and can be calculated with the formula:
	// NumChannels * BitsPerSample/8 The number of bytes for one sample including
	// all channels.
	// The block alignment (in bytes) of the waveform data. Playback software needs
	// to process a multiple of <nBlockAlign> bytes of data at a time, so the value of
	// <BlockAlign> can be used for buffer alignment.
	BlockAlign uint16
	// BitsPerSample 8, 16, 24...
	// Only available for PCM
	// This value specifies the number of bits used to define each sample. This value is usually 8, 16, 24 or 32.
	// If the number of bits is not byte aligned (a multiple of 8) then the number of bytes used per sample is
	// rounded up to the nearest byte size and the unused bytes are set to 0 and ignored.
	// The <nBitsPerSample> field specifies the number of bits of data used to represent each sample of
	// each channel. If there are multiple channels, the sample size is the same for each channel.
	BitsPerSample uint16

	// headerParsed is set once the container header has been consumed.
	headerParsed bool
	// startOffset is the absolute offset of the container ID in the reader.
	startOffset int64
	// containerEnd is the absolute offset one byte past the end of the
	// container as declared by its header.
	containerEnd int64
	// sawFmt/sawData track the mandatory WAVE chunks while parsing.
	sawFmt  bool
	sawData bool

	// padPending describes the alignment byte following an odd sized chunk
	// whose content was consumed without draining the chunk.
	padPending bool
	padOffset  int64
	padLength  int
}

// setReader installs r as the parser reader, wrapping it in a counting reader
// when necessary.
func (c *Parser) setReader(r io.Reader) {
	if cr, ok := r.(*countingReader); ok {
		c.cr = cr
		c.r = cr
		return
	}
	c.cr = &countingReader{r: r}
	c.r = c.cr
}

func (c *Parser) maxChunkSize() int64 {
	if c.MaxChunkSize <= 0 {
		return DefaultMaxChunkSize
	}
	return c.MaxChunkSize
}

// byteOrder returns the byte order used by the container for its sizes and
// numeric fields.
func (c *Parser) byteOrder() binary.ByteOrder {
	if c.BigEndian {
		return binary.BigEndian
	}
	return binary.LittleEndian
}

// ParseHeaders reads the header of the passed container and populate the container with parsed info.
// Note that this code advances the container reader.
func (c *Parser) ParseHeaders() error {
	if c == nil {
		return errors.New("can't parse the header of a nil pointer")
	}
	if c.cr == nil {
		return errors.New("can't parse the header of a parser without a reader")
	}
	if c.headerParsed {
		return nil
	}

	start := c.cr.n
	var id [4]byte
	if _, err := io.ReadFull(c.cr, id[:]); err != nil {
		return &ParseError{Offset: start, Err: ErrTruncated}
	}

	var bigEndian bool
	switch id {
	case RiffID:
		bigEndian = false
	case RifxID:
		bigEndian = true
	default:
		return &ParseError{Chunk: id, Offset: start, Err: ErrFmtNotSupported}
	}

	var sizeBuf [4]byte
	if _, err := io.ReadFull(c.cr, sizeBuf[:]); err != nil {
		return &ParseError{Chunk: id, Offset: start, Err: ErrTruncated}
	}
	var order binary.ByteOrder = binary.LittleEndian
	if bigEndian {
		order = binary.BigEndian
	}
	size := order.Uint32(sizeBuf[:])

	// A RIFF size smaller than the 4 byte format ID can't describe a valid
	// container.
	if size < 4 {
		return &ParseError{Chunk: id, Offset: start, Err: ErrSizeOutOfBounds}
	}

	// The format ID is part of the fixed 12 byte header: a short read is a
	// truncated header whatever the declared container size.
	var format [4]byte
	if _, err := io.ReadFull(c.cr, format[:]); err != nil {
		return &ParseError{Chunk: id, Offset: start, Err: ErrTruncated}
	}

	// 0xFFFFFFFF is the conventional "size unknown" value used by streaming
	// writers. The container then ends with the reader.
	unknownSize := size == 0xFFFFFFFF

	// When the reader knows its length, reject a container size that can't fit
	// in the remaining data. A size that only fits once read with the other
	// byte order means the sizes use a different byte order from the ID.
	if seeker, ok := c.cr.r.(io.Seeker); ok && !unknownSize {
		cur, errCur := seeker.Seek(0, io.SeekCurrent)
		end, errEnd := seeker.Seek(0, io.SeekEnd)
		_, errSeek := seeker.Seek(cur, io.SeekStart)
		if errCur == nil && errEnd == nil && errSeek == nil {
			// A container of size N holds N bytes after its 8 byte header.
			// Only the first four of those (the format ID) have been read so
			// far.
			available := end - cur + 4
			if int64(size) > available {
				var other binary.ByteOrder = binary.BigEndian
				if bigEndian {
					other = binary.LittleEndian
				}
				if swapped := other.Uint32(sizeBuf[:]); swapped >= 4 && int64(swapped) <= available {
					return &ParseError{Chunk: id, Offset: start, Err: ErrMixedByteOrder}
				}
				return &ParseError{Chunk: id, Offset: start, Err: ErrSizeOutOfBounds}
			}
		}
	}

	c.ID = id
	c.BigEndian = bigEndian
	c.Format = format
	c.startOffset = start
	if !unknownSize {
		c.containerEnd = start + 8 + int64(size)
	}
	c.Size = size
	c.headerParsed = true
	return nil
}

// Duration returns the time duration for the current RIFF container
// based on the sub format (wav etc...)
func (c *Parser) Duration() (time.Duration, error) {
	if c == nil {
		return 0, errors.New("can't calculate the duration of a nil pointer")
	}
	if c.ID == [4]byte{} || c.AvgBytesPerSec == 0 {
		if err := c.Parse(); err != nil {
			return 0, err
		}
	}
	switch c.Format {
	case WavFormatID:
		return c.wavDuration()
	default:
		return 0, ErrFmtNotSupported
	}
}

// String implements the Stringer interface.
func (c *Parser) String() string {
	out := fmt.Sprintf("Format: %s - ", c.Format)
	if c.Format == WavFormatID {
		out += fmt.Sprintf("%d channels @ %d / %d bits - ", c.NumChannels, c.SampleRate, c.BitsPerSample)
		d, _ := c.Duration()
		out += fmt.Sprintf("Duration: %f seconds", d.Seconds())
	}
	return out
}

// NextChunk returns a convenient structure to parse the next chunk.
// If the container is fully read, io.EOF is returned as an error.
func (c *Parser) NextChunk() (*Chunk, error) {
	if c == nil {
		return nil, errors.New("can't calculate the duration of a nil pointer")
	}
	if c.cr == nil {
		return nil, errors.New("can't read a chunk from a parser without a reader")
	}
	if !c.headerParsed {
		// Be forgiving with callers that skip ParseHeaders and go straight
		// to the chunk iteration: consume the container header first.
		if err := c.ParseHeaders(); err != nil {
			return nil, err
		}
	}

	start := c.cr.n
	// Skip the alignment byte of a previous odd sized chunk that a caller
	// consumed entirely without draining: it is not chunk content and the
	// next header starts after it.
	if c.padPending && start == c.padOffset {
		if _, err := io.CopyN(ioutil.Discard, c.cr, int64(c.padLength)); err != nil {
			return nil, &ParseError{Offset: start, Err: ErrTruncated}
		}
		start = c.cr.n
	}
	c.padPending = false
	if c.containerEnd > 0 {
		rem := c.containerEnd - start
		if rem <= 0 {
			return nil, io.EOF
		}
		// A single trailing byte can only be the alignment padding of an odd
		// sized final chunk. Anything shorter than a chunk header is truncated.
		if rem < 8 {
			if rem <= 1 {
				if _, err := io.CopyN(ioutil.Discard, c.cr, rem); err != nil {
					return nil, &ParseError{Offset: start, Err: ErrTruncated}
				}
				return nil, io.EOF
			}
			return nil, &ParseError{Offset: start, Err: ErrTruncated}
		}
	}

	var id [4]byte
	if n, err := io.ReadFull(c.cr, id[:]); err != nil {
		if n == 0 && err == io.EOF {
			// The reader ended even though the container promised more
			// chunks: the container is truncated.
			if c.containerEnd > 0 && start < c.containerEnd {
				return nil, &ParseError{Offset: start, Err: ErrTruncated}
			}
			return nil, io.EOF
		}
		return nil, &ParseError{Offset: start, Err: ErrTruncated}
	}

	var sizeBuf [4]byte
	if _, err := io.ReadFull(c.cr, sizeBuf[:]); err != nil {
		return nil, &ParseError{Chunk: id, Offset: start, Err: ErrTruncated}
	}
	size := c.byteOrder().Uint32(sizeBuf[:])

	// The declared chunk must fit in the container. When only the byte
	// swapped size fits, the file mixes the byte orders.
	if c.containerEnd > 0 && start+8+int64(size) > c.containerEnd {
		if swapped := swap32(size); start+8+int64(swapped) <= c.containerEnd {
			return nil, &ParseError{Chunk: id, Offset: start, Err: ErrMixedByteOrder}
		}
		return nil, &ParseError{Chunk: id, Offset: start, Err: ErrSizeOutOfBounds}
	}
	if int64(size) > c.maxChunkSize() {
		return nil, &ParseError{Chunk: id, Offset: start, Err: ErrChunkTooLarge}
	}

	// all RIFF chunks (including WAVE "data" chunks) must be word aligned.
	// If the data uses an odd number of bytes, a padding byte with a value of zero must be placed at the end of the sample data.
	// The "data" chunk header's size should not include this byte.
	padded := int64(size)
	if padded%2 == 1 {
		padded++
	}
	// Don't count a padding byte that would fall outside of the container
	// (a final odd sized chunk without its alignment byte).
	if c.containerEnd > 0 && start+8+padded > c.containerEnd {
		padded = int64(size)
	}
	if int64(int(padded)) != padded {
		// The host int can't represent the chunk length; refuse it rather
		// than silently wrapping.
		return nil, &ParseError{Chunk: id, Offset: start, Err: ErrChunkTooLarge}
	}

	ch := &Chunk{
		ID:           id,
		Size:         int(padded),
		DeclaredSize: size,
		Offset:       start,
		R:            c.cr,
		BigEndian:    c.BigEndian,
	}
	// Remember a padding byte a caller may leave behind by consuming only the
	// chunk content.
	c.padPending = padded > int64(size)
	c.padOffset = start + 8 + int64(size)
	c.padLength = int(padded - int64(size))

	switch id {
	case FmtID:
		c.sawFmt = true
	case DataFormatID:
		c.sawData = true
	}
	return ch, nil
}

// endOfChunks is used by Parse once the container is exhausted. A WAVE
// container without a fmt or a data chunk is reported as an error with its
// position instead of a plain io.EOF.
func (c *Parser) endOfChunks() error {
	if c.Format == WavFormatID && c.headerParsed {
		if !c.sawFmt {
			return &ParseError{Offset: c.startOffset + 8, Err: ErrMissingFmt}
		}
		if !c.sawData {
			return &ParseError{Offset: c.containerEnd, Err: ErrMissingData}
		}
	}
	return io.EOF
}

// IDnSize returns the next ID + block size. The size is read with the byte
// order of the container: little endian for RIFF, big endian for RIFX.
func (c *Parser) IDnSize() ([4]byte, uint32, error) {
	var ID [4]byte
	var blockSize uint32
	if c == nil || c.cr == nil {
		return ID, blockSize, errors.New("can't read from a nil parser")
	}
	start := c.cr.n
	if _, err := io.ReadFull(c.cr, ID[:]); err != nil {
		if err == io.EOF {
			// The reader ended cleanly between two chunks.
			return ID, blockSize, io.EOF
		}
		return ID, blockSize, &ParseError{Offset: start, Err: ErrTruncated}
	}

	// When reading a container header, the ID tells the byte order. Once the
	// header was parsed, the parser byte order applies to the chunk sizes.
	order := c.byteOrder()
	if !c.headerParsed {
		if ID == RifxID {
			order = binary.BigEndian
		} else if ID == RiffID {
			order = binary.LittleEndian
		}
	}

	var buf [4]byte
	if _, err := io.ReadFull(c.cr, buf[:]); err != nil {
		return ID, blockSize, &ParseError{Chunk: ID, Offset: start, Err: ErrTruncated}
	}
	blockSize = order.Uint32(buf[:])
	return ID, blockSize, nil
}

// Parse parses the content of the file and populate the useful fields.
// If the parser has a chan set, chunks are sent to the channel.
// For WAVE containers both a fmt and a data chunk are required.
func (p *Parser) Parse() error {
	if p == nil {
		return errors.New("can't calculate the wav duration of a nil pointer")
	}
	if p.cr == nil {
		return errors.New("can't parse a parser without a reader")
	}

	if !p.headerParsed {
		if err := p.ParseHeaders(); err != nil {
			return err
		}
	}

	var err error
	for {
		var chunk *Chunk
		chunk, err = p.NextChunk()
		if err != nil {
			break
		}

		switch chunk.ID {
		case FmtID:
			if derr := chunk.DecodeWavHeader(p); derr != nil {
				err = derr
			}
		}
		if err != nil {
			break
		}

		if p.Chan != nil && chunk.ID != FmtID {
			if chunk.Wg == nil {
				chunk.Wg = p.Wg
			}
			if chunk.Wg == nil {
				chunk.Wg = &sync.WaitGroup{}
			}
			chunk.Wg.Add(1)
			p.Chan <- chunk
			// the channel has to release otherwise the goroutine is locked
			chunk.Wg.Wait()
		}

		// BFW: bext chunk described here
		// https://tech.ebu.ch/docs/tech/tech3285.pdf

		// Always drain: it discards any content left unread and skips the
		// alignment byte of an odd sized chunk.
		chunk.Drain()
		if !chunk.IsFullyRead() {
			err = &ParseError{Chunk: chunk.ID, Offset: chunk.Offset, Err: ErrTruncated}
			break
		}
	}
	if p.Wg != nil {
		p.Wg.Wait()
	}

	if p.Chan != nil {
		close(p.Chan)
	}

	if err == io.EOF {
		// The chunks are exhausted: a WAVE container still needs its fmt and
		// data chunks.
		err = p.endOfChunks()
		if err == io.EOF {
			err = nil
		}
	}
	return err
}

// WavDuration returns the time duration of a wav container.
func (p *Parser) wavDuration() (time.Duration, error) {
	if p.Size == 0 || p.AvgBytesPerSec == 0 {
		return 0, fmt.Errorf("can't extract the duration due to the file not being properly parsed")
	}
	duration := time.Duration((float64(p.Size) / float64(p.AvgBytesPerSec)) * float64(time.Second))
	return duration, nil
}

// jumpTo advances the reader to the amount of bytes provided
func (p *Parser) jumpTo(bytesAhead int) error {
	for bytesAhead > 0 {
		readSize := bytesAhead
		if readSize > 4000 {
			readSize = 4000
		}
		n, err := io.CopyN(ioutil.Discard, p.cr, int64(readSize))
		bytesAhead -= int(n)
		if err != nil {
			return &ParseError{Offset: p.cr.n, Err: ErrTruncated}
		}
	}
	return nil
}

// swap16 returns v with its bytes swapped.
func swap16(v uint16) uint16 { return bits.ReverseBytes16(v) }

// swap32 returns v with its bytes swapped.
func swap32(v uint32) uint32 { return bits.ReverseBytes32(v) }
