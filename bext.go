package riff

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
)

// BextID is the ID of the Broadcast Audio Extension chunk defined by the
// EBU Tech 3285 Broadcast Wave Format (BWF) specification.
var BextID = [4]byte{'b', 'e', 'x', 't'}

// Fixed field sizes of the bext chunk as defined by EBU Tech 3285.
// The fixed part of the chunk is 412 bytes long; any trailing bytes
// (historically reserved space, CodingHistory in BWF v2 and later
// extensions such as UMID extension or loudness metadata) are kept
// verbatim in BextChunk.Tail.
const (
	bextDescriptionSize         = 256
	bextOriginatorSize          = 32
	bextOriginatorReferenceSize = 32
	bextOriginationDateSize     = 10
	bextOriginationTimeSize     = 8
	bextTimeReferenceSize       = 8
	bextVersionSize             = 2
	bextUMIDSize                = 64

	// BextFixedSize is the size in bytes of the fixed bext fields.
	BextFixedSize = bextDescriptionSize + bextOriginatorSize +
		bextOriginatorReferenceSize + bextOriginationDateSize +
		bextOriginationTimeSize + bextTimeReferenceSize +
		bextVersionSize + bextUMIDSize
)

// ChunkError wraps an error encountered while reading or writing a chunk.
// Pos is the byte offset of the chunk header inside the RIFF container
// (the RIFF header itself lives at offset 0, the first sub chunk usually
// starts at offset 12). When the chunk can't be identified yet (e.g. a
// truncated chunk header), ID is zero and Pos points where the header
// was expected.
type ChunkError struct {
	ID     [4]byte
	Pos    int64
	Reason string
	Err    error
}

func (e *ChunkError) Error() string {
	if e.ID == [4]byte{} {
		return fmt.Sprintf("riff: chunk at offset %d: %s", e.Pos, e.reasonOrErr())
	}
	return fmt.Sprintf("riff: chunk %q at offset %d: %s", e.ID, e.Pos, e.reasonOrErr())
}

