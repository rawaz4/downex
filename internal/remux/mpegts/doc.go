// Package mpegts demultiplexes MPEG-2 transport streams (ISO/IEC 13818-1, ITU-T
// H.222.0) into PES units: 188-byte packets with resynchronisation, continuity
// counters, scrambling detection, program-specific information (PAT and PMT
// sections with CRC-32 and versions) and PES packets with their 33-bit PTS and DTS.
//
// It is Downex's own implementation, written from the standard and RFC 8216 §3.2
// (HLS transport stream segments): no third-party demuxer is used. All input is
// untrusted. Memory is bounded (one PES unit of at most MaxPESBytes per kept
// stream, sections of at most 1024 bytes, at most 64 elementary streams per
// program), and malformed input yields an error or is skipped, never a panic.
//
// The package does not interpret elementary stream data; internal/remux turns the
// units into MP4 samples (docs/VIDEO-DETECT-SPEC.md §4).
package mpegts
