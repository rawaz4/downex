// Package webstorage reads, read-only, what today's window kept in WebView2's localStorage:
// the LevelDB folder Chromium writes (EBWebView\Default\Local Storage\leveldb). It is a
// small tolerant reader written from LevelDB's documented file formats (doc/log_format.md,
// doc/table_format.md and the VersionEdit tags of db/version_edit.cc) and Chromium's
// localStorage key layout: it reads the write-ahead .log files and the .ldb/.sst tables
// (uncompressed or Snappy blocks), keeps the newest record of each key, and skips whatever
// it cannot read. It never writes to the folder, never locks it and never creates
// anything there.
package webstorage

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// maxFile bounds what is read of one file: localStorage is capped at 10 MB per origin, and a
// folder of this size is all one window's.
const maxFile = 64 << 20

// record is one key's newest state.
type record struct {
	seq     uint64
	deleted bool
	value   []byte
}

// db is the folder's records by user key.
type db map[string]record

func (d db) put(key []byte, seq uint64, deleted bool, value []byte) {
	if cur, ok := d[string(key)]; ok && cur.seq > seq {
		return
	}
	d[string(key)] = record{seq: seq, deleted: deleted, value: value}
}

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// unmask undoes LevelDB's CRC masking.
func unmask(c uint32) uint32 {
	rot := c - 0xa282ead8
	return rot>>17 | rot<<15
}

const (
	blockSize  = 32 << 10
	headerSize = 7
)

// logRecords splits a log-format file (a .log or the MANIFEST) into its records: FULL, or
// FIRST, MIDDLE… LAST pieces joined. A piece with a bad checksum, a bad length or out of
// order drops the record it belongs to; reading goes on with the next block.
func logRecords(data []byte) [][]byte {
	var out [][]byte
	var partial []byte
	inRecord := false
	for off := 0; off < len(data); {
		left := blockSize - off%blockSize
		if left < headerSize {
			off += left // the block's trailer
			continue
		}
		if off+headerSize > len(data) {
			break
		}
		h := data[off : off+headerSize]
		length := int(binary.LittleEndian.Uint16(h[4:6]))
		typ := h[6]
		if typ == 0 && length == 0 {
			// preallocated or zeroed space: the rest of the block is empty
			off += left
			continue
		}
		if headerSize+length > left || off+headerSize+length > len(data) {
			partial, inRecord = nil, false
			off += left
			continue
		}
		payload := data[off+headerSize : off+headerSize+length]
		crc := crc32.Update(crc32.Checksum([]byte{typ}, castagnoli), castagnoli, payload)
		off += headerSize + length
		if unmask(binary.LittleEndian.Uint32(h[0:4])) != crc {
			partial, inRecord = nil, false
			continue
		}
		switch typ {
		case 1: // FULL
			out = append(out, payload)
			partial, inRecord = nil, false
		case 2: // FIRST
			partial, inRecord = append([]byte(nil), payload...), true
		case 3: // MIDDLE
			if inRecord {
				partial = append(partial, payload...)
			}
		case 4: // LAST
			if inRecord {
				out = append(out, append(partial, payload...))
			}
			partial, inRecord = nil, false
		default:
			partial, inRecord = nil, false
		}
	}
	return out
}

// reader reads varints and length-prefixed slices; a short or bad input sets bad.
type reader struct {
	b   []byte
	bad bool
}

func (r *reader) uvarint() uint64 {
	v, n := binary.Uvarint(r.b)
	if n <= 0 {
		r.bad = true
		r.b = nil
		return 0
	}
	r.b = r.b[n:]
	return v
}

func (r *reader) bytes(n uint64) []byte {
	if r.bad || n > uint64(len(r.b)) {
		r.bad = true
		r.b = nil
		return nil
	}
	out := r.b[:n]
	r.b = r.b[n:]
	return out
}

func (r *reader) prefixed() []byte { return r.bytes(r.uvarint()) }

