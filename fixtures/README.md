# Test fixtures

`sample.wav`, `junkKick.wav`, `sample.avi` and `sample.rmi` are the original
little endian RIFF fixtures of the package.

The `pcm16_*` and `pcm24_*` pairs were added for the RIFX (big endian
container) support. Each pair carries the same audio content and the same
chunk layout:

| Pair            | Channels | Sample rate | Bits | fmt chunk | Other chunks      |
|-----------------|----------|-------------|------|-----------|-------------------|
| `pcm16_*.wav`   | 2        | 44100       | 16   | 16 bytes  | `LIST` (unknown)  |
| `pcm24_*.wav`   | 1        | 44100       | 24   | 18 bytes  | `cue ` (odd size) |

* `*_riff.wav` is a plain `RIFF`/`WAVE` file: container and chunk sizes and the
  fmt fields are little endian, sample values are little endian.
* `*_rifx.wav` is the `RIFX`/`WAVE` twin: container and chunk sizes and the fmt
  fields are big endian, sample values are big endian (24 bit samples are
  stored most significant byte first).

The 24 bit pair has a 153 byte data chunk, so it also exercises the alignment
padding byte of odd sized chunks. Both files of a pair are byte identical
except for the byte order of the sizes, the fmt fields and the sample data.

They were generated with a small standalone Go program using
`encoding/binary` with the appropriate byte order; the test helper `buildWav`
in `rifx_test.go` builds containers the same way for the in-memory cases.
