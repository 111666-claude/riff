package riff

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureChunk is a chunk of a fixture described by the test helpers below.
type fixtureChunk struct {
	id   [4]byte
	data []byte
}

func byteOrder(bigEndian bool) binary.ByteOrder {
	if bigEndian {
		return binary.BigEndian
	}
	return binary.LittleEndian
}

// buildWav assembles a WAVE container from the given chunks. Sizes and the
// fmt fields are written with the container byte order; a padding byte is
// added after odd sized chunks.
func buildWav(bigEndian bool, format [4]byte, chunks ...fixtureChunk) []byte {
	order := byteOrder(bigEndian)
	body := &bytes.Buffer{}
	body.Write(format[:])
	for _, ch := range chunks {
		body.Write(ch.id[:])
		var sz [4]byte
		order.PutUint32(sz[:], uint32(len(ch.data)))
		body.Write(sz[:])
		body.Write(ch.data)
		if len(ch.data)%2 == 1 {
			body.WriteByte(0)
		}
	}
	out := &bytes.Buffer{}
	if bigEndian {
		out.WriteString("RIFX")
	} else {
		out.WriteString("RIFF")
	}
	var sz [4]byte
	order.PutUint32(sz[:], uint32(body.Len()))
	out.Write(sz[:])
	out.Write(body.Bytes())
	return out.Bytes()
}

// buildFmt returns a canonical 16 byte PCM fmt payload.
func buildFmt(bigEndian bool, channels uint16, rate uint32, bits uint16) []byte {
	order := byteOrder(bigEndian)
	blockAlign := channels * bits / 8
	buf := &bytes.Buffer{}
	write := func(v interface{}) {
		if err := binary.Write(buf, order, v); err != nil {
			panic(err)
		}
	}
	write(uint16(1))                 // PCM
	write(channels)                  // channels
	write(rate)                      // sample rate
	write(rate * uint32(blockAlign)) // byte rate
	write(blockAlign)                // block align
	write(bits)                      // bits per sample
	return buf.Bytes()
}

// parseAll parses the container and returns the chunks in order along with the
// sample data of every data chunk.
func parseAll(t *testing.T, r io.Reader) (*Parser, []*Chunk, [][]byte) {
	t.Helper()
	p := New(r)
	if err := p.ParseHeaders(); err != nil {
		t.Fatalf("ParseHeaders: %v", err)
	}
	var chunks []*Chunk
	var data [][]byte
	for {
		ch, err := p.NextChunk()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("NextChunk: %v", err)
		}
		if ch.ID == FmtID {
			if err := ch.DecodeWavHeader(p); err != nil {
				t.Fatalf("DecodeWavHeader: %v", err)
			}
		} else if ch.ID == DataFormatID {
			b, err := ioutil.ReadAll(ch)
			if err != nil {
				t.Fatalf("reading data chunk: %v", err)
			}
			data = append(data, b)
		}
		ch.Done()
		chunks = append(chunks, ch)
	}
	return p, chunks, data
}