func (e *ChunkError) reasonOrErr() string {
	if e.Reason != "" {
		return e.Reason
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return "unknown error"
}

// Unwrap returns the wrapped error so errors.Is and errors.As can reach
// the underlying cause.
func (e *ChunkError) Unwrap() error { return e.Err }

// BextChunk holds the content of a BWF "bext" chunk.
//
// The string fields map onto fixed size, NUL padded fields: Description
// (256 bytes), Originator (32 bytes), OriginatorReference (32 bytes),
// OriginationDate (10 bytes, "YYYY-MM-DD") and OriginationTime
// (8 bytes, "HH:MM:SS"). TimeReference is a 64 bit unsigned little endian
// value, Version a 16 bit unsigned value and UMID a 64 byte field.
//
// Tail preserves every byte following the 412 fixed bytes (CodingHistory,
// reserved/extension bytes) untouched so unknown data survives a round
// trip even if this package doesn't interpret it.
type BextChunk struct {
	Description         string
	Originator          string
	OriginatorReference string
	OriginationDate     string
	OriginationTime     string
	TimeReference       uint64
	Version             uint16
	UMID                [64]byte
	// Tail holds the raw bytes beyond the 412 fixed fields.
	Tail []byte
}

// ParseBext decodes a bext chunk payload. chunkPos is the offset of the
// chunk header inside the RIFF container and is reported back on errors.
// The payload must contain at least the 412 fixed bytes.
func ParseBext(payload []byte, chunkPos int64) (*BextChunk, error) {
	if len(payload) < BextFixedSize {
		return nil, &ChunkError{
			ID:     BextID,
			Pos:    chunkPos,
			Reason: fmt.Sprintf("truncated bext chunk: got %d bytes, need at least %d fixed bytes", len(payload), BextFixedSize),
		}
	}
	b := &BextChunk{
		Description:         cString(payload[0:256]),
		Originator:          cString(payload[256:288]),
		OriginatorReference: cString(payload[288:320]),
		OriginationDate:     cString(payload[320:330]),
		OriginationTime:     cString(payload[330:338]),
		TimeReference:       binary.LittleEndian.Uint64(payload[338:346]),
		Version:             binary.LittleEndian.Uint16(payload[346:348]),
		Tail:                append([]byte(nil), payload[BextFixedSize:]...),
	}
	copy(b.UMID[:], payload[348:412])

	if b.OriginationDate != "" {
		if _, err := time.Parse("2006-01-02", b.OriginationDate); err != nil {
			return nil, &ChunkError{
				ID:     BextID,
				Pos:    chunkPos,
				Reason: fmt.Sprintf("invalid bext origination date %q: expected YYYY-MM-DD", b.OriginationDate),
				Err:    err,
			}
		}
	}
	if b.OriginationTime != "" {
		if _, err := time.Parse("15:04:05", b.OriginationTime); err != nil {
			return nil, &ChunkError{
				ID:     BextID,
				Pos:    chunkPos,
				Reason: fmt.Sprintf("invalid bext origination time %q: expected HH:MM:SS", b.OriginationTime),
				Err:    err,
			}
		}
	}
	return b, nil
}

// MarshalBinary serializes the bext chunk into its on-disk payload
// (without the chunk header). Fixed fields are validated first so that
// overlong text or an invalid date/time can never silently truncate
// metadata.
func (b *BextChunk) MarshalBinary() ([]byte, error) {
	if b == nil {
		return nil, errors.New("riff: can't marshal a nil bext chunk")
	}
	if err := b.Validate(); err != nil {
		return nil, err
	}
	out := make([]byte, BextFixedSize+len(b.Tail))
	putFixedString(out[0:256], b.Description)
	putFixedString(out[256:288], b.Originator)
	putFixedString(out[288:320], b.OriginatorReference)
	putFixedString(out[320:330], b.OriginationDate)
	putFixedString(out[330:338], b.OriginationTime)
	binary.LittleEndian.PutUint64(out[338:346], b.TimeReference)
	binary.LittleEndian.PutUint16(out[346:348], b.Version)
	copy(out[348:412], b.UMID[:])
	copy(out[412:], b.Tail)
	return out, nil
}

// Validate checks that every text field fits its fixed field and that
// the date/time (when present) follow the BWF formats.
func (b *BextChunk) Validate() error {
	checks := []struct {
		name  string
		value string
		max   int
	}{
		{"description", b.Description, bextDescriptionSize},
		{"originator", b.Originator, bextOriginatorSize},
		{"originator reference", b.OriginatorReference, bextOriginatorReferenceSize},
		{"origination date", b.OriginationDate, bextOriginationDateSize},
		{"origination time", b.OriginationTime, bextOriginationTimeSize},
	}
	for _, c := range checks {
		if len(c.value) > c.max {
			return fmt.Errorf("riff: bext %s too long: %d bytes exceed the %d byte field", c.name, len(c.value), c.max)
		}
	}
	if b.OriginationDate != "" {
		if _, err := time.Parse("2006-01-02", b.OriginationDate); err != nil {
			return fmt.Errorf("riff: invalid bext origination date %q: expected YYYY-MM-DD", b.OriginationDate)
		}
	}
	if b.OriginationTime != "" {
		if _, err := time.Parse("15:04:05", b.OriginationTime); err != nil {
			return fmt.Errorf("riff: invalid bext origination time %q: expected HH:MM:SS", b.OriginationTime)
		}
	}
	return nil
}

// cString converts a NUL padded fixed length field into a Go string by
// dropping everything from the first NUL byte.
func cString(field []byte) string {
	if i := bytes.IndexByte(field, 0); i >= 0 {
		field = field[:i]
	}
	return string(field)
}

// putFixedString copies s into the fixed size dst field, zero padding
// the remainder. The caller is responsible for validating the length.
func putFixedString(dst []byte, s string) {
	for i := range dst {
		dst[i] = 0
	}
	copy(dst, s)
}

// RawChunk is an in-memory copy of a RIFF sub chunk: its ID, the header
// position in the source container and its exact payload.
type RawChunk struct {
	ID      [4]byte
	Pos     int64
	Payload []byte
}

// File is a fully buffered RIFF container. The whole container is read
// into memory so its chunks can be inspected, edited and written back
// while preserving the order and exact bytes of every untouched chunk.
type File struct {
	ID     [4]byte
	Size   uint32
	Format [4]byte
	Chunks []*RawChunk
	// Bext points at the parsed bext chunk, if the container has one.
	// It references the chunk stored in Chunks; edit it in place and
	// write the File back to update the metadata.
	Bext *BextChunk
	bext *RawChunk
}

// ReadFile loads an entire RIFF container from r and, when present,
// parses its bext chunk. All other chunks are buffered as raw bytes.
func ReadFile(r io.Reader) (*File, error) {
	f := &File{}

	var header [8]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, fmt.Errorf("riff: failed to read RIFF header: %v", err)
	}
	copy(f.ID[:], header[0:4])
	f.Size = binary.LittleEndian.Uint32(header[4:8])
	if f.ID != RiffID {
		return nil, fmt.Errorf("%s - %s", f.ID, ErrFmtNotSupported)
	}
	if _, err := io.ReadFull(r, f.Format[:]); err != nil {
		return nil, fmt.Errorf("riff: failed to read RIFF format: %v", err)
	}

	var pos int64 = 12
	for {
		var chunkHeader [8]byte
		n, err := io.ReadFull(r, chunkHeader[:])
		if err == io.EOF {
			break
		}
		if err == io.ErrUnexpectedEOF {
			return nil, &ChunkError{Pos: pos, Reason: fmt.Sprintf("truncated chunk header: got %d of 8 bytes", n), Err: err}
		}
		if err != nil {
			return nil, &ChunkError{Pos: pos, Err: err}
		}

		ch := &RawChunk{Pos: pos}
		copy(ch.ID[:], chunkHeader[0:4])
		declared := int64(binary.LittleEndian.Uint32(chunkHeader[4:8]))
		if declared > int64(maxInt()) {
			return nil, &ChunkError{
				ID:     ch.ID,
				Pos:    pos,
				Reason: fmt.Sprintf("chunk size %d overflows address space", declared),
			}
		}
		// The chunk (header, payload and possible padding) must fit inside
		// the bounds advertised by the RIFF size field.
		remaining := int64(f.Size) + 8 - pos
		footprint := 8 + declared + declared%2
		if footprint > remaining {
			return nil, &ChunkError{
				ID:  ch.ID,
				Pos: pos,
				Reason: fmt.Sprintf("chunk size %d exceeds the RIFF container: needs %d bytes but only %d remain",
					declared, footprint, remaining),
			}
		}
		ch.Payload = make([]byte, declared)
		if declared > 0 {
			if _, err := io.ReadFull(r, ch.Payload); err != nil {
				return nil, &ChunkError{
					ID:     ch.ID,
					Pos:    pos,
					Reason: "truncated chunk payload",
					Err:    err,
				}
			}
		}
		// RIFF chunks are word aligned: an odd payload is followed by
		// one padding byte which is not part of the declared size.
		if declared%2 == 1 {
			var pad [1]byte
			if _, err := io.ReadFull(r, pad[:]); err != nil {
				return nil, &ChunkError{
					ID:     ch.ID,
					Pos:    pos,
					Reason: "missing padding byte after odd-sized chunk",
					Err:    err,
				}
			}
		}

		if ch.ID == BextID {
			if f.bext != nil {
				return nil, &ChunkError{
					ID:     BextID,
					Pos:    pos,
					Reason: fmt.Sprintf("duplicate bext chunk, first one found at offset %d", f.bext.Pos),
				}
			}
			bext, err := ParseBext(ch.Payload, pos)
			if err != nil {
				return nil, err
			}
			f.Bext = bext
			f.bext = ch
		}
		f.Chunks = append(f.Chunks, ch)
		pos += 8 + declared + declared%2
	}
	return f, nil
}

