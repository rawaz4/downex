// Package testmedia generates small, valid media for tests: H.264 and HEVC
// parameter sets and access units (ISO/IEC 14496-10, ISO/IEC 23008-2), AAC ADTS
// frames (ISO/IEC 13818-7, 14496-3), MPEG-2 transport streams (ISO/IEC 13818-1)
// with PSI, PES, PCR, continuity counters and fault injection, HLS playlists
// (RFC 8216) and progressive MP4 files (ISO/IEC 14496-12/14) built with mp4ff.
//
// Every generator is deterministic for a given seed. The package is imported by
// tests only (internal/remux, internal/remux/mpegts, internal/e2e); no command
// depends on it.
package testmedia