func TestRifxHeaders(t *testing.T) {
	fixtures := []struct {
		path      string
		bigEndian bool
		channels  uint16
		rate      uint32
		bits      uint16
		byteRate  uint32
		align     uint16
		fmtSize   uint32
	}{
		{"fixtures/pcm16_riff.wav", false, 2, 44100, 16, 176400, 4, 16},
		{"fixtures/pcm16_rifx.wav", true, 2, 44100, 16, 176400, 4, 16},
		{"fixtures/pcm24_riff.wav", false, 1, 44100, 24, 132300, 3, 18},
		{"fixtures/pcm24_rifx.wav", true, 1, 44100, 24, 132300, 3, 18},
	}

	for _, exp := range fixtures {
		t.Run(filepath.Base(exp.path), func(t *testing.T) {
			f, err := os.Open(exp.path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()

			p, _, _ := parseAll(t, f)

			if p.ID != RiffID && p.ID != RifxID {
				t.Fatalf("unexpected container ID %q", p.ID)
			}
			if p.BigEndian != exp.bigEndian {
				t.Fatalf("BigEndian = %v, want %v", p.BigEndian, exp.bigEndian)
			}
			wantID := RiffID
			if exp.bigEndian {
				wantID = RifxID
			}
			if p.ID != wantID {
				t.Fatalf("ID = %q, want %q", p.ID, wantID)
			}
			if p.Format != WavFormatID {
				t.Fatalf("Format = %q, want %q", p.Format, WavFormatID)
			}
			if p.WavAudioFormat != 1 {
				t.Fatalf("WavAudioFormat = %d, want 1", p.WavAudioFormat)
			}
			if p.NumChannels != exp.channels {
				t.Fatalf("NumChannels = %d, want %d", p.NumChannels, exp.channels)
			}
			if p.SampleRate != exp.rate {
				t.Fatalf("SampleRate = %d, want %d", p.SampleRate, exp.rate)
			}
			if p.AvgBytesPerSec != exp.byteRate {
				t.Fatalf("AvgBytesPerSec = %d, want %d", p.AvgBytesPerSec, exp.byteRate)
			}
			if p.BlockAlign != exp.align {
				t.Fatalf("BlockAlign = %d, want %d", p.BlockAlign, exp.align)
			}
			if p.BitsPerSample != exp.bits {
				t.Fatalf("BitsPerSample = %d, want %d", p.BitsPerSample, exp.bits)
			}
			if p.wavHeaderSize != exp.fmtSize {
				t.Fatalf("wavHeaderSize = %d, want %d", p.wavHeaderSize, exp.fmtSize)
			}
		})
	}
}

// TestRifxMatchesRiff checks that a RIFX file yields the same fields, chunk
// order and decoded samples as the RIFF file with the same content. The data
// chunk of a RIFX file stores its samples big endian while the RIFF one stores
// them little endian, so the comparison happens at the sample level.
func TestRifxMatchesRiff(t *testing.T) {
	pairs := [][2]string{
		{"fixtures/pcm16_riff.wav", "fixtures/pcm16_rifx.wav"},
		{"fixtures/pcm24_riff.wav", "fixtures/pcm24_rifx.wav"},
	}

	for _, pair := range pairs {
		t.Run(filepath.Base(pair[1]), func(t *testing.T) {
			leFile, err := os.Open(pair[0])
			if err != nil {
				t.Fatal(err)
			}
			defer leFile.Close()
			beFile, err := os.Open(pair[1])
			if err != nil {
				t.Fatal(err)
			}
			defer beFile.Close()

			leParser, leChunks, leData := parseAll(t, leFile)
			beParser, beChunks, beData := parseAll(t, beFile)

			if leParser.Size != beParser.Size {
				t.Fatalf("container sizes differ: RIFF %d, RIFX %d", leParser.Size, beParser.Size)
			}
			if leParser.Format != beParser.Format {
				t.Fatalf("formats differ: %q vs %q", leParser.Format, beParser.Format)
			}
			if leParser.WavAudioFormat != beParser.WavAudioFormat ||
				leParser.NumChannels != beParser.NumChannels ||
				leParser.SampleRate != beParser.SampleRate ||
				leParser.AvgBytesPerSec != beParser.AvgBytesPerSec ||
				leParser.BlockAlign != beParser.BlockAlign ||
				leParser.BitsPerSample != beParser.BitsPerSample ||
				leParser.wavHeaderSize != beParser.wavHeaderSize {
				t.Fatalf("fmt fields differ:\nRIFF %+v\nRIFX %+v", leParser, beParser)
			}

			if len(leChunks) != len(beChunks) {
				t.Fatalf("chunk counts differ: %d vs %d", len(leChunks), len(beChunks))
			}
			var ids []string
			for i := range leChunks {
				if leChunks[i].ID != beChunks[i].ID {
					t.Fatalf("chunk %d ID differs: %q vs %q", i, leChunks[i].ID, beChunks[i].ID)
				}
				if leChunks[i].DeclaredSize != beChunks[i].DeclaredSize {
					t.Fatalf("chunk %d size differs: %d vs %d", i, leChunks[i].DeclaredSize, beChunks[i].DeclaredSize)
				}
				ids = append(ids, string(leChunks[i].ID[:]))
			}
			if len(leData) != len(beData) {
				t.Fatalf("data chunk counts differ: %d vs %d", len(leData), len(beData))
			}
			for i := range leData {
				if len(leData[i]) != len(beData[i]) {
					t.Fatalf("data chunk %d lengths differ: %d vs %d", i, len(leData[i]), len(beData[i]))
				}
				// Same samples, stored with the container byte order.
				if got := normalizeSamples(leData[i], leParser.BitsPerSample, binary.LittleEndian); !bytes.Equal(got, normalizeSamples(beData[i], beParser.BitsPerSample, binary.BigEndian)) {
					t.Fatalf("data chunk %d samples differ", i)
				}
			}
			t.Logf("chunk order: %v", ids)
		})
	}
}

// normalizeSamples returns the sample bytes re-encoded little endian whatever
// the source byte order, so containers carrying the same samples can be
// compared byte for byte.
func normalizeSamples(data []byte, bitsPerSample uint16, order binary.ByteOrder) []byte {
	width := int(bitsPerSample) / 8
	out := append([]byte(nil), data...)
	var buf [4]byte
	for i := 0; i+width <= len(out); i += width {
		copy(buf[:], out[i:i+width])
		for j := 0; j < width; j++ {
			out[i+j] = buf[width-1-j]
		}
		if order == binary.LittleEndian {
			// already little endian, restore the source order
			copy(out[i:i+width], buf[:width])
		}
	}
	return out
}

// TestRifxSampleReads checks that ReadSample uses the container byte order
// while the underlying bytes stay untouched.
func TestRifxSampleReads(t *testing.T) {
	open := func(path string) (*Parser, *Chunk, func()) {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		p := New(f)
		if err := p.ParseHeaders(); err != nil {
			t.Fatal(err)
		}
		for {
			ch, err := p.NextChunk()
			if err != nil {
				t.Fatal(err)
			}
			if ch.ID == FmtID {
				if err := ch.DecodeWavHeader(p); err != nil {
					t.Fatal(err)
				}
				ch.Done()
				continue
			}
			if ch.ID == DataFormatID {
				return p, ch, func() { f.Close() }
			}
			ch.Done()
		}
	}

	for _, pair := range [][2]string{
		{"fixtures/pcm16_riff.wav", "fixtures/pcm16_rifx.wav"},
		{"fixtures/pcm24_riff.wav", "fixtures/pcm24_rifx.wav"},
	} {
		t.Run(filepath.Base(pair[1]), func(t *testing.T) {
			leParser, leChunk, closeLE := open(pair[0])
			defer closeLE()
			beParser, beChunk, closeBE := open(pair[1])
			defer closeBE()

			readSamples := func(p *Parser, ch *Chunk) []int32 {
				width := int(p.BitsPerSample) / 8
				block := int(p.BlockAlign)
				var samples []int32
				for !ch.IsFullyRead() {
					buf := make([]byte, block)
					if err := ch.ReadSample(&buf); err != nil {
						t.Fatal(err)
					}
					order := ch.byteOrder()
					for i := 0; i+width <= len(buf); i += width {
						switch width {
						case 2:
							samples = append(samples, int32(int16(order.Uint16(buf[i:]))))
						case 3:
							var v int32
							if order == binary.BigEndian {
								v = int32(buf[i])<<16 | int32(buf[i+1])<<8 | int32(buf[i+2])
							} else {
								v = int32(buf[i]) | int32(buf[i+1])<<8 | int32(buf[i+2])<<16
							}
							if v&0x800000 != 0 {
								v |= ^0xFFFFFF
							}
							samples = append(samples, v)
						}
					}
				}
				return samples
			}

			leSamples := readSamples(leParser, leChunk)
			beSamples := readSamples(beParser, beChunk)
			if len(leSamples) != len(beSamples) {
				t.Fatalf("sample counts differ: %d vs %d", len(leSamples), len(beSamples))
			}
			for i := range leSamples {
				if leSamples[i] != beSamples[i] {
					t.Fatalf("sample %d differs: RIFF %d, RIFX %d", i, leSamples[i], beSamples[i])
				}
			}
			if len(leSamples) != leChunk.contentSize()/(int(leParser.BitsPerSample)/8) {
				t.Fatalf("read %d samples from %d bytes", len(leSamples), leChunk.contentSize())
			}
		})
	}
}

func TestRifxRawDataIsUntouched(t *testing.T) {
	want, err := ioutil.ReadFile("fixtures/pcm16_riff.wav")
	if err != nil {
		t.Fatal(err)
	}
	// The data payload sits at the same absolute offset in both fixtures.
	_, leChunks, leData := parseAll(t, bytes.NewReader(want))
	rifx, err := ioutil.ReadFile("fixtures/pcm16_rifx.wav")
	if err != nil {
		t.Fatal(err)
	}
	_, _, beData := parseAll(t, bytes.NewReader(rifx))

	var dataOffset int64 = -1
	for _, ch := range leChunks {
		if ch.ID == DataFormatID {
			dataOffset = ch.Offset + 8
		}
	}
	if dataOffset < 0 {
		t.Fatal("no data chunk in fixture")
	}
	for i := range leData {
		if len(leData[i]) != len(beData[i]) {
			t.Fatalf("data length differs: %d vs %d", len(leData[i]), len(beData[i]))
		}
	}

	// Locate the data chunk in the RIFX file independently and compare the raw
	// bytes stored on disk with what the parser exposed.
	idx := bytes.Index(rifx, []byte("data"))
	if idx < 0 {
		t.Fatal("data chunk not found")
	}
	rawOnDisk := rifx[idx+8 : idx+8+len(beData[0])]
	if !bytes.Equal(rawOnDisk, beData[0]) {
		t.Fatal("the parser altered the data chunk bytes")
	}
	// The RIFF fixture stores the same samples little endian, so its raw
	// bytes are the byte swapped version of the RIFX ones.
	if bytes.Equal(rawOnDisk, leData[0]) {
		t.Fatal("the fixtures should store their samples with their own byte order")
	}
	for i := 0; i+2 <= len(rawOnDisk); i += 2 {
		if rawOnDisk[i] != leData[0][i+1] || rawOnDisk[i+1] != leData[0][i] {
			t.Fatalf("byte %d: RIFX %x isn't the big endian form of RIFF %x", i, rawOnDisk[i:i+2], leData[0][i:i+2])
		}
	}
}

func TestSmallRifxContainers(t *testing.T) {
	// A tiny 8 bit mono RIFX file exercises odd chunk sizes and the padding
	// byte together with unknown chunks.
	payload := []byte{0x00, 0x7f, 0x80, 0xff, 0x01} // 5 bytes, odd
	buf := buildWav(true, WavFormatID,
		fixtureChunk{FmtID, buildFmt(true, 1, 8000, 8)},
		fixtureChunk{[4]byte{'L', 'I', 'S', 'T'}, []byte("INFOabcdef")},
		fixtureChunk{DataFormatID, payload},
	)

	p, chunks, data := parseAll(t, bytes.NewReader(buf))
	if !p.BigEndian {
		t.Fatal("expected a big endian parser")
	}
	if p.NumChannels != 1 || p.SampleRate != 8000 || p.BitsPerSample != 8 {
		t.Fatalf("unexpected fmt: %+v", p)
	}
	var order []string
	for _, ch := range chunks {
		order = append(order, string(ch.ID[:]))
	}
	if len(order) != 3 || order[0] != "fmt " || order[1] != "LIST" || order[2] != "data" {
		t.Fatalf("unexpected chunk order: %v", order)
	}
	if len(data) != 1 || !bytes.Equal(data[0], payload) {
		t.Fatalf("data = %x, want %x", data, payload)
	}
	// The data chunk is padded to an even size, the padding byte is not part
	// of the declared size but is consumed by the parser.
	if chunks[2].DeclaredSize != 5 {
		t.Fatalf("declared size = %d, want 5", chunks[2].DeclaredSize)
	}
	if chunks[2].Size != 6 {
		t.Fatalf("padded size = %d, want 6", chunks[2].Size)
	}
}

func TestLiRifxSmallOddData(t *testing.T) {
	// The RIFF counterpart of the previous test.
	payload := []byte{0x00, 0x7f, 0x80, 0xff, 0x01}
	buf := buildWav(false, WavFormatID,
		fixtureChunk{FmtID, buildFmt(false, 1, 8000, 8)},
		fixtureChunk{[4]byte{'L', 'I', 'S', 'T'}, []byte("INFOabcdef")},
		fixtureChunk{DataFormatID, payload},
	)
	p, chunks, data := parseAll(t, bytes.NewReader(buf))
	if p.BigEndian {
		t.Fatal("expected a little endian parser")
	}
	if len(data) != 1 || !bytes.Equal(data[0], payload) {
		t.Fatalf("data = %x, want %x", data, payload)
	}
	if chunks[2].DeclaredSize != 5 || chunks[2].Size != 6 {
		t.Fatalf("unexpected data chunk sizes: %d / %d", chunks[2].DeclaredSize, chunks[2].Size)
	}
}

func TestMixedByteOrder(t *testing.T) {
	t.Run("little endian chunk size in rifx container", func(t *testing.T) {
		// A RIFX container whose fmt chunk size is written little endian.
		// 16 little endian reads as 0x10000000 in big endian, far past the
		// end of the container.
		buf := buildWav(true, WavFormatID,
			fixtureChunk{FmtID, buildFmt(true, 2, 44100, 16)},
			fixtureChunk{DataFormatID, make([]byte, 32)},
		)
		// chunk header at offset 12: ID(4) + size(4).
		binary.LittleEndian.PutUint32(buf[16:20], 16)
		p, err := parseChunksErr(buf)
		if err == nil {
			t.Fatal("expected an error")
		}
		t.Logf("got: %v", err)
		if !errors.Is(err, ErrMixedByteOrder) {
			t.Fatalf("expected ErrMixedByteOrder, got %v", err)
		}
		if p.BigEndian != true {
			t.Fatal("container byte order detection got lost")
		}
	})

	t.Run("big endian fmt fields in riff container", func(t *testing.T) {
		buf := buildWav(false, WavFormatID,
			fixtureChunk{FmtID, buildFmt(true, 2, 44100, 16)},
			fixtureChunk{DataFormatID, make([]byte, 32)},
		)
		_, err := parseChunksErr(buf)
		if err == nil {
			t.Fatal("expected an error")
		}
		t.Logf("got: %v", err)
		if !errors.Is(err, ErrMixedByteOrder) {
			t.Fatalf("expected ErrMixedByteOrder, got %v", err)
		}
	})

	t.Run("big endian container size in riff container", func(t *testing.T) {
		// Same container, but its size field was written big endian. The big
		// endian value is small enough to fit the reader while the little
		// endian one is not.
		buf := buildWav(false, WavFormatID,
			fixtureChunk{FmtID, buildFmt(false, 2, 44100, 16)},
			fixtureChunk{DataFormatID, make([]byte, 32)},
		)
		binary.BigEndian.PutUint32(buf[4:8], uint32(len(buf)-8))
		p := New(bytes.NewReader(buf))
		err := p.ParseHeaders()
		if err == nil {
			t.Fatal("expected an error")
		}
		t.Logf("got: %v", err)
		if !errors.Is(err, ErrMixedByteOrder) {
			t.Fatalf("expected ErrMixedByteOrder, got %v", err)
		}
	})
}

func TestRifxErrors(t *testing.T) {
	validFmt := buildFmt(true, 2, 44100, 16)
	valid := buildWav(true, WavFormatID,
		fixtureChunk{FmtID, validFmt},
		fixtureChunk{DataFormatID, make([]byte, 32)},
	)

	tests := []struct {
		name        string
		buf         []byte
		want        error
		nonSeekable bool
	}{
		{
			name: "truncated header",
			buf:  []byte("RIFX\x00\x00"),
			want: ErrTruncated,
		},
		{
			// A reader without a known length can't be checked up front, so
			// the short format ID is reported as truncated.
			name:        "truncated format id",
			buf:         []byte("RIFX\x00\x00\x00\x04WA"),
			want:        ErrTruncated,
			nonSeekable: true,
		},
		{
			name: "declared container larger than the file",
			buf:  []byte("RIFX\x00\x00\x01\x00WAVE"),
			want: ErrSizeOutOfBounds,
		},
		{
			name: "container size out of bounds",
			buf: func() []byte {
				b := append([]byte(nil), valid...)
				binary.BigEndian.PutUint32(b[4:8], 0x7ffffff0)
				return b
			}(),
			want: ErrSizeOutOfBounds,
		},
		{
			name: "chunk size out of bounds",
			buf: func() []byte {
				b := append([]byte(nil), valid...)
				binary.BigEndian.PutUint32(b[16:20], 0x00f00000)
				return b
			}(),
			want: ErrSizeOutOfBounds,
		},
		{
			name: "chunk header truncated",
			// The container ends in the middle of the data chunk header.
			buf: func() []byte {
				b := append([]byte(nil), valid[:40]...)
				binary.BigEndian.PutUint32(b[4:8], uint32(len(b)-8))
				return b
			}(),
			want: ErrTruncated,
		},
		{
			name: "container shorter than declared",
			buf:  valid[:len(valid)-4],
			want: ErrSizeOutOfBounds,
		},
		{
			name: "zero channels",
			buf: buildWav(true, WavFormatID,
				fixtureChunk{FmtID, buildFmt(true, 0, 44100, 16)},
				fixtureChunk{DataFormatID, make([]byte, 32)},
			),
			want: ErrInvalidChannels,
		},
		{
			name: "invalid bits per sample",
			buf: buildWav(true, WavFormatID,
				fixtureChunk{FmtID, buildFmt(true, 2, 44100, 12)},
				fixtureChunk{DataFormatID, make([]byte, 32)},
			),
			want: ErrInvalidBitsPerSample,
		},
		{
			name: "short fmt chunk",
			buf: buildWav(true, WavFormatID,
				fixtureChunk{FmtID, validFmt[:10]},
				fixtureChunk{DataFormatID, make([]byte, 32)},
			),
			want: ErrTruncated,
		},
		{
			name: "missing fmt",
			buf: buildWav(true, WavFormatID,
				fixtureChunk{DataFormatID, make([]byte, 32)},
			),
			want: ErrMissingFmt,
		},
		{
			name: "missing data",
			buf: buildWav(true, WavFormatID,
				fixtureChunk{FmtID, validFmt},
			),
			want: ErrMissingData,
		},
		{
			name: "huge chunk size",
			buf: func() []byte {
				// A RIFX container claiming to hold a 700MiB chunk while
				// carrying only a header. The declared sizes are consistent
				// with each other, so only the allocation limit can reject
				// it. A non seeking reader is used so the header bound check
				// can't see the real file length first.
				b := append([]byte(nil), valid[:12]...)
				const chunkSize = 0x50000000 // 1.25 GiB
				binary.BigEndian.PutUint32(b[4:8], 12+chunkSize)
				var hdr [8]byte
				copy(hdr[:4], []byte("JUNK"))
				binary.BigEndian.PutUint32(hdr[4:], chunkSize)
				b = append(b, hdr[:]...)
				return b
			}(),
			want: ErrChunkTooLarge,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var err error
			if test.nonSeekable || test.want == ErrChunkTooLarge {
				// Hide the reader length: the container size is consistent
				// with the chunk header, only the allocation limit or the
				// reads themselves can reject the chunk.
				_, err = parseChunksErrWith(&readerOnly{bytes.NewReader(test.buf)})
			} else {
				_, err = parseChunksErr(test.buf)
			}
			if err == nil {
				t.Fatal("expected an error")
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
			var perr *ParseError
			if !errors.As(err, &perr) {
				t.Fatalf("error %v doesn't carry a chunk position", err)
			}
			t.Logf("got: %v (chunk %q, offset %d)", err, perr.Chunk, perr.Offset)
		})
	}
}

// parseChunksErr runs ParseHeaders then Parse and returns the first error.
func parseChunksErr(buf []byte) (*Parser, error) {
	return parseChunksErrWith(bytes.NewReader(buf))
}

func parseChunksErrWith(r io.Reader) (*Parser, error) {
	p := New(r)
	if err := p.ParseHeaders(); err != nil {
		return p, err
	}
	return p, p.Parse()
}

// readerOnly hides the io.Seeker interface of the wrapped reader so the
// parser can't inspect the total length of the input.
type readerOnly struct {
	r io.Reader
}

func (r *readerOnly) Read(p []byte) (int, error) { return r.r.Read(p) }

func TestRifxMaxChunkSize(t *testing.T) {
	buf := buildWav(true, WavFormatID,
		fixtureChunk{FmtID, buildFmt(true, 1, 8000, 8)},
		fixtureChunk{DataFormatID, make([]byte, 64)},
	)
	p := New(bytes.NewReader(buf))
	if err := p.ParseHeaders(); err != nil {
		t.Fatal(err)
	}
	p.MaxChunkSize = 32
	// The fmt chunk is 16 bytes so it passes, the data chunk (64) doesn't.
	ch, err := p.NextChunk()
	if err != nil {
		t.Fatalf("fmt chunk should fit: %v", err)
	}
	if ch.DeclaredSize != 16 {
		t.Fatalf("fmt chunk declared %d bytes", ch.DeclaredSize)
	}
	ch.Done()
	_, err = p.NextChunk()
	if !errors.Is(err, ErrChunkTooLarge) {
		t.Fatalf("got %v, want ErrChunkTooLarge", err)
	}
	// The default limit accepts the data chunk.
	p2 := New(bytes.NewReader(buf))
	if err := p2.ParseHeaders(); err != nil {
		t.Fatal(err)
	}
	ch, err = p2.NextChunk()
	if err != nil {
		t.Fatal(err)
	}
	ch.Done()
	if _, err := p2.NextChunk(); err != nil {
		t.Fatalf("default limit rejected a small chunk: %v", err)
	}
}

func TestRifxDuration(t *testing.T) {
	leFile, err := os.Open("fixtures/pcm16_riff.wav")
	if err != nil {
		t.Fatal(err)
	}
	defer leFile.Close()
	beFile, err := os.Open("fixtures/pcm16_rifx.wav")
	if err != nil {
		t.Fatal(err)
	}
	defer beFile.Close()

	leDur, err := Duration(leFile)
	if err != nil {
		t.Fatal(err)
	}
	beDur, err := Duration(beFile)
	if err != nil {
		t.Fatal(err)
	}
	if leDur != beDur {
		t.Fatalf("durations differ: RIFF %s, RIFX %s", leDur, beDur)
	}
}

func TestRifxParseChannel(t *testing.T) {
	buf, err := ioutil.ReadFile("fixtures/pcm24_rifx.wav")
	if err != nil {
		t.Fatal(err)
	}
	p := New(bytes.NewReader(buf))
	if err := p.ParseHeaders(); err != nil {
		t.Fatal(err)
	}
	p.Chan = make(chan *Chunk, 4)
	var got []string
	done := make(chan struct{})
	go func() {
		for ch := range p.Chan {
			got = append(got, string(ch.ID[:]))
			ch.Done()
		}
		close(done)
	}()
	if err := p.Parse(); err != nil {
		t.Fatal(err)
	}
	<-done
	if len(got) != 2 || got[0] != "cue " || got[1] != "data" {
		t.Fatalf("unexpected chunks sent to the channel: %v", got)
	}
}

func TestRifxNonWavFormat(t *testing.T) {
	// A RIFX container of another sub format: the chunks are walked but the
	// fmt is not interpreted as WAVE.
	buf := buildWav(true, aviFormatID,
		fixtureChunk{FmtID, buildFmt(true, 2, 44100, 16)},
	)
	p, err := parseChunksErr(buf)
	if err != nil {
		t.Fatalf("non WAVE RIFX should parse its chunks: %v", err)
	}
	if p.Format != aviFormatID {
		t.Fatalf("Format = %q, want %q", p.Format, aviFormatID)
	}
}

// TestOddChunkPaddingSkipped checks that an odd sized chunk is followed by its
// alignment byte and that the next chunk header is read at the right place,
// whether the caller drained the chunk or consumed its content exactly.
func TestOddChunkPaddingSkipped(t *testing.T) {
	payload := []byte{1, 2, 3, 4, 5} // odd
	buf := buildWav(true, WavFormatID,
		fixtureChunk{[4]byte{'J', 'U', 'N', 'K'}, payload},
		fixtureChunk{FmtID, buildFmt(true, 1, 8000, 8)},
		fixtureChunk{DataFormatID, []byte{9}},
	)

	for _, consumeExactly := range []bool{false, true} {
		p := New(bytes.NewReader(buf))
		if err := p.ParseHeaders(); err != nil {
			t.Fatal(err)
		}
		junk, err := p.NextChunk()
		if err != nil {
			t.Fatal(err)
		}
		if junk.DeclaredSize != uint32(len(payload)) {
			t.Fatalf("JUNK declared %d bytes, want %d", junk.DeclaredSize, len(payload))
		}
		if consumeExactly {
			got := make([]byte, len(payload))
			if _, err := io.ReadFull(junk, got); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, payload) {
				t.Fatalf("JUNK content = %x, want %x", got, payload)
			}
			if junk.Pos != len(payload) {
				t.Fatalf("pos = %d after consuming the content exactly", junk.Pos)
			}
		} else {
			junk.Done()
		}

		next, err := p.NextChunk()
		if err != nil {
			t.Fatalf("consumeExactly=%v: %v", consumeExactly, err)
		}
		if next.ID != FmtID {
			t.Fatalf("consumeExactly=%v: next chunk is %q, want fmt", consumeExactly, next.ID)
		}
		next.Done()

		dataChunk, err := p.NextChunk()
		if err != nil {
			t.Fatal(err)
		}
		if dataChunk.ID != DataFormatID {
			t.Fatalf("next chunk is %q, want data", dataChunk.ID)
		}
		if _, err := ioutil.ReadAll(dataChunk); err != nil {
			t.Fatal(err)
		}
		if _, err := p.NextChunk(); err != io.EOF {
			t.Fatalf("expected io.EOF, got %v", err)
		}
	}
}

