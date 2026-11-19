package riff

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- helpers -------------------------------------------------------------

func mustReadFile(t *testing.T, path string) *File {
	t.Helper()
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rf, err := ReadFile(f)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	return rf
}

func chunkByID(f *File, id [4]byte) *RawChunk {
	for _, ch := range f.Chunks {
		if ch.ID == id {
			return ch
		}
	}
	return nil
}

func assertChunkError(t *testing.T, err error, wantID [4]byte, wantPos int64, substr string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error containing %q, got nil", substr)
	}
	ce, ok := err.(*ChunkError)
	if !ok {
		t.Fatalf("expected *ChunkError, got %T: %v", err, err)
	}
	if ce.ID != wantID {
		t.Fatalf("expected chunk ID %q in error, got %q (%v)", wantID, ce.ID, err)
	}
	if ce.Pos != wantPos {
		t.Fatalf("expected chunk position %d in error, got %d (%v)", wantPos, ce.Pos, err)
	}
	if !strings.Contains(ce.Error(), substr) {
		t.Fatalf("expected error %q to contain %q", ce.Error(), substr)
	}
}

// buildRIFF assembles an in-memory RIFF container from raw chunks.
// Chunks are (id, payload) pairs; odd payloads get a padding byte.
func buildRIFF(format [4]byte, pairs ...struct {
	id      [4]byte
	payload []byte
}) []byte {
	var buf bytes.Buffer
	type c struct {
		id      [4]byte
		payload []byte
	}
	chunks := make([]c, 0, len(pairs))
	for _, p := range pairs {
		chunks = append(chunks, c{p.id, p.payload})
	}
	total := int64(4)
	for _, ch := range chunks {
		total += 8 + int64(len(ch.payload)) + int64(len(ch.payload))%2
	}
	hdr := make([]byte, 12)
	copy(hdr[0:4], RiffID[:])
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(total))
	copy(hdr[8:12], format[:])
	buf.Write(hdr)
	for _, ch := range chunks {
		chHdr := make([]byte, 8)
		copy(chHdr[0:4], ch.id[:])
		binary.LittleEndian.PutUint32(chHdr[4:8], uint32(len(ch.payload)))
		buf.Write(chHdr)
		buf.Write(ch.payload)
		if len(ch.payload)%2 == 1 {
			buf.WriteByte(0)
		}
	}
	return buf.Bytes()
}

func pair(id string, payload []byte) struct {
	id      [4]byte
	payload []byte
} {
	var four [4]byte
	copy(four[:], id)
	return struct {
		id      [4]byte
		payload []byte
	}{four, payload}
}

func validBextPayload() []byte {
	b := &BextChunk{
		Description:         "desc",
		Originator:          "originator",
		OriginatorReference: "ref-42",
		OriginationDate:     "2026-11-20",
		OriginationTime:     "09:30:15",
		TimeReference:       123456789,
		Version:             1,
		Tail:                []byte("coding history"),
	}
	p, err := b.MarshalBinary()
	if err != nil {
		panic(err)
	}
	return p
}

// ---- bext field parsing --------------------------------------------------

func TestReadBextFixtureFields(t *testing.T) {
	f := mustReadFile(t, "fixtures/bext_odd.wav")

	if f.Format != WavFormatID {
		t.Fatalf("expected WAVE format, got %q", f.Format)
	}
	if f.Bext == nil {
		t.Fatal("expected a parsed bext chunk")
	}
	b := f.Bext
	if b.Description != "Location sound: take 1, boom mic, wind noise at 00:00:12" {
		t.Fatalf("unexpected description: %q", b.Description)
	}
	if b.Originator != "SoundDevices 633" {
		t.Fatalf("unexpected originator: %q", b.Originator)
	}
	if b.OriginatorReference != "SD633-SN-00042-2026-11-20" {
		t.Fatalf("unexpected originator reference: %q", b.OriginatorReference)
	}
	if b.OriginationDate != "2026-11-20" {
		t.Fatalf("unexpected date: %q", b.OriginationDate)
	}
	if b.OriginationTime != "09:30:15" {
		t.Fatalf("unexpected time: %q", b.OriginationTime)
	}
	if b.TimeReference != 5000000000 {
		t.Fatalf("unexpected time reference: %d", b.TimeReference)
	}
	if b.Version != 1 {
		t.Fatalf("unexpected version: %d", b.Version)
	}
	if b.UMID[0] != 0x06 || b.UMID[1] != 0x0A || b.UMID[3] != 0x34 {
		t.Fatalf("unexpected UMID prefix: % x", b.UMID[:12])
	}
	if string(b.Tail) != "A=PCM,F=44100,W=16,M=stereo,T=Test bext fixture\r\n" {
		t.Fatalf("unexpected tail bytes: %q", string(b.Tail))
	}
}

