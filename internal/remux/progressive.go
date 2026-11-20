package remux

import (
	"context"
	"io"
	"os"
)

// Parts of progressive MP4 files (docs/MEDIA-SPEC.md §8 C9, C10;
// docs/SITE-MEDIA-SPEC.md §2.7): the sample tables of the file's 'moov' box (stsz,
// stco/co64, stsc, stts, ctts, stss; ISO/IEC 14496-12 §8.6–§8.7) say where each sample
// lies, so a part can be cut from the file, or from only the span of it that holds the
// part's samples, without re-encoding.

// MaxProgressiveMoov bounds the 'moov' box of a progressive file (C15).
const MaxProgressiveMoov = 64 << 20

// ProgressiveSource is a progressive MP4 file, or a span of one.
type ProgressiveSource struct {
	// Moov is the file's 'moov' box, header included.
	Moov []byte
	// Data is the path of a file that holds the source's bytes from offset DataBase on
	// (the whole file: DataBase 0).
	Data     string
	DataBase int64
}

// progressiveMuxer builds the muxer tracks of the audio and video tracks of a
// progressive 'moov' box: their samples in chunks whose offsets are counted from base.
func progressiveMuxer(moov []byte, data string, base int64) (*muxer, error) {
	if len(moov) > MaxProgressiveMoov {
		return nil, errf(ErrUnsupported, "'moov' box larger than %d bytes", MaxProgressiveMoov)
	}
	info, err := parseInit(moov)
	if err != nil {
		return nil, err
	}
	m := &muxer{inits: []*initInfo{info}, fileIdx: map[string]int{}, progressive: true}
	file := m.fileID(data)
	for _, it := range info.tracks {
		t := &track{src: it, outID: uint32(len(m.tracks) + 1), started: true}
		if err := t.readTables(file, base, &m.totalSamples); err != nil {
			return nil, withContext(err, "track %d", it.trackID)
		}
		m.tracks = append(m.tracks, t)
	}
	return m, nil
}

// readTables fills t's progressive tables from its source sample tables.
func (t *track) readTables(file int, base int64, total *uint64) error {
	stbl := t.src.trak.Mdia.Minf.Stbl
	switch {
	case stbl.Stsz == nil:
		return errf(ErrUnsupported, "no 'stsz' box (compact sample sizes are not read)")
	case stbl.Stts == nil || stbl.Stsc == nil || stbl.Stco == nil && stbl.Co64 == nil:
		return errf(ErrMalformed, "incomplete sample tables")
	}
	n := uint64(stbl.Stsz.SampleNumber)
	if n == 0 {
		return errf(ErrMalformed, "no samples")
	}
	*total += n
	if *total > maxProgressiveSamples {
		return errClipTooLong
	}
	if stbl.Stsz.SampleUniformSize == 0 && uint64(len(stbl.Stsz.SampleSize)) != n {
		return errf(ErrMalformed, "'stsz' lists %d sizes for %d samples", len(stbl.Stsz.SampleSize), n)
	}
	t.sizes = make([]uint32, n)
	for i := range t.sizes {
		if stbl.Stsz.SampleUniformSize != 0 {
			t.sizes[i] = stbl.Stsz.SampleUniformSize
		} else {
			t.sizes[i] = stbl.Stsz.SampleSize[i]
		}
	}
	t.durs = make([]uint32, 0, n)
	for i, c := range stbl.Stts.SampleCount {
		if uint64(len(t.durs))+uint64(c) > n || i >= len(stbl.Stts.SampleTimeDelta) {
			return errf(ErrMalformed, "'stts' describes more samples than 'stsz'")
		}
		for range c {
			t.durs = append(t.durs, stbl.Stts.SampleTimeDelta[i])
		}
	}
	if uint64(len(t.durs)) != n {
		return errf(ErrMalformed, "'stts' describes %d of %d samples", len(t.durs), n)
	}
	t.ctos = make([]int32, n)
	if stbl.Ctts != nil {
		var cnt uint64
		for i := range stbl.Ctts.NrSampleCount() {
			cnt += uint64(stbl.Ctts.SampleCount(i))
		}
		if cnt != n {
			return errf(ErrMalformed, "'ctts' describes %d of %d samples", cnt, n)
		}
		ctos, err := stbl.Ctts.CompositionTimeOffsets(uint32(n))
		if err != nil || uint64(len(ctos)) != n {
			return errf(ErrMalformed, "'ctts' does not match the samples")
		}
		copy(t.ctos, ctos)
	}
	for _, c := range t.ctos {
		t.minCTO = min(t.minCTO, c)
		t.anyCTO = t.anyCTO || c != 0
	}
	if stbl.Stss != nil {
		t.nonSync = true
		for _, s := range stbl.Stss.SampleNumber {
			if s == 0 || uint64(s) > n {
				return errf(ErrMalformed, "'stss' names sample %d of %d", s, n)
			}
		}
		t.syncs = append([]uint32(nil), stbl.Stss.SampleNumber...)
	}
	var offs []uint64
	if stbl.Co64 != nil {
		offs = stbl.Co64.ChunkOffset
	} else {
		offs = make([]uint64, len(stbl.Stco.ChunkOffset))
		for i, o := range stbl.Stco.ChunkOffset {
			offs[i] = uint64(o)
		}
	}
	entries := stbl.Stsc.Entries
	if len(entries) == 0 || entries[0].FirstChunk != 1 {
		return errf(ErrMalformed, "'stsc' does not start at chunk 1")
	}
	sample := uint64(0)
	e := 0
	for k := range offs {
		chunkNr := uint32(k + 1)
		for e+1 < len(entries) && entries[e+1].FirstChunk <= chunkNr {
			e++
		}
		per := uint64(entries[e].SamplesPerChunk)
		if per == 0 || sample+per > n {
			if per == 0 {
				continue
			}
			return errf(ErrMalformed, "'stsc' describes more samples than 'stsz'")
		}
		if offs[k] > uint64(1)<<62 {
			return errf(ErrMalformed, "chunk offset out of range")
		}
		c := chunk{file: file, off: int64(offs[k]) - base, n: uint32(per), sdi: stbl.Stsc.GetSampleDescriptionID(int(chunkNr)), first: sample}
		if c.sdi == 0 || c.sdi > t.src.nEntries {
			return errf(ErrMalformed, "a chunk refers to sample description %d of %d", c.sdi, t.src.nEntries)
		}
		for j := sample; j < sample+per; j++ {
			c.size += int64(t.sizes[j])
		}
		t.chunks = append(t.chunks, c)
		t.payload += c.size
		sample += per
	}
	if sample != n {
		return errf(ErrMalformed, "the chunks hold %d of %d samples", sample, n)
	}
	var at uint64
	j := 0
	for i := range t.chunks {
		t.chunks[i].time = at
		for range t.chunks[i].n {
			at += uint64(t.durs[j])
			j++
		}
	}
	t.next, t.nSamples = at, n
	return nil
}