// TestPaddingByteNotExposed checks that the alignment byte is not reported as
// chunk content by the chunk reader.
func TestPaddingByteNotExposed(t *testing.T) {
	buf := buildWav(false, WavFormatID,
		fixtureChunk{DataFormatID, []byte{1, 2, 3}},
	)
	p := New(bytes.NewReader(buf))
	if err := p.ParseHeaders(); err != nil {
		t.Fatal(err)
	}
	ch, err := p.NextChunk()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ioutil.ReadAll(ch)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte{1, 2, 3}) {
		t.Fatalf("io.ReadAll = %x, want the 3 content bytes only", got)
	}
}

// TestRifxUnknownSize covers the 0xFFFFFFFF "size unknown" conventions used by
// streaming writers: the parser has to rely on the reader to find the end of
// the container instead of rejecting the file.
func TestRifxUnknownSize(t *testing.T) {
	// Container size unknown.
	buf := buildWav(true, WavFormatID,
		fixtureChunk{FmtID, buildFmt(true, 1, 8000, 8)},
		fixtureChunk{DataFormatID, []byte{1, 2, 3, 4}},
	)
	binary.BigEndian.PutUint32(buf[4:8], 0xFFFFFFFF)
	p, chunks, data := parseAll(t, &readerOnly{bytes.NewReader(buf)})
	if !p.BigEndian || p.Size != 0xFFFFFFFF {
		t.Fatalf("unexpected parser state: %+v", p)
	}
	if len(chunks) != 2 || len(data) != 1 || !bytes.Equal(data[0], []byte{1, 2, 3, 4}) {
		t.Fatalf("unexpected chunks or data")
	}

	// A 0xFFFFFFFF chunk size inside a container of known size would run
	// past that container: it is rejected.
	buf2 := buildWav(true, WavFormatID,
		fixtureChunk{FmtID, buildFmt(true, 1, 8000, 8)},
		fixtureChunk{DataFormatID, []byte{1, 2, 3, 4}},
	)
	binary.BigEndian.PutUint32(buf2[40:44], 0xFFFFFFFF)
	if _, err := parseChunksErr(buf2); err == nil {
		t.Fatal("a 0xFFFFFFFF chunk size inside a sized container should be rejected")
	}
}

