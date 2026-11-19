# Resource Interchange File Format parser

[![GoDoc](http://godoc.org/github.com/go-audio/riff?status.svg)](http://godoc.org/go-audio/riff)

[![Build
Status](https://travis-ci.org/go-audio/riff.png)](https://travis-ci.org/go-audio/riff)

The [RIFF](https://en.wikipedia.org/wiki/Resource_Interchange_File_Format) container format is used to store chunks of data in media files, especially in wav files hence this package inside under the go-audio organization.

Both byte orders of the container are supported:

* `RIFF` containers store their sizes and numeric fields little endian.
* `RIFX` containers store the container size, the chunk sizes and the WAV fmt
  fields big endian. The audio data of a `data` chunk is kept as the original
  byte stream; use `Chunk.ReadSample` to decode sample values with the
  container byte order.

The byte order is detected from the container ID by `Parser.ParseHeaders`, and
`Parser.BigEndian` reports the result. `Parser` and `Chunk` are the same entry
points for both container kinds.

Errors carry the offending chunk ID and its absolute offset through
`*riff.ParseError`; use `errors.Is` with the exported sentinels such as
`riff.ErrTruncated`, `riff.ErrSizeOutOfBounds`, `riff.ErrMixedByteOrder` or
`riff.ErrChunkTooLarge`.