// applyBatch adds a WriteBatch's records: an 8-byte sequence, a 4-byte count, then puts
// (tag 1, key, value) and deletions (tag 0, key) numbered from the sequence.
func (d db) applyBatch(b []byte) {
	if len(b) < 12 {
		return
	}
	seq := binary.LittleEndian.Uint64(b[0:8])
	count := binary.LittleEndian.Uint32(b[8:12])
	r := &reader{b: b[12:]}
	for i := uint32(0); i < count && len(r.b) > 0 && !r.bad; i++ {
		tag := r.b[0]
		r.b = r.b[1:]
		switch tag {
		case 1:
			key := r.prefixed()
			value := r.prefixed()
			if !r.bad {
				d.put(key, seq+uint64(i), false, value)
			}
		case 0:
			key := r.prefixed()
			if !r.bad {
				d.put(key, seq+uint64(i), true, nil)
			}
		default:
			return
		}
	}
}

// ---- tables ---------------------------------------------------------------------------------

const tableMagic = 0xdb4775248b80fb57

type handle struct{ offset, size uint64 }

func readHandle(r *reader) handle { return handle{r.uvarint(), r.uvarint()} }

var errBlock = errors.New("unreadable block")

// block is a table block's contents after its trailer (type and checksum) was checked. The
// handle comes from the file and is not covered by a checksum, so every bound is checked
// without forming a sum that could wrap: offset, size and the 5-byte trailer must all fit
// in what is left of data.
func block(data []byte, h handle) ([]byte, error) {
	n := uint64(len(data))
	if h.offset > n || h.size > n-h.offset || n-h.offset-h.size < 5 || h.size > maxFile {
		return nil, errBlock
	}
	end := h.offset + h.size // at most len(data)-5: no wrap, and it fits an int
	raw := data[h.offset:end]
	trailer := data[end : end+5]
	crc := crc32.Update(crc32.Checksum(raw, castagnoli), castagnoli, trailer[:1])
	if unmask(binary.LittleEndian.Uint32(trailer[1:5])) != crc {
		return nil, errBlock
	}
	switch trailer[0] {
	case 0:
		return raw, nil
	case 1:
		return snappyDecode(raw)
	}
	return nil, errBlock // another compression (zstd): not read
}

// blockEntries calls f with every key and value of a block (keys rebuilt from their shared
// prefixes). The restart array's size is computed in uint64, so a huge count cannot wrap
// (on 32-bit builds int(restarts)*4 would).
func blockEntries(b []byte, f func(key, value []byte)) {
	if len(b) < 4 {
		return
	}
	restarts := uint64(binary.LittleEndian.Uint32(b[len(b)-4:]))
	room := uint64(len(b) - 4)
	if restarts == 0 || restarts > room/4 {
		return
	}
	r := &reader{b: b[:room-restarts*4]}
	var key []byte
	for len(r.b) > 0 && !r.bad {
		shared := r.uvarint()
		unshared := r.uvarint()
		vlen := r.uvarint()
		if r.bad || shared > uint64(len(key)) {
			return
		}
		delta := r.bytes(unshared)
		value := r.bytes(vlen)
		if r.bad {
			return
		}
		key = append(key[:shared:shared], delta...)
		f(key, value)
	}
}

// applyTable adds a table's entries (internal keys: the user key and 8 bytes of sequence
// and type).
func (d db) applyTable(data []byte) {
	if len(data) < 48 || binary.LittleEndian.Uint64(data[len(data)-8:]) != tableMagic {
		return
	}
	r := &reader{b: data[len(data)-48 : len(data)-8]}
	readHandle(r) // metaindex: filters, not needed
	index := readHandle(r)
	if r.bad {
		return
	}
	ib, err := block(data, index)
	if err != nil {
		return
	}
	// Each data block is read once, however many index entries name it (a malformed index
	// could name one block over and over).
	seen := map[handle]bool{}
	blockEntries(ib, func(_, value []byte) {
		hr := &reader{b: value}
		h := readHandle(hr)
		if hr.bad || seen[h] {
			return
		}
		seen[h] = true
		data2, err := block(data, h)
		if err != nil {
			return
		}
		blockEntries(data2, func(ikey, v []byte) {
			if len(ikey) < 8 {
				return
			}
			trailer := binary.LittleEndian.Uint64(ikey[len(ikey)-8:])
			user := append([]byte(nil), ikey[:len(ikey)-8]...)
			switch trailer & 0xff {
			case 1:
				d.put(user, trailer>>8, false, append([]byte(nil), v...))
			case 0:
				d.put(user, trailer>>8, true, nil)
			}
		})
	})
}