func TestRifxReadHelpers(t *testing.T) {
	buf := buildWav(true, WavFormatID,
		fixtureChunk{FmtID, buildFmt(true, 2, 44100, 16)},
		fixtureChunk{DataFormatID, []byte{0x12, 0x34, 0x56, 0x78}},
	)
	p := New(bytes.NewReader(buf))
	if err := p.ParseHeaders(); err != nil {
		t.Fatal(err)
	}
	ch, err := p.NextChunk()
	if err != nil {
		t.Fatal(err)
	}
	ch.Done()

	data, err := p.NextChunk()
	if err != nil {
		t.Fatal(err)
	}

	// ReadBE and ReadLE are explicit: they ignore the container byte order.
	var be uint16
	if err := data.ReadBE(&be); err != nil {
		t.Fatal(err)
	}
	if be != 0x1234 {
		t.Fatalf("ReadBE = %#x, want 0x1234", be)
	}
	var le uint16
	if err := data.ReadLE(&le); err != nil {
		t.Fatal(err)
	}
	if le != 0x7856 {
		t.Fatalf("ReadLE = %#x, want 0x7856", le)
	}
	// The 4 content bytes are consumed: further reads hit EOF even though the
	// chunk padded size is even.
	var sample uint16
	if err := data.ReadSample(&sample); err != io.EOF {
		t.Fatalf("reading past the chunk = %v, want io.EOF", err)
	}
	if data.Pos != 4 {
		t.Fatalf("Pos = %d, want 4", data.Pos)
	}
	if _, err := data.ReadByte(); err != io.EOF {
		t.Fatalf("ReadByte past the chunk = %v, want io.EOF", err)
	}
}