// DataChunk returns the raw "data" chunk or nil if the container has
// none.
func (f *File) DataChunk() *RawChunk {
	for _, ch := range f.Chunks {
		if ch.ID == DataFormatID {
			return ch
		}
	}
	return nil
}

// SetBext sets the bext metadata to write back. Passing nil removes an
// existing bext chunk on the next Write. When no bext existed in the
// source, a new chunk is inserted right before the first "fmt " chunk
// (or at the end when no fmt chunk exists), matching the usual BWF layout.
func (f *File) SetBext(b *BextChunk) {
	f.Bext = b
}

// Write serializes the container to w as a complete RIFF file. The
// chunk order and payloads of untouched chunks are preserved; the bext
// chunk (if any) is re-serialized from Bext and every odd sized chunk
// gets its zero padding byte.
func (f *File) Write(w io.Writer) error {
	// A brand new bext chunk (no bext existed in the source) is inserted
	// right before the first "fmt " chunk, else at the end.
	insertAt := len(f.Chunks)
	if f.Bext != nil && f.bext == nil {
		for i, ch := range f.Chunks {
			if ch.ID == FmtID {
				insertAt = i
				break
			}
		}
	}

	preparedChunks := make([]preparedChunk, 0, len(f.Chunks)+1)
	appendChunk := func(id [4]byte, pos int64, payload []byte) error {
		if int64(len(payload)) > int64(^uint32(0)) {
			return &ChunkError{
				ID:     id,
				Pos:    pos,
				Reason: fmt.Sprintf("chunk size %d overflows the 32 bit RIFF size field", len(payload)),
			}
		}
		preparedChunks = append(preparedChunks, preparedChunk{id: id, pos: pos, payload: payload})
		return nil
	}

	for i, ch := range f.Chunks {
		// A nil Bext means a previously present bext gets dropped.
		if f.Bext == nil && ch == f.bext {
			continue
		}
		if f.Bext != nil && f.bext == nil && i == insertAt {
			payload, err := f.Bext.MarshalBinary()
			if err != nil {
				return &ChunkError{ID: BextID, Pos: -1, Err: err}
			}
			if err := appendChunk(BextID, -1, payload); err != nil {
				return err
			}
		}
		payload := ch.Payload
		if ch == f.bext && f.Bext != nil {
			var err error
			if payload, err = f.Bext.MarshalBinary(); err != nil {
				return &ChunkError{ID: BextID, Pos: ch.Pos, Err: err}
			}
		}
		if err := appendChunk(ch.ID, ch.Pos, payload); err != nil {
			return err
		}
	}
	// No fmt chunk (or empty container): append the new bext at the end.
	if f.Bext != nil && f.bext == nil && insertAt == len(f.Chunks) {
		payload, err := f.Bext.MarshalBinary()
		if err != nil {
			return &ChunkError{ID: BextID, Pos: -1, Err: err}
		}
		if err := appendChunk(BextID, -1, payload); err != nil {
			return err
		}
	}

	total, err := riffContainerSize(preparedChunks)
	if err != nil {
		return err
	}

	var header [12]byte
	copy(header[0:4], RiffID[:])
	binary.LittleEndian.PutUint32(header[4:8], uint32(total))
	copy(header[8:12], f.Format[:])
	if _, err := w.Write(header[:]); err != nil {
		return fmt.Errorf("riff: failed to write RIFF header: %v", err)
	}

	for _, pc := range preparedChunks {
		var chunkHeader [8]byte
		copy(chunkHeader[0:4], pc.id[:])
		binary.LittleEndian.PutUint32(chunkHeader[4:8], uint32(len(pc.payload)))
		if _, err := w.Write(chunkHeader[:]); err != nil {
			return &ChunkError{ID: pc.id, Pos: pc.pos, Reason: "failed to write chunk header", Err: err}
		}
		if len(pc.payload) > 0 {
			if _, err := w.Write(pc.payload); err != nil {
				return &ChunkError{ID: pc.id, Pos: pc.pos, Reason: "failed to write chunk payload", Err: err}
			}
		}
		if len(pc.payload)%2 == 1 {
			if _, err := w.Write([]byte{0}); err != nil {
				return &ChunkError{ID: pc.id, Pos: pc.pos, Reason: "failed to write padding byte", Err: err}
			}
		}
	}
	return nil
}

