package mpegts

import "bytes"

// Data before a file's first packet (docs/VIDEO-DETECT-SPEC.md §4.2): some CDNs serve
// every HLS segment disguised as an image, a complete PNG, JPEG, GIF, ... followed by
// the transport stream, which players skip. FindStart finds where the packets begin;
// the Demuxer starts each file there, and whoever reads a fragment as a transport
// stream (internal/mediajob's sniff and early check, ConcatTS) uses its bytes from
// there on.
const (
	// MaxPrefix bounds the data before a file's first packet: FindStart looks for a
	// packet start at the offsets below it.
	MaxPrefix = 64 << 10
	// startPackets is the number of packets in a row, a sync byte every PacketSize
	// bytes, that mark a packet start.
	startPackets = 5
	// StartBytes is the start of a file that FindStart needs to decide every offset
	// below MaxPrefix: the last of them plus its startPackets packet starts.
	StartBytes = MaxPrefix - 1 + (startPackets-1)*PacketSize + 1
)

// FindStart returns the offset of the first transport stream packet of a file. A file
// that starts as a transport stream does, with the sync byte at 0 and at 188 (where it
// has that byte; media.Sniff's rule) and a whole packet, starts at 0 unless it is a GIF
// (whose signature begins with the sync byte, 'G'): a damaged packet among its first
// ones is the demuxer's resynchronisation to pass, not the end of a prefix. Otherwise
// it is the first k below MaxPrefix where the sync byte appears at k, k+188, k+376,
// k+564 and k+752, fewer only where b ends sooner, with at least one whole packet at k.
// ok is false when there is none.
//
// b is the start of the file: at least its first StartBytes bytes, or all of it when it
// is shorter. A shorter b is taken to be the whole file.
func FindStart(b []byte) (k int, ok bool) {
	if len(b) >= PacketSize && b[0] == SyncByte && (len(b) == PacketSize || b[PacketSize] == SyncByte) &&
		!bytes.HasPrefix(b, []byte("GIF8")) {
		return 0, true
	}
	limit := min(MaxPrefix, len(b)-PacketSize+1) // k < limit: a whole packet at k
	for k < limit {
		i := bytes.IndexByte(b[k:limit], SyncByte)
		if i < 0 {
			break
		}
		k += i
		if aligned(b, k) {
			return k, true
		}
		k++
	}
	return 0, false
}

// aligned reports whether b has the sync byte at k and at the next startPackets-1
// packet starts that it holds.
func aligned(b []byte, k int) bool {
	for j := 0; j < startPackets && k+j*PacketSize < len(b); j++ {
		if b[k+j*PacketSize] != SyncByte {
			return false
		}
	}
	return true
}