func TestRifxIDnSize(t *testing.T) {
	p := New(bytes.NewReader([]byte("RIFX\x00\x00\x00\x2aWAVE")))
	id, size, err := p.IDnSize()
	if err != nil {
		t.Fatal(err)
	}
	if id != RifxID || size != 42 {
		t.Fatalf("IDnSize = %q, %d; want RIFX, 42", id, size)
	}
	// Chunk sizes are read big endian once the container is known.
	p = New(bytes.NewReader(buildWav(true, WavFormatID,
		fixtureChunk{FmtID, buildFmt(true, 1, 8000, 16)},
	)))
	if err := p.ParseHeaders(); err != nil {
		t.Fatal(err)
	}
	id, size, err = p.IDnSize()
	if err != nil {
		t.Fatal(err)
	}
	if id != FmtID || size != 16 {
		t.Fatalf("IDnSize = %q, %d; want fmt, 16", id, size)
	}
	// An empty reader is a clean EOF.
	p = New(bytes.NewReader(nil))
	if _, _, err := p.IDnSize(); err != io.EOF {
		t.Fatalf("empty reader = %v, want io.EOF", err)
	}
}

func TestRifxString(t *testing.T) {
	f, err := os.Open("fixtures/pcm16_rifx.wav")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	p := New(f)
	if err := p.ParseHeaders(); err != nil {
		t.Fatal(err)
	}
	if err := p.Parse(); err != nil {
		t.Fatal(err)
	}
	s := p.String()
	if !strings.Contains(s, "WAVE") || !strings.Contains(s, "2 channels @ 44100 / 16 bits") {
		t.Fatalf("unexpected String(): %q", s)
	}
	if !p.BigEndian {
		t.Fatal("expected a big endian parser")
	}
}