// cutProgressive builds the tracks of a progressive 'moov' box and cuts them to w.
func cutProgressive(moov []byte, data string, base int64, w Window) (*muxer, Result, error) {
	m, err := progressiveMuxer(moov, data, base)
	if err != nil {
		return nil, Result{}, err
	}
	if err := m.finish(); err != nil {
		return nil, Result{}, err
	}
	res := Result{Container: ContainerMP4, Exact: true}
	if !w.IsZero() {
		offset := movieDuration(m.origin)
		if w.Origin == OriginFirst {
			offset = m.firstInputOffset()
		}
		w.Late = true // every sample of the file is in the tables
		if res, err = m.applyWindow(w, offset); err != nil {
			return nil, Result{}, err
		}
	}
	return m, res, nil
}

// SampleSpan returns the byte span [start, end) of a progressive file that holds every
// sample a part w keeps, from the file's 'moov' box (docs/SITE-MEDIA-SPEC.md §2.7):
// one range that a download of the part fetches.
func SampleSpan(moov []byte, w Window) (start, end int64, err error) {
	m, _, err := cutProgressive(moov, "", 0, w)
	if err != nil {
		return 0, 0, err
	}
	start, end = -1, 0
	for _, t := range m.tracks {
		for _, c := range t.chunks {
			if c.off < 0 {
				return 0, 0, errf(ErrMalformed, "chunk offset out of range")
			}
			if start < 0 || c.off < start {
				start = c.off
			}
			end = max(end, c.off+c.size)
		}
	}
	if start < 0 {
		return 0, 0, errf(ErrMalformed, "the part has no samples")
	}
	return start, end, nil
}

// TrimMP4 writes the part w of the progressive MP4 source into outPath
// (docs/MEDIA-SPEC.md §8 C10): the kept samples are copied byte for byte from
// src.Data, under new sample tables and edit lists (C6–C8). The zero Window copies
// every sample. outPath is created or truncated, synced, and removed again on failure.
func TrimMP4(ctx context.Context, src ProgressiveSource, outPath string, w Window) (Result, error) {
	if src.Data == "" || outPath == "" || cleanPath(src.Data) == cleanPath(outPath) {
		return Result{}, errf(ErrInvalid, "data and output must be two files")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	m, res, err := cutProgressive(src.Moov, src.Data, src.DataBase, w)
	if err != nil {
		return Result{}, err
	}
	fi, err := os.Stat(src.Data)
	if err != nil {
		return Result{}, err
	}
	for _, t := range m.tracks {
		for _, c := range t.chunks {
			if c.off < 0 || c.off+c.size > fi.Size() {
				return Result{}, errf(ErrMalformed, "samples of the part lie outside the downloaded data")
			}
		}
	}
	err = writeOutput(outPath, func(out io.Writer) error {
		fc := newFileCache()
		defer fc.close()
		return m.writeProgressive(ctx, out, &fileChunks{m: m, fc: fc, buf: make([]byte, 1<<20)})
	})
	if err != nil {
		return Result{}, err
	}
	res.Container = ContainerMP4
	return res, nil
}
