package mediajob

import (
	"downex/internal/media"
	"downex/internal/remux/mpegts"
)

// Segments disguised as images (docs/VIDEO-DETECT-SPEC.md §4.2, §4.8): some CDNs serve
// every HLS segment as an image, a complete PNG, JPEG, GIF, ... followed by the
// transport stream, which players skip. A fragment file keeps what was received; the
// sniff, the early check and internal/remux read it from its first packet
// (mpegts.FindStart), which they find again from the file, so a resumed job needs no
// record of it. Fragmented MP4 behind such a prefix is not recognised.

// sniffContainer identifies the container of a fragment from its first bytes (at least
// mpegts.StartBytes of them, or all): WebM and fragmented MP4 by their magic at offset
// 0 (media.Sniff), a transport stream when its first packet starts within
// mpegts.MaxPrefix bytes, else ContainerUnknown.
func sniffContainer(prefix []byte) media.Container {
	switch c := media.Sniff(prefix); c {
	case media.ContainerWebM, media.ContainerFMP4:
		return c
	}
	if _, ok := mpegts.FindStart(prefix); ok {
		return media.ContainerTS
	}
	return media.ContainerUnknown
}

// sniffFile is sniffContainer of the start of the file at path.
func sniffFile(path string) (media.Container, error) {
	data, err := readPrefix(path, mpegts.StartBytes)
	if err != nil {
		return media.ContainerUnknown, err
	}
	return sniffContainer(data), nil
}

// readTSPrefix reads at most n bytes of the transport stream file at path from its first
// packet: what precedes it is not media. A file without a packet start is read from its
// offset 0, for remux.CheckTS to refuse.
func readTSPrefix(path string, n int) ([]byte, error) {
	data, err := readPrefix(path, n+mpegts.StartBytes)
	if err != nil {
		return nil, err
	}
	if k, ok := mpegts.FindStart(data); ok {
		data = data[k:]
	}
	return data[:min(len(data), n)], nil
}
