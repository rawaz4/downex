// Package filecheck is the file check of docs/DESKTOP-SPEC.md §9: it compares what a
// downloaded file really is (from its bytes), what its name claims (the extension)
// and what the server claimed (the Content-Type), lists the members of ZIP, RAR5 and
// CAB archives (names only, nothing is unpacked), and applies name-only rules (double
// extensions, padding, hidden text-direction characters).
//
// It runs in three stages: CheckName (NAME, before any byte arrives), CheckHead
// (EARLY, from the first HeadBytes) and CheckFile/CheckPath (FINAL, the completed
// file). The result is advisory: it never says a file is safe, and Downex never
// opens, renames, moves or deletes a file because of it. A nil result means there is
// nothing to say.
//
// The package is pure Go (the standard library and internal/model only). Its
// signatures and layouts come from public specifications only (§9.10): the Microsoft
// PE/COFF specification; the System V ABI (ELF); Apple's Mach-O file format reference
// (magic numbers only); [MS-CFB]; [MS-SHLLINK]; [MS-CAB]; the Microsoft VHD Image
// Format Specification; [MS-VHDX]; PKWARE's APPNOTE.TXT; RARLAB's "RAR 5.0 archive
// format"; 7-Zip's 7zFormat.txt (signature only); RFC 1952 (gzip); the xz file
// format; RFC 8878 (Zstandard); POSIX ustar; ECMA-119; ECMA-167/OSTA UDF; ISO 32000
// (PDF); ISO/IEC 14496-12 (ISO-BMFF); RFC 8794 and RFC 9559 (EBML, Matroska); RFC
// 3533 (Ogg); RFC 9639 (FLAC); ISO/IEC 13818-1 (transport stream); ISO/IEC 11172-3
// (MPEG audio header); the ID3v2 specification; W3C PNG; ITU T.81 and JFIF; GIF89a;
// the WebP container specification; the ASF specification; WHATWG MIME Sniffing; the
// xar format documentation; the LSB RPM format; deb(5).
package filecheck
