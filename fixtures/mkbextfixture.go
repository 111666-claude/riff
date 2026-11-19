//go:build ignore
// +build ignore

// mkbextfixture regenerates fixtures/bext_odd.wav, a small BWF/WAVE file
// deliberately containing unknown chunks, an odd sized "data" chunk
// (with its word-alignment padding byte) and a populated bext chunk with
// trailing CodingHistory bytes. Run it from the repository root with:
//
//	go run fixtures/mkbextfixture.go
package main

import (
	"encoding/binary"
	"io/ioutil"
	"log"
)

type chunk struct {
	id      [4]byte
	payload []byte
}

func fourCC(s string) [4]byte {
	var id [4]byte
	copy(id[:], s)
	return id
}

func bextPayload() []byte {
	const fixed = 412
	history := []byte("A=PCM,F=44100,W=16,M=stereo,T=Test bext fixture\r\n")
	out := make([]byte, fixed+len(history))
	put := func(offset, size int, s string) {
		copy(out[offset:offset+size], s)
	}
	put(0, 256, "Location sound: take 1, boom mic, wind noise at 00:00:12")
	put(256, 32, "SoundDevices 633")
	put(288, 32, "SD633-SN-00042-2026-11-20")
	put(320, 10, "2026-11-20")
	put(330, 8, "09:30:15")
	// 64 bit time reference exercising values above 2^32.
	binary.LittleEndian.PutUint64(out[338:346], 5_000_000_000)
	binary.LittleEndian.PutUint16(out[346:348], 1)
	// UMID: basic SMPTE UMID (universal label 06 0A 2B 34 ...).
	copy(out[348:412], []byte{
		0x06, 0x0A, 0x2B, 0x34, 0x01, 0x01, 0x01, 0x01,
		0x01, 0x01, 0x02, 0x10,
	})
	copy(out[fixed:], history)
	return out
}

func fmtPayload() []byte {
	p := make([]byte, 16)
	binary.LittleEndian.PutUint16(p[0:2], 1)       // PCM
	binary.LittleEndian.PutUint16(p[2:4], 2)       // stereo
	binary.LittleEndian.PutUint32(p[4:8], 44100)   // sample rate
	binary.LittleEndian.PutUint32(p[8:12], 176400) // avg bytes/sec
	binary.LittleEndian.PutUint16(p[12:14], 4)     // block align
	binary.LittleEndian.PutUint16(p[14:16], 16)    // bits per sample
	return p
}

func main() {
	chunks := []chunk{
		// Odd sized unknown chunk, forces a padding byte.
		{fourCC("JUNK"), []byte{0xAA, 0xBB, 0xCC}},
		{fourCC("bext"), bextPayload()},
		{fourCC("fmt "), fmtPayload()},
		// Even sized unknown chunk between fmt and data.
		{fourCC("xyzA"), []byte{0xDE, 0xAD, 0xBE, 0xEF}},
		// Odd sized data chunk: 3 bytes + one zero padding byte.
		{fourCC("data"), []byte{0x01, 0x02, 0x03}},
		// Chunk following the padded data proves offsets stay correct.
		{fourCC("tail"), []byte{0x7F, 0x80}},
	}

	bodySize := 4 // WAVE fourCC
	for _, c := range chunks {
		bodySize += 8 + len(c.payload) + len(c.payload)%2
	}

	var out []byte
	hdr := make([]byte, 12)
	copy(hdr[0:4], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(bodySize))
	copy(hdr[8:12], "WAVE")
	out = append(out, hdr...)

	for _, c := range chunks {
		chHdr := make([]byte, 8)
		copy(chHdr[0:4], c.id[:])
		binary.LittleEndian.PutUint32(chHdr[4:8], uint32(len(c.payload)))
		out = append(out, chHdr...)
		out = append(out, c.payload...)
		if len(c.payload)%2 == 1 {
			out = append(out, 0)
		}
	}

	if err := ioutil.WriteFile("fixtures/bext_odd.wav", out, 0644); err != nil {
		log.Fatal(err)
	}
	log.Printf("wrote fixtures/bext_odd.wav (%d bytes, RIFF size %d)", len(out), bodySize)
}