// ---- the folder --------------------------------------------------------------------------------

// version is what the MANIFEST says is live: the tables and the oldest log still in use.
type version struct {
	tables  map[uint64]bool
	logNum  uint64
	prevLog uint64
	ok      bool
}

func readManifest(dir string) version {
	cur, err := readFile(filepath.Join(dir, "CURRENT"))
	if err != nil {
		return version{}
	}
	name := strings.TrimSpace(string(cur))
	if !strings.HasPrefix(name, "MANIFEST-") || strings.ContainsAny(name, `/\`) {
		return version{}
	}
	data, err := readFile(filepath.Join(dir, name))
	if err != nil {
		return version{}
	}
	v := version{tables: map[uint64]bool{}}
	for _, rec := range logRecords(data) {
		r := &reader{b: rec}
		for len(r.b) > 0 && !r.bad {
			switch r.uvarint() {
			case 1: // comparator
				r.prefixed()
			case 2: // log number
				v.logNum = r.uvarint()
			case 3, 4: // next file number, last sequence
				r.uvarint()
			case 5: // compact pointer
				r.uvarint()
				r.prefixed()
			case 6: // deleted file
				r.uvarint()
				delete(v.tables, r.uvarint())
			case 7: // new file
				r.uvarint()
				n := r.uvarint()
				r.uvarint()
				r.prefixed()
				r.prefixed()
				if !r.bad {
					v.tables[n] = true
				}
			case 9: // previous log number
				v.prevLog = r.uvarint()
			default:
				r.bad = true
			}
		}
		if r.bad {
			return version{}
		}
	}
	v.ok = true
	return v
}

func readFile(path string) ([]byte, error) {
	f, err := openShared(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxFile {
		return nil, errors.New("not a readable file")
	}
	buf := make([]byte, info.Size())
	n, err := f.ReadAt(buf, 0)
	if err != nil && n != len(buf) {
		return buf[:n], nil // a log being written: what was there
	}
	return buf, nil
}

// fileNumber is the number of "000123.log" (ok false for other names).
func fileNumber(name, ext string) (uint64, bool) {
	if !strings.HasSuffix(name, ext) {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimSuffix(name, ext), 10, 64)
	return n, err == nil
}

// readDB reads every record the folder holds.
func readDB(dir string) (db, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	v := readManifest(dir)
	type file struct {
		num  uint64
		name string
		log  bool
	}
	var files []file
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		name := e.Name()
		if n, ok := fileNumber(name, ".log"); ok {
			// a log older than the MANIFEST's is already in its tables
			if v.ok && n < v.logNum && n != v.prevLog {
				continue
			}
			files = append(files, file{n, name, true})
			continue
		}
		for _, ext := range []string{".ldb", ".sst"} {
			if n, ok := fileNumber(name, ext); ok {
				if v.ok && !v.tables[n] {
					continue // obsolete, not deleted yet
				}
				files = append(files, file{n, name, false})
			}
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].num < files[j].num })
	d := db{}
	for _, f := range files {
		data, err := readFile(filepath.Join(dir, f.name))
		if err != nil {
			continue
		}
		if f.log {
			for _, rec := range logRecords(data) {
				d.applyBatch(rec)
			}
		} else {
			d.applyTable(data)
		}
	}
	return d, nil
}