func TestParseErrorMessage(t *testing.T) {
	withChunk := &ParseError{Chunk: DataFormatID, Offset: 36, Err: ErrTruncated}
	if got, want := withChunk.Error(), `riff: chunk "data" at offset 36: truncated data`; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	container := &ParseError{Offset: 0, Err: ErrFmtNotSupported}
	if got, want := container.Error(), "riff: offset 0: format not supported"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	var nilErr *ParseError
	if got := nilErr.Error(); got != "<nil>" {
		t.Fatalf("nil Error() = %q", got)
	}
	if len(RifxID) != 4 || RifxID != [4]byte{'R', 'I', 'F', 'X'} {
		t.Fatalf("unexpected RifxID: %q", RifxID)
	}
}

// TestChunkBuiltByHand covers the helpers on a chunk assembled without the
// parser: its padded Size is the only length information available.
func TestChunkBuiltByHand(t *testing.T) {
	ch := &Chunk{ID: DataFormatID, Size: 4, R: bytes.NewReader([]byte{1, 2, 3, 4})}
	if ch.IsFullyRead() {
		t.Fatal("a chunk with unread content shouldn't be fully read")
	}
	if ch.contentSize() != 4 {
		t.Fatalf("contentSize = %d, want 4", ch.contentSize())
	}
	var v uint32
	if err := ch.ReadBE(&v); err != nil {
		t.Fatal(err)
	}
	if v != 0x01020304 {
		t.Fatalf("ReadBE = %#x", v)
	}
	if !ch.IsFullyRead() {
		t.Fatal("chunk should be fully read")
	}
	if _, err := ch.ReadByte(); err != io.EOF {
		t.Fatalf("ReadByte = %v, want io.EOF", err)
	}
}

func ExampleParser_rifx() {
	path, _ := filepath.Abs("fixtures/pcm16_rifx.wav")
	f, err := os.Open(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	c := New(f)
	if err := c.ParseHeaders(); err != nil {
		panic(err)
	}
	fmt.Printf("container %q, big endian: %v\n", c.ID, c.BigEndian)

	var chunk *Chunk
	for {
		chunk, err = c.NextChunk()
		if err == io.EOF {
			break
		}
		if err != nil {
			panic(err)
		}
		if chunk.ID == FmtID {
			if err := chunk.DecodeWavHeader(c); err != nil {
				panic(err)
			}
		}
		chunk.Done()
	}
	fmt.Printf("%d channels @ %d Hz, %d bits\n", c.NumChannels, c.SampleRate, c.BitsPerSample)

	// Output:
	// container "RIFX", big endian: true
	// 2 channels @ 44100 Hz, 16 bits
}
