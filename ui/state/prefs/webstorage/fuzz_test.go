package webstorage

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// The fuzz targets call the readers without Read's recover, so any panic fails them. Run
// each with go test -run='^$' -fuzz='^FuzzX$' -fuzztime=60s; go test alone runs the seeds.

// seedTables are whole tables carrying the magic (the stage-1 table seed had none, so the
// fuzzer never reached the footer handles), the re-check's footer among them.
func seedTables() [][]byte {
	o := WailsOrigin
	plain := table(false, entry{user: lsKey(o, "downex.theme"), seq: 2, v: latin1("dark")})
	snappy := table(true,
		entry{user: lsKey(o, "downex.density"), seq: 1, v: latin1("compact")},
		entry{user: lsKey(o, "downex.columnWidths"), seq: 3, v: latin1(`{"size":82}`)},
	)
	return [][]byte{plain, snappy, footerTable(^uint64(0)-9, 7), footerTable(0, 64), footerTable(60, 1<<32)}
}

func FuzzSnappy(f *testing.F) {
	f.Add([]byte{17, 3 << 2, 'a', 'b', 'c', 'd', 1 | (8-4)<<2, 4, 2 | 4<<2, 12, 0})
	f.Add([]byte{10, 63 << 2, 0xff, 0xff, 0xff, 0xff, 'a'})
	f.Add([]byte{10, 0, 'a', 3 | 3<<2, 0xff, 0xff, 0xff, 0xff})
	f.Add(snappyLiteral(bytes.Repeat([]byte("downex "), 40)))
	f.Fuzz(func(t *testing.T, src []byte) {
		got, err := snappyDecode(src)
		if err != nil {
			return
		}
		if n, _ := binary.Uvarint(src); uint64(len(got)) != n {
			t.Fatalf("%d bytes for a claim of %d", len(got), n)
		}
	})
}

func FuzzLog(f *testing.F) {
	// Small seeds: the fuzzer slows to a crawl on inputs of a whole 32 KiB block, so the
	// records that span blocks are left to TestReadSkipsWhatItCannotRead.
	f.Add(logFile(batch(1, op{key: []byte("k"), value: []byte("v")})))
	f.Add(logFile(batch(1, op{key: []byte("k"), value: []byte("v")}), batch(2, op{key: []byte("k"), del: true})))
	f.Add([]byte{0, 0, 0, 0, 5, 0, 2, 'a', 'b', 'c', 'd', 'e'}) // a FIRST with a bad checksum
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, rec := range logRecords(data) {
			db{}.applyBatch(rec)
		}
	})
}

func FuzzBatch(f *testing.F) {
	f.Add(batch(5, op{key: []byte("k"), value: []byte("v")}, op{key: []byte("k"), del: true}))
	f.Add(append(batch(^uint64(0), op{key: []byte("k"), value: []byte("v")}), 1, 0xff, 0xff, 0xff, 0xff, 0x0f))
	f.Fuzz(func(t *testing.T, b []byte) { db{}.applyBatch(b) })
}

func FuzzTable(f *testing.F) {
	for _, s := range seedTables() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) { db{}.applyTable(data) })
}

// FuzzTableHandles writes any metaindex and index handle into a valid table's footer, so
// that the fuzzer explores the handle values themselves rather than varint bytes.
func FuzzTableHandles(f *testing.F) {
	f.Add(uint64(0), uint64(0), ^uint64(0)-9, uint64(7))
	f.Add(uint64(1), uint64(2), uint64(0), ^uint64(0))
	f.Add(uint64(0), uint64(0), uint64(1)<<63, uint64(1)<<63)
	f.Add(uint64(0), uint64(0), uint64(10), uint64(20))
	base := table(true, entry{user: lsKey(WailsOrigin, "downex.theme"), seq: 2, v: latin1("dark")})
	body := base[:len(base)-48]
	f.Fuzz(func(t *testing.T, moff, msize, ioff, isize uint64) {
		foot := binary.AppendUvarint(nil, moff)
		foot = binary.AppendUvarint(foot, msize)
		foot = binary.AppendUvarint(foot, ioff)
		foot = binary.AppendUvarint(foot, isize)
		foot = append(foot, make([]byte, 40-len(foot))...)
		data := append(append([]byte(nil), body...), binary.LittleEndian.AppendUint64(foot, tableMagic)...)
		db{}.applyTable(data)
	})
}

func FuzzBlockEntries(f *testing.F) {
	f.Add(blockOf([][2][]byte{{[]byte("a"), []byte("1")}, {[]byte("ab"), []byte("2")}}))
	f.Add(binary.LittleEndian.AppendUint32(make([]byte, 8), ^uint32(0)))
	f.Fuzz(func(t *testing.T, b []byte) {
		blockEntries(b, func(key, value []byte) { _ = append(key[:0:0], value...) })
	})
}

// FuzzDir reads a folder of a CURRENT, a MANIFEST, a log and a table, each from the fuzzer,
// through readDB (no recover) and then Read.
func FuzzDir(f *testing.F) {
	tables := seedTables()
	light := logFile(batch(5, op{key: lsKey(WailsOrigin, "downex.theme"), value: latin1("light")}))
	f.Add([]byte("MANIFEST-000002\n"), manifest(4, 3), light, tables[1])
	f.Add([]byte("MANIFEST-000002\n"), manifest(4, 3), []byte{}, tables[2])
	f.Add([]byte{}, []byte{}, []byte{}, tables[0])
	f.Fuzz(func(t *testing.T, current, mf, log, ldb []byte) {
		dir := t.TempDir()
		for name, data := range map[string][]byte{"CURRENT": current, "MANIFEST-000002": mf, "000004.log": log, "000003.ldb": ldb} {
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := readDB(dir); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(dir, WailsOrigin, nil); err != nil && err != ErrNoStorage {
			t.Fatal(err)
		}
	})
}

func FuzzDecode(f *testing.F) {
	f.Add(latin1("compact"))
	f.Add(utf16le("\U0001F600"))
	f.Fuzz(func(t *testing.T, b []byte) { decode(b) })
}
