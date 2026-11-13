package remux

import (
	"github.com/Eyevinn/mp4ff/mp4"

	"downex/internal/remux/mpegts"
)

// Audio conversion of transport streams (docs/VIDEO-DETECT-SPEC.md §4.4): AAC in
// ADTS frames (ISO/IEC 13818-7 §6.2, ISO/IEC 14496-3 §1.A.2) becomes raw AAC
// samples of an mp4a sample entry whose esds holds the AudioSpecificConfig.

// maxADTSCarry bounds the bytes of a frame carried from one PES to the next.
const maxADTSCarry = 8 << 10

// adtsRates are the sampling frequencies of sampling_frequency_index 0-12.
var adtsRates = [13]int64{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}

// adtsHeader is the parsed header of an ADTS frame.
type adtsHeader struct {
	profile   byte // the audio object type − 1
	rateIndex byte
	channels  byte // channel_configuration
	rawBlocks byte // number_of_raw_data_blocks_in_frame
	hdrLen    int  // 7, or 9 with a CRC
	frameLen  int  // frame_length: header and raw data
}

// parseADTS parses the header at the start of b (at least 7 bytes). It is valid
// with the syncword, layer 0, a sampling frequency index of 0-12, and a
// frame_length of at least the header.
func parseADTS(b []byte) (h adtsHeader, ok bool) {
	if b[0] != 0xff || b[1]&0xf6 != 0xf0 {
		return h, false
	}
	h.hdrLen = 7
	if b[1]&1 == 0 {
		h.hdrLen = 9
	}
	h.profile = b[2] >> 6
	h.rateIndex = b[2] >> 2 & 0x0f
	h.channels = (b[2]&1)<<2 | b[3]>>6
	h.frameLen = int(b[3]&3)<<11 | int(b[4])<<3 | int(b[5])>>5
	h.rawBlocks = b[6] & 3
	return h, h.rateIndex <= 12 && h.frameLen >= h.hdrLen
}

// adtsSplitter splits a stream of ADTS frames that arrives in pieces (PES
// payloads). Its result depends only on the bytes, not on where the stream is cut:
// bytes at which no valid header starts are skipped, and an incomplete frame (or
// header) at the end of a piece is carried over to the next.
type adtsSplitter struct {
	carry   []byte
	scratch []byte
}

func (s *adtsSplitter) reset() { s.carry = s.carry[:0] }

// feed splits the carried bytes and data into frames. fn receives each complete
// frame's header and raw data block (valid during the call) and whether the frame
// started in data rather than in the carried bytes.
func (s *adtsSplitter) feed(data []byte, fn func(h adtsHeader, raw []byte, startedHere bool) error) error {
	buf, carried := data, len(s.carry)
	if carried > 0 {
		s.scratch = append(append(s.scratch[:0], s.carry...), data...)
		buf = s.scratch
	}
	p := 0
	for len(buf)-p >= 7 {
		h, ok := parseADTS(buf[p:])
		if !ok {
			p++
			continue
		}
		if len(buf)-p < h.frameLen {
			break
		}
		if err := fn(h, buf[p+h.hdrLen:p+h.frameLen], p >= carried); err != nil {
			return err
		}
		p += h.frameLen
	}
	if len(buf)-p > maxADTSCarry {
		return errf(ErrMalformed, "ADTS frame of more than %d bytes", maxADTSCarry)
	}
	s.carry = append(s.carry[:0], buf[p:]...)
	return nil
}

// audioConv converts the PES units of one AAC (ADTS) stream.
type audioConv struct {
	split adtsSplitter
	cfg   adtsHeader
	have  bool
	rate  int64
	entry *mp4.AudioSampleEntryBox

	domain int
	timer  audioTimer
	pend   pendingSample
}

func newAudioConv() *audioConv { return &audioConv{domain: -1} }

func (c *audioConv) timescale() uint32 { return uint32(c.rate) }

func (c *audioConv) sampleEntries() []mp4.Box {
	if c.entry == nil {
		return nil
	}
	return []mp4.Box{c.entry}
}

// config checks a frame's configuration against the track's, which the first
// frame sets.
func (c *audioConv) config(h adtsHeader) error {
	if h.rawBlocks != 0 {
		return errf(ErrUnsupported, "%d raw data blocks in one ADTS frame", h.rawBlocks+1)
	}
	if c.have {
		if h.profile != c.cfg.profile || h.rateIndex != c.cfg.rateIndex || h.channels != c.cfg.channels {
			return errf(ErrUnsupported, "the AAC configuration changes within the track")
		}
		return nil
	}
	rate := adtsRates[h.rateIndex]
	switch {
	case h.channels == 0:
		return errf(ErrUnsupported, "AAC channel configuration 0 (a program config element)")
	case rate > 0xffff:
		return errf(ErrUnsupported, "AAC sample rate %d Hz", rate)
	}
	channels := uint16(h.channels)
	if h.channels == 7 {
		channels = 8
	}
	// AudioSpecificConfig: audioObjectType, samplingFrequencyIndex,
	// channelConfiguration, GASpecificConfig (frameLengthFlag, dependsOnCoreCoder,
	// extensionFlag all 0).
	aot := h.profile + 1
	asc := []byte{aot<<3 | h.rateIndex>>1, (h.rateIndex&1)<<7 | h.channels<<3}
	c.entry = mp4.CreateAudioSampleEntryBox("mp4a", channels, 16, uint16(rate), mp4.CreateEsdsBox(asc))
	c.cfg, c.have, c.rate = h, true, rate
	return nil
}

// push converts one PES unit. pts is unwrapped (has: the unit has a PTS; without
// one, pts is the input's last timestamp and only times a domain's first frame).
func (c *audioConv) push(u *mpegts.Unit, pts, _ int64, has bool, domain int, emit func(*tsSample) error) error {
	if domain != c.domain {
		if err := c.endDomain(emit); err != nil {
			return err
		}
		c.domain = domain
		c.timer.newDomain()
		c.split.reset()
	}
	if u.AfterLoss {
		c.split.reset() // the rest of a frame whose next part was lost
	}
	k := int64(0)
	return c.split.feed(u.Data, func(h adtsHeader, raw []byte, here bool) error {
		if err := c.config(h); err != nil {
			return err
		}
		f := mulDivRound(pts, uint64(c.rate), tsClock)
		known := !c.timer.started
		if here {
			f += aacFrame * k
			known = known || has
			k++
		}
		if len(raw) == 0 {
			return nil // a header without raw data: no sample, its time stays a gap
		}
		at, drop, gap := c.timer.place(f, known, c.rate)
		if drop {
			return nil // an overlap
		}
		if c.pend.ok {
			c.pend.s.dur += uint32(gap)
			if err := emit(&c.pend.s); err != nil {
				return err
			}
		}
		c.pend.ok = true
		c.pend.s = tsSample{data: append(c.pend.s.data[:0], raw...), dts: at, dur: aacFrame, sync: true, sdi: 1, domain: domain}
		return nil
	})
}

// endDomain emits the pending frame as the last of its domain.
func (c *audioConv) endDomain(emit func(*tsSample) error) error {
	if !c.pend.ok {
		return nil
	}
	c.pend.ok = false
	c.pend.s.last = true
	return emit(&c.pend.s)
}

func (c *audioConv) flush(emit func(*tsSample) error) error { return c.endDomain(emit) }