func TestReadJunkKickBext(t *testing.T) {
	f := mustReadFile(t, "fixtures/junkKick.wav")
	if f.Bext == nil {
		t.Fatal("junkKick.wav should contain a bext chunk")
	}
	b := f.Bext
	if b.Description != "" {
		t.Fatalf("expected empty description, got %q", b.Description)
	}
	if b.Originator != "Pro Tools" {
		t.Fatalf("unexpected originator: %q", b.Originator)
	}
	if b.OriginatorReference != "GIAQ64SmozoaaaGk" {
		t.Fatalf("unexpected originator reference: %q", b.OriginatorReference)
	}
	if b.OriginationDate != "2013-08-15" || b.OriginationTime != "10:24:54" {
		t.Fatalf("unexpected date/time: %q %q", b.OriginationDate, b.OriginationTime)
	}
	if b.TimeReference != 260067049 {
		t.Fatalf("unexpected time reference: %d", b.TimeReference)
	}
	if b.Version != 0 {
		t.Fatalf("unexpected version: %d", b.Version)
	}
	// 602 byte bext minus 412 fixed bytes = 190 preserved tail bytes.
	if len(b.Tail) != 602-BextFixedSize {
		t.Fatalf("expected %d tail bytes, got %d", 602-BextFixedSize, len(b.Tail))
	}
	// The original JUNK, minf, elm1 and data chunks must all be present.
	for _, id := range []string{"JUNK", "fmt ", "minf", "elm1", "data"} {
		var four [4]byte
		copy(four[:], id)
		if chunkByID(f, four) == nil {
			t.Fatalf("missing expected chunk %q", id)
		}
	}
}

// ---- round trip and chunk fidelity ---------------------------------------

