/*
Package riff is package implementing a simple Resource Interchange File Format
(RIFF) parser with basic support for sub formats such as WAV.
The goal of this package is to give all the tools needed for a developer
to implement parse any kind of file formats using the RIFF container format.

Support for PCM wav format was added so the headers are parsed, the duration and the raw sound data
of a wav file can be easily accessed (See the examples below) .

Both RIFF (little endian) and RIFX (big endian) containers are supported. The
byte order is detected from the container ID: in a RIFX container the container
size, the chunk sizes and the WAV fmt fields are big endian. The sound data is
always exposed as the original byte stream; Chunk.ReadSample decodes sample
values using the container byte order.

For more information about RIFF:
https://en.wikipedia.org/wiki/Resource_Interchange_File_Format
*/
package riff
