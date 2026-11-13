package testmedia

import "math/rand"

// SampleRates are the sampling frequencies of the ADTS sampling_frequency_index
// (ISO/IEC 14496-3 Table 1.18).
var SampleRates = [13]int{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}

// RateIndex returns the sampling_frequency_index of rate, or -1.
func RateIndex(rate int) int {
	for i, r := range SampleRates {
		if r == rate {
			return i
		}
	}
	return -1
}

// AudioConfig describes a generated AAC elementary stream in ADTS frames.
type AudioConfig struct {
	ObjectType int   // AAC audio object type 1-4 (default 2: AAC LC); the ADTS profile is ObjectType − 1
	SampleRate int   // default 48000
	Channels   int   // channel_configuration (default 2)
	Frames     int   // number of frames
	StartPTS   int64 // presentation time of the first frame in 90 kHz ticks (unwrapped)
	Seed       int64
	CRC        bool // protection_absent 0: a 16-bit CRC follows the header
	// RawBlocks sets number_of_raw_data_blocks_in_frame (normally 0).
	RawBlocks int
	// FrameBytes is the mean raw data size (default 300).
	FrameBytes int
}

// AudioFrame is one generated AAC frame.
type AudioFrame struct {
	PTS  int64  // 90 kHz ticks: StartPTS + k × 1024 × 90000 / rate, rounded down
	ADTS []byte // header (and CRC) followed by Raw
	Raw  []byte // the raw_data_block an MP4 sample holds
}

func (c *AudioConfig) defaults() {
	if c.ObjectType == 0 {
		c.ObjectType = 2
	}
	if c.SampleRate == 0 {
		c.SampleRate = 48000
	}
	if c.Channels == 0 {
		c.Channels = 2
	}
	if c.FrameBytes == 0 {
		c.FrameBytes = 300
	}
}

// AAC generates the frames of c.
func AAC(c AudioConfig) []AudioFrame {
	c.defaults()
	rng := rand.New(rand.NewSource(c.Seed))
	out := make([]AudioFrame, c.Frames)
	for k := range out {
		raw := make([]byte, c.FrameBytes/2+rng.Intn(c.FrameBytes))
		rng.Read(raw)
		out[k] = AudioFrame{
			PTS:  c.StartPTS + int64(k)*1024*90000/int64(c.SampleRate),
			ADTS: ADTSFrame(c, raw),
			Raw:  raw,
		}
	}
	return out
}

// ADTSHeader returns the 7-byte ADTS header (9 bytes with a CRC field of zeros)
// of a frame carrying payload bytes of raw data (ISO/IEC 13818-7 §6.2).
func ADTSHeader(c AudioConfig, payload int) []byte {
	c.defaults()
	hl := 7
	if c.CRC {
		hl = 9
	}
	idx := RateIndex(c.SampleRate)
	if idx < 0 {
		idx = 3
	}
	var w BitWriter
	w.Bits(0xfff, 12)                 // syncword
	w.Bit(0)                          // ID: MPEG-4
	w.Bits(0, 2)                      // layer
	w.Flag(!c.CRC)                    // protection_absent
	w.Bits(uint64(c.ObjectType-1), 2) // profile
	w.Bits(uint64(idx), 4)
	w.Bit(0) // private_bit
	w.Bits(uint64(c.Channels), 3)
	w.Bit(0) // original_copy
	w.Bit(0) // home
	w.Bit(0) // copyright_identification_bit
	w.Bit(0) // copyright_identification_start
	w.Bits(uint64(hl+payload), 13)
	w.Bits(0x7ff, 11) // adts_buffer_fullness: variable rate
	w.Bits(uint64(c.RawBlocks), 2)
	h := w.Bytes()
	if c.CRC {
		h = append(h, 0, 0)
	}
	return h
}

// ADTSFrame returns raw wrapped in an ADTS header.
func ADTSFrame(c AudioConfig, raw []byte) []byte {
	return append(ADTSHeader(c, len(raw)), raw...)
}

// AudioSpecificConfig returns the two-byte AudioSpecificConfig an MP4 sample entry
// of c holds: object type, the sampling frequency index and the
// channel configuration (ISO/IEC 14496-3 §1.6.2.1, GASpecificConfig all zero).
func AudioSpecificConfig(c AudioConfig) []byte {
	c.defaults()
	aot := c.ObjectType
	idx := RateIndex(c.SampleRate)
	return []byte{byte(aot<<3 | idx>>1), byte((idx&1)<<7 | c.Channels<<3)}
}