func TestBinaryRoundTrip(t *testing.T) {
	original, err := ioutil.ReadFile(filepath.Clean("fixtures/bext_odd.wav"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := ReadFile(bytes.NewReader(original))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := f.Write(&out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), original) {
		t.Fatalf("round trip changed %d bytes: original=%d written=%d", diffBytes(original, out.Bytes()), len(original), out.Len())
	}

	// The written file must parse again with identical fields.
	f2, err := ReadFile(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if !bextEqual(f2.Bext, f.Bext) {
		t.Fatal("re-parsed bext differs from the originally parsed one")
	}
}

// bextEqual compares two bext chunks including their byte slices.
func bextEqual(a, b *BextChunk) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Description == b.Description &&
		a.Originator == b.Originator &&
		a.OriginatorReference == b.OriginatorReference &&
		a.OriginationDate == b.OriginationDate &&
		a.OriginationTime == b.OriginationTime &&
		a.TimeReference == b.TimeReference &&
		a.Version == b.Version &&
		a.UMID == b.UMID &&
		bytes.Equal(a.Tail, b.Tail)
}

func diffBytes(a, b []byte) int {
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	diff := 0
	for i := 0; i < n; i++ {
		if i >= len(a) || i >= len(b) || a[i] != b[i] {
			diff++
		}
	}
	return diff
}

func TestUntouchedChunksPreserved(t *testing.T) {
	original, err := ioutil.ReadFile(filepath.Clean("fixtures/bext_odd.wav"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := ReadFile(bytes.NewReader(original))
	if err != nil {
		t.Fatal(err)
	}
	// Only edit bext metadata; every other chunk must survive byte for byte.
	f.Bext.Description = "edited description"
	f.Bext.TimeReference = 42

	var out bytes.Buffer
	if err := f.Write(&out); err != nil {
		t.Fatal(err)
	}
	f2, err := ReadFile(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if len(f2.Chunks) != len(f.Chunks) {
		t.Fatalf("chunk count changed: %d -> %d", len(f.Chunks), len(f2.Chunks))
	}
	for i, before := range f.Chunks {
		after := f2.Chunks[i]
		if before.ID != after.ID {
			t.Fatalf("chunk %d ID changed: %q -> %q", i, before.ID, after.ID)
		}
		if before.ID == BextID {
			continue
		}
		if !bytes.Equal(before.Payload, after.Payload) {
			t.Fatalf("chunk %q payload was modified despite being untouched", before.ID)
		}
	}

	// Odd sized chunks must carry one zero padding byte and offsets stay
	// consistent: verify data payload length stays 3 and the file parses.
	data := f2.DataChunk()
	if data == nil || len(data.Payload) != 3 || !bytes.Equal(data.Payload, []byte{1, 2, 3}) {
		t.Fatalf("data chunk not preserved: %+v", data)
	}
}

func TestOddPaddingAndRiffSize(t *testing.T) {
	original, err := ioutil.ReadFile(filepath.Clean("fixtures/bext_odd.wav"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := ReadFile(bytes.NewReader(original))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := f.Write(&out); err != nil {
		t.Fatal(err)
	}
	written := out.Bytes()
	if int(binary.LittleEndian.Uint32(written[4:8])) != len(written)-8 {
		t.Fatal("RIFF size field does not match the actual container size")
	}
	// JUNK payload is 3 bytes: header at 12, payload at 20, padding at 23.
	if written[23] != 0 {
		t.Fatalf("expected zero padding byte at offset 23, got %#x", written[23])
	}
	// data header location: RIFF(12) + JUNK(12) + bext(8+461+1) ...
	// Just locate it and check the byte right after its 3 payload bytes.
	dataPos := indexChunk(written, DataFormatID)
	if dataPos < 0 {
		t.Fatal("data chunk not found")
	}
	dataSize := binary.LittleEndian.Uint32(written[dataPos+4:])
	if dataSize != 3 {
		t.Fatalf("expected data size 3, got %d", dataSize)
	}
	if written[dataPos+8+int(dataSize)] != 0 {
		t.Fatal("data chunk padding byte missing or non-zero")
	}
}

func indexChunk(b []byte, id [4]byte) int {
	for i := 12; i+8 <= len(b); {
		var cur [4]byte
		copy(cur[:], b[i:i+4])
		size := int(binary.LittleEndian.Uint32(b[i+4 : i+8]))
		if cur == id {
			return i
		}
		i += 8 + size + size%2
	}
	return -1
}

func TestEditBextAndTailPreserved(t *testing.T) {
	f := mustReadFile(t, "fixtures/bext_odd.wav")
	origTail := append([]byte(nil), f.Bext.Tail...)
	f.Bext.Description = "new desc"
	f.Bext.Originator = "new originator"
	f.Bext.OriginationDate = "2030-01-02"
	f.Bext.OriginationTime = "23:59:59"
	f.Bext.TimeReference = 1099511627776
	f.Bext.Version = 2
	f.Bext.UMID[0] = 0x42

	var out bytes.Buffer
	if err := f.Write(&out); err != nil {
		t.Fatal(err)
	}
	f2, err := ReadFile(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	b2 := f2.Bext
	if b2.Description != "new desc" || b2.Originator != "new originator" ||
		b2.OriginationDate != "2030-01-02" || b2.OriginationTime != "23:59:59" ||
		b2.TimeReference != 1<<40 || b2.Version != 2 || b2.UMID[0] != 0x42 {
		t.Fatalf("edited fields not persisted: %+v", b2)
	}
	if !bytes.Equal(b2.Tail, origTail) {
		t.Fatal("unknown bext tail bytes were not preserved")
	}
}

func TestRemoveAndInsertBext(t *testing.T) {
	f := mustReadFile(t, "fixtures/bext_odd.wav")
	before := len(f.Chunks)

	// Remove.
	f.SetBext(nil)
	var out bytes.Buffer
	if err := f.Write(&out); err != nil {
		t.Fatal(err)
	}
	f2, err := ReadFile(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if f2.Bext != nil || len(f2.Chunks) != before-1 {
		t.Fatalf("bext removal failed: chunks=%d", len(f2.Chunks))
	}
	// Re-insert: it should land before fmt and every other chunk stays ordered.
	f2.SetBext(&BextChunk{Description: "fresh", OriginationDate: "2026-11-20"})
	var out2 bytes.Buffer
	if err := f2.Write(&out2); err != nil {
		t.Fatal(err)
	}
	f3, err := ReadFile(bytes.NewReader(out2.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if f3.Bext == nil || f3.Bext.Description != "fresh" {
		t.Fatal("re-inserted bext not found")
	}
	// bext must be inserted before fmt while remaining after the leading JUNK.
	var fmtPos, bextPos, junkPos int = -1, -1, -1
	for i, ch := range f3.Chunks {
		switch ch.ID {
		case FmtID:
			fmtPos = i
		case BextID:
			bextPos = i
		case junkID:
			junkPos = i
		}
	}
	if bextPos < 0 || fmtPos < 0 || !(junkPos < bextPos && bextPos < fmtPos) {
		t.Fatalf("unexpected insertion order: junk=%d bext=%d fmt=%d", junkPos, bextPos, fmtPos)
	}
	// Order of all other chunks is unchanged.
	got := make([]string, 0, len(f3.Chunks))
	for _, ch := range f3.Chunks {
		if ch.ID != BextID {
			got = append(got, string(ch.ID[:]))
		}
	}
	want := []string{"JUNK", "fmt ", "xyzA", "data", "tail"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("other chunks reordered: %v", got)
	}
}

func TestInsertBextWithoutFmtAppendsAtEnd(t *testing.T) {
	// Container without a fmt chunk: a new bext must be appended last.
	raw := buildRIFF(WavFormatID,
		pair("data", []byte{1, 2, 3}),
		pair("tail", []byte{9, 9}),
	)
	f, err := ReadFile(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	f.SetBext(&BextChunk{Description: "added", OriginationDate: "2026-11-20"})
	var out bytes.Buffer
	if err := f.Write(&out); err != nil {
		t.Fatal(err)
	}
	f2, err := ReadFile(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	last := f2.Chunks[len(f2.Chunks)-1]
	if last.ID != BextID || f2.Bext == nil || f2.Bext.Description != "added" {
		t.Fatalf("bext not appended at end: last=%q bext=%+v", last.ID, f2.Bext)
	}
	// The odd sized data chunk keeps its padding and parses cleanly.
	if d := f2.DataChunk(); d == nil || !bytes.Equal(d.Payload, []byte{1, 2, 3}) {
		t.Fatal("data chunk lost during bext insertion")
	}

	// Empty container: inserting bext alone must still produce valid RIFF.
	empty, err := ReadFile(bytes.NewReader(buildRIFF(WavFormatID)))
	if err != nil {
		t.Fatal(err)
	}
	empty.SetBext(&BextChunk{Version: 1})
	var out2 bytes.Buffer
	if err := empty.Write(&out2); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(bytes.NewReader(out2.Bytes())); err != nil {
		t.Fatalf("single-bext container invalid: %v", err)
	}
}

// ---- error handling ------------------------------------------------------

func TestDuplicateBext(t *testing.T) {
	payload := validBextPayload()
	// RIFF: fmt, bext(at some offset), bext(duplicate).
	raw := buildRIFF(WavFormatID,
		pair("fmt ", make([]byte, 16)),
		pair("bext", payload),
		pair("bext", payload),
	)
	// First bext sits after RIFF(12) + fmt chunk (8+16).
	dupPos := int64(12 + 24 + 8 + len(payload))
	_, err := ReadFile(bytes.NewReader(raw))
	assertChunkError(t, err, BextID, dupPos, "duplicate bext")
}

func TestTruncatedBextFixedFields(t *testing.T) {
	// Payload shorter than the 412 fixed bytes.
	raw := buildRIFF(WavFormatID, pair("bext", make([]byte, 100)))
	_, err := ReadFile(bytes.NewReader(raw))
	assertChunkError(t, err, BextID, 12, "truncated bext")
}

func TestTruncatedChunkHeader(t *testing.T) {
	// RIFF header + WAVE + 5 stray bytes instead of an 8 byte chunk header.
	raw := buildRIFF(WavFormatID)
	raw = append(raw, 1, 2, 3, 4, 5)
	_, err := ReadFile(bytes.NewReader(raw))
	assertChunkError(t, err, [4]byte{}, 12, "truncated chunk header")
}

func TestTruncatedPayloadAndMissingPad(t *testing.T) {
	// Chunk header declaring 10 bytes but only 3 available.
	var buf bytes.Buffer
	hdr := make([]byte, 12)
	copy(hdr[0:4], RiffID[:])
	binary.LittleEndian.PutUint32(hdr[4:8], 4+8+10)
	copy(hdr[8:12], WavFormatID[:])
	buf.Write(hdr)
	ch := make([]byte, 8)
	copy(ch[0:4], "data")
	binary.LittleEndian.PutUint32(ch[4:8], 10)
	buf.Write(ch)
	buf.Write([]byte{1, 2, 3})
	_, err := ReadFile(bytes.NewReader(buf.Bytes()))
	assertChunkError(t, err, DataFormatID, 12, "truncated chunk payload")

	// Odd sized chunk without its padding byte.
	raw := buildRIFF(WavFormatID, pair("data", []byte{1, 2, 3}))
	_, err = ReadFile(bytes.NewReader(raw[:len(raw)-1]))
	assertChunkError(t, err, DataFormatID, int64(indexChunk(raw, DataFormatID)), "missing padding byte")
}

func TestInvalidDateAndTime(t *testing.T) {
	// Craft on-disk payloads directly: invalid values must be rejected on
	// read even though MarshalBinary refuses to produce them on write.
	mk := func(date, clock string) []byte {
		p := make([]byte, BextFixedSize)
		copy(p[320:330], date)
		copy(p[330:338], clock)
		return buildRIFF(WavFormatID, pair("bext", p))
	}
	for _, tc := range []struct {
		date, clock, sub string
	}{
		{"2026-13-40", "09:30:15", "invalid bext origination date"},
		{"2026-02-30", "09:30:15", "invalid bext origination date"},
		{"not-a-date", "09:30:15", "invalid bext origination date"},
		{"2026-11-20", "25:00:00", "invalid bext origination time"},
		{"2026-11-20", "99:99:99", "invalid bext origination time"},
		{"2026-11-20", "12:61:00", "invalid bext origination time"},
	} {
		_, err := ReadFile(bytes.NewReader(mk(tc.date, tc.clock)))
		assertChunkError(t, err, BextID, 12, tc.sub)
	}

	// Empty date/time fields must be accepted.
	raw := buildRIFF(WavFormatID, pair("bext", mustMarshal(t, &BextChunk{})))
	if _, err := ReadFile(bytes.NewReader(raw)); err != nil {
		t.Fatalf("empty date/time rejected: %v", err)
	}
}

func mustMarshal(t *testing.T, b *BextChunk) []byte {
	t.Helper()
	p, err := b.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOverlongTextRejected(t *testing.T) {
	long := strings.Repeat("x", bextDescriptionSize+1)
	b := &BextChunk{Description: long}
	_, err := b.MarshalBinary()
	if err == nil || !strings.Contains(err.Error(), "description too long") {
		t.Fatalf("expected description too long error, got %v", err)
	}

	// Writing through File must surface the validation failure with the
	// bext chunk position.
	f := mustReadFile(t, "fixtures/bext_odd.wav")
	bextPos := f.bext.Pos
	f.Bext.Description = long
	var buf bytes.Buffer
	err = f.Write(&buf)
	assertChunkError(t, err, BextID, bextPos, "too long")

	// Every other fixed field gets the same treatment.
	for _, tc := range []struct {
		set func(*BextChunk)
		sub string
	}{
		{func(b *BextChunk) { b.Originator = strings.Repeat("x", 33) }, "originator too long"},
		{func(b *BextChunk) { b.OriginatorReference = strings.Repeat("x", 33) }, "originator reference too long"},
		{func(b *BextChunk) { b.OriginationDate = strings.Repeat("x", 11) }, "origination date too long"},
		{func(b *BextChunk) { b.OriginationTime = strings.Repeat("x", 9) }, "origination time too long"},
	} {
		b := &BextChunk{}
		tc.set(b)
		if _, err := b.MarshalBinary(); err == nil || !strings.Contains(err.Error(), tc.sub) {
			t.Fatalf("expected %q error, got %v", tc.sub, err)
		}
	}
}

func TestSizeOverflow(t *testing.T) {
	// Read path: a chunk declaring more bytes than the container advertises
	// must fail before any large allocation, with the chunk position attached.
	var buf bytes.Buffer
	hdr := make([]byte, 12)
	copy(hdr[0:4], RiffID[:])
	binary.LittleEndian.PutUint32(hdr[4:8], 20) // RIFF claims only 20 bytes follow
	copy(hdr[8:12], WavFormatID[:])
	buf.Write(hdr)
	ch := make([]byte, 8)
	copy(ch[0:4], "huge")
	binary.LittleEndian.PutUint32(ch[4:8], 1000000)
	buf.Write(ch)
	var huge [4]byte
	copy(huge[:], "huge")
	_, err := ReadFile(bytes.NewReader(buf.Bytes()))
	assertChunkError(t, err, huge, 12, "exceeds the RIFF container")

	// A near-max uint32 chunk inside a near-max RIFF still can't exceed the
	// advertised bounds either.
	var buf2 bytes.Buffer
	hdr2 := make([]byte, 12)
	copy(hdr2[0:4], RiffID[:])
	binary.LittleEndian.PutUint32(hdr2[4:8], 100)
	copy(hdr2[8:12], WavFormatID[:])
	buf2.Write(hdr2)
	ch2 := make([]byte, 8)
	copy(ch2[0:4], "big2")
	binary.LittleEndian.PutUint32(ch2[4:8], ^uint32(0))
	buf2.Write(ch2)
	var big2 [4]byte
	copy(big2[:], "big2")
	_, err = ReadFile(bytes.NewReader(buf2.Bytes()))
	assertChunkError(t, err, big2, 12, "exceeds the RIFF container")

	// Write path arithmetic: near-limit sizes must trip the 32 bit guard
	// without allocating multi-gigabyte payloads.
	const max32 = int64(^uint32(0))
	if _, err := accumulateContainerSize(max32-10, 100); err == nil {
		t.Fatal("expected container size overflow error")
	}
	if _, err := accumulateContainerSize(4, max32); err == nil {
		t.Fatal("expected overflow for max uint32 payload")
	}
	if v, err := accumulateContainerSize(4, 100); err != nil || v != 4+8+100 {
		t.Fatalf("unexpected accumulation: v=%d err=%v", v, err)
	}
}

// callFailWriter fails on the Nth Write call (0 based), independently of
// how many bytes each call carries.
type callFailWriter struct{ failOnCall int }

func (w *callFailWriter) Write(p []byte) (int, error) {
	if w.failOnCall == 0 {
		return 0, errors.New("disk full")
	}
	w.failOnCall--
	return len(p), nil
}

func TestWriteFailureCarriesChunkPosition(t *testing.T) {
	f := mustReadFile(t, "fixtures/bext_odd.wav")
	// Write emits one Write call per chunk header and one per payload.
	// Fail inside the payload Write of the requested target chunk and check
	// the error names that chunk and its source position.
	for _, target := range []struct {
		id [4]byte
	}{
		{BextID},
		{DataFormatID},
	} {
		ch := chunkByID(f, target.id)
		if ch == nil {
			t.Fatalf("missing %q chunk in fixture", target.id)
		}
		// Calls before this chunk: RIFF header (1) + two Writes per
		// preceding chunk (header, payload; padding is a third when odd).
		call := 1
		for _, earlier := range f.Chunks {
			if earlier == ch {
				break
			}
			call++ // header
			call++ // payload
			if len(earlier.Payload)%2 == 1 {
				call++ // padding byte
			}
		}
		w := &callFailWriter{failOnCall: call} // next call is the payload
		err := f.Write(w)
		if err == nil {
			t.Fatalf("expected a write failure in chunk %q", target.id)
		}
		ce, ok := err.(*ChunkError)
		if !ok {
			t.Fatalf("expected *ChunkError for %q, got %T: %v", target.id, err, err)
		}
		if ce.ID != target.id {
			t.Fatalf("expected %q chunk in error, got %q", target.id, ce.ID)
		}
		if ce.Pos != ch.Pos {
			t.Fatalf("expected chunk position %d, got %d", ch.Pos, ce.Pos)
		}
	}

	// A failure while emitting the RIFF header itself is a plain I/O error.
	headerErr := f.Write(&callFailWriter{failOnCall: 0})
	if headerErr == nil {
		t.Fatal("expected a RIFF header write failure")
	}
	if _, ok := headerErr.(*ChunkError); ok {
		t.Fatalf("RIFF header failure must not be attributed to a chunk: %v", headerErr)
	}
}

func TestNonRIFFRejected(t *testing.T) {
	raw := buildRIFF([4]byte{'R', 'M', 'I', 'D'})
	copy(raw[0:4], "RIFX")
	if _, err := ReadFile(bytes.NewReader(raw)); err == nil {
		t.Fatal("expected non-RIFF container to be rejected")
	}
}

// ---- streaming parser regression -----------------------------------------

func TestStreamingParserStillSkipsBext(t *testing.T) {
	// The original streaming Parser must keep draining bext/unknown chunks
	// without choking on the new fixture (odd padding included).
	path, _ := filepath.Abs("fixtures/bext_odd.wav")
	fh, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	p := New(fh)
	if err := p.ParseHeaders(); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for {
		ch, err := p.NextChunk()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, string(ch.ID[:]))
		ch.Done()
	}
	want := []string{"JUNK", "bext", "fmt ", "xyzA", "data", "tail"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("streaming chunk order = %v, want %v", ids, want)
	}
}