// preparedChunk is a chunk about to be serialized, carrying its source
// position for error reporting.
type preparedChunk struct {
	id      [4]byte
	pos     int64
	payload []byte
}

// riffContainerSize computes the value of the RIFF size field: the
// 4 byte format fourCC plus every chunk header (8 bytes), payload and
// padding byte for odd sized payloads. It also guards against the
// 32 bit size field overflowing.
func riffContainerSize(chunks []preparedChunk) (int64, error) {
	var total int64 = 4 // format fourCC following the RIFF header
	for _, pc := range chunks {
		var err error
		if total, err = accumulateContainerSize(total, int64(len(pc.payload))); err != nil {
			return 0, err
		}
	}
	return total, nil
}

// accumulateContainerSize adds the on-disk footprint of one chunk
// (8 byte header + payload + optional padding byte) to total and
// reports an error when the 32 bit RIFF size field would overflow.
func accumulateContainerSize(total, payloadLen int64) (int64, error) {
	add := 8 + payloadLen + payloadLen%2
	if payloadLen < 0 || add < 0 || total+add > int64(^uint32(0)) {
		return 0, fmt.Errorf("riff: container size overflows the 32 bit RIFF size field (payload length %d)", payloadLen)
	}
	return total + add, nil
}

// maxInt returns the maximum value an int can hold on the current
// architecture, used as a sanity bound before allocating a buffer for
// an attacker controlled chunk size.
func maxInt() int {
	return int(^uint(0) >> 1)
}
