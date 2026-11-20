package filecheck

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"downex/internal/model"
)

func checkBytes(t *testing.T, name string, b []byte, served string) *model.FileCheck {
	t.Helper()
	fc, err := CheckFile(context.Background(), bytes.NewReader(b), int64(len(b)), name, false, served)
	if err != nil {
		t.Fatalf("CheckFile(%s): %v", name, err)
	}
	return fc
}

func filler(n int) []byte { return bytes.Repeat([]byte{0xAB}, n) }

// TestVerdicts is the judge matrix of §9.5 with the exact summaries of §9.6.
func TestVerdicts(t *testing.T) {
	pdfZip := append([]byte("%PDF-1.7\n"), filler(70000)...)
	pdfWithProgram := zipBytes(t, pdfZip, "readme.txt", "tool.exe")
	pdfWithText := zipBytes(t, pdfZip, "readme.txt")
	tests := []struct {
		name    string
		file    string
		data    []byte
		served  string
		level   model.FileCheckLevel
		reason  string
		summary string
	}{
		{"program as mp4", "movie.mp4", peBytes(false), "", model.FileCheckDanger, "program_disguised",
			"This file is a Windows program, not an MP4 video."},
		{"script as jpg", "photo.jpg", []byte("#!/bin/sh\nrm -rf ~\n"), "", model.FileCheckDanger, "program_disguised",
			"This file is a script, not a JPEG image."},
		{"zip with program as mp4", "movie.mp4", zipBytes(t, nil, "movie/setup.exe"), "", model.FileCheckDanger, "archive_with_program",
			"This file is a ZIP archive, not an MP4 video, and it holds a program: “movie/setup.exe”."},
		{"zip with programs as mp4", "movie.mp4", zipBytes(t, nil, "a.exe", "b.scr", "c.txt"), "", model.FileCheckDanger, "archive_with_program",
			"This file is a ZIP archive, not an MP4 video, and it holds a program: “a.exe” and 1 more."},
		{"zip without program as mp4", "movie.mp4", zipBytes(t, nil, "a.txt"), "", model.FileCheckCaution, "archive_disguised",
			"This file is a ZIP archive, not an MP4 video."},
		{"program in docx", "report.docx", zipBytes(t, nil, "[Content_Types].xml", "word/document.xml", "run.exe"), "", model.FileCheckDanger, "program_inside_document",
			"This Word document holds a program: “run.exe”."},
		// An e-book's own scripts are not programs (§9.3), but anything else that runs is,
		// and is named even after a script.
		{"program in epub with scripts", "book.epub", zipBytes(t, nil, "META-INF/container.xml", "OEBPS/a.js",
			"OEBPS/b.js", "setup.exe", "OEBPS/c.js"), "", model.FileCheckDanger, "program_inside_document",
			"This EPUB e-book holds a program: “setup.exe”."},
		{"disguised script in epub", "book.epub", zipBytes(t, nil, "META-INF/container.xml", "cover.jpg.js"), "",
			model.FileCheckDanger, "program_inside_document", "This EPUB e-book holds a program: “cover.jpg.js”."},
		{"scripts in a plain zip named epub", "book.epub", zipBytes(t, nil, "OEBPS/a.js"), "", model.FileCheckDanger,
			"program_inside_document", "This EPUB e-book holds a program: “OEBPS/a.js”."},
		{"disguised member", "photos.zip", zipBytes(t, nil, "invoice.pdf.exe"), "", model.FileCheckDanger, "disguised_member",
			"This archive holds “invoice.pdf.exe”, a program whose name imitates a PDF document."},
		{"disguised member in rar", "photos.rar", rar5Bytes(false, "pic.jpg.scr"), "", model.FileCheckDanger, "disguised_member",
			"This archive holds “pic.jpg.scr”, a program whose name imitates a JPEG image."},
		{"disguised member in cab", "drivers.cab", cabBytes("readme.txt", "notes.txt‮exe.scr"), "", model.FileCheckDanger, "disguised_member",
			"This archive holds “notes.txt⟨U+202E⟩exe.scr”, a program whose name imitates another type of file."},
		{"pdf with appended program", "paper.pdf", pdfWithProgram, "", model.FileCheckDanger, "appended_archive_with_program",
			"This PDF document also carries a ZIP archive with a program inside: “tool.exe”."},
		{"pdf with appended archive", "paper.pdf", pdfWithText, "", model.FileCheckNote, "appended_archive",
			"This PDF document also carries a ZIP archive."},
		{"iso as mp4", "movie.mp4", isoBytes(), "", model.FileCheckDanger, "disk_image_disguised",
			"This file is a disc image (ISO), not an MP4 video. A disc image can hold programs."},
		{"web page as zip", "data.zip", []byte("<!DOCTYPE html><html><title>Sign in</title></html>"), "", model.FileCheckCaution, "web_page",
			"This file is a web page, not a ZIP archive. The server may have sent an error or sign-in page instead of the file; check the address or sign in on the site, then download it again."},
		{"text as mp4", "video.mp4", []byte("Access denied: the link has expired.\n"), "", model.FileCheckCaution, "text_not_binary",
			"This file is plain text, not an MP4 video. The server may have sent a message instead of the file."},
		{"text as exe", "setup.exe", []byte("Access denied: the link has expired.\n"), "", model.FileCheckCaution, "text_not_binary",
			"This file is plain text, not a Windows program. The server may have sent a message instead of the file."},
		{"7z as mp4", "movie.mp4", append([]byte("7z\xbc\xaf\x27\x1c"), filler(100)...), "", model.FileCheckCaution, "archive_disguised",
			"This file is a 7z archive, not an MP4 video."},
		{"png as jpg", "photo.jpg", append([]byte("\x89PNG\r\n\x1a\n"), filler(100)...), "", model.FileCheckNote, "other_type",
			"This file is a PNG image, though its name says a JPEG image."},
		{"rar as zip", "a.zip", rar5Bytes(false, "a.txt"), "", model.FileCheckNote, "archive_kind",
			"This file is a RAR archive, though its name says a ZIP archive."},
		{"gzip as html", "page.html", append([]byte("\x1f\x8b\x08"), filler(100)...), "", model.FileCheckNote, "other_type",
			"This file is a gzip archive, though its name says a web page."},
		{"apk as docx", "cv.docx", zipBytes(t, nil, "AndroidManifest.xml", "classes.dex"), "", model.FileCheckDanger, "program_disguised",
			"This file is an Android app, not a Word document."},
		{"program served as video, no extension", "download", peBytes(false), "video/mp4", model.FileCheckDanger, "program_disguised",
			"This file is a Windows program, not a video."},
		{"exe served as video", "clip.exe", peBytes(false), "video/mp4", model.FileCheckDanger, "served_as_other",
			"The server said this was a video (video/mp4), but it is a Windows program."},
		{"exe that is an mp4", "clip.exe", append([]byte("\x00\x00\x00\x20ftypisom"), filler(100)...), "video/mp4", model.FileCheckNote, "other_type",
			"This file is an MP4 video, though its name says a Windows program."},
		{"double extension", "invoice.pdf.exe", peBytes(false), "", model.FileCheckDanger, "double_extension",
			"“invoice.pdf.exe” is a Windows program whose name imitates a PDF document."},
		{"padding", "photo.jpg      .scr", peBytes(false), "", model.FileCheckDanger, "name_padding",
			"“photo.jpg      .scr” is a Windows program whose real extension is hidden behind spaces."},
		{"hangul filler", "photo.jpgㅤㅤㅤ.exe", peBytes(false), "", model.FileCheckDanger, "name_padding",
			"“photo.jpgㅤㅤㅤ.exe” is a Windows program whose real extension is hidden behind spaces."},
		{"filler run", "holiday   pictures.exe", peBytes(false), "", model.FileCheckDanger, "name_padding",
			"“holiday   pictures.exe” is a Windows program whose real extension is hidden behind spaces."},
		{"dll as png", "logo.png", peBytes(true), "", model.FileCheckDanger, "program_disguised",
			"This file is a Windows program library (DLL), not a PNG image."},
		{"msi as pdf", "manual.pdf", cfbBytes(9, clsidMSI), "", model.FileCheckDanger, "program_disguised",
			"This file is a Windows installer, not a PDF document."},
		{"shortcut as jpg", "photo.jpg", lnkBytes(), "", model.FileCheckDanger, "program_disguised",
			"This file is a Windows shortcut, not a JPEG image."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fc := checkBytes(t, tc.file, tc.data, tc.served)
			if fc == nil {
				t.Fatalf("no result, want %v %s", tc.level, tc.reason)
			}
			if fc.Level != tc.level || fc.Reason != tc.reason || fc.Stage != model.FileCheckStageFinal {
				t.Fatalf("result = %+v, want level %v reason %s", fc, tc.level, tc.reason)
			}
			if got := Summary(fc, tc.file); got != tc.summary {
				t.Fatalf("Summary =\n  %s\nwant\n  %s", got, tc.summary)
			}
		})
	}
}

// TestFalseAlarmGuards: the kept guards of §9.5, each NONE or NOTE.
func TestFalseAlarmGuards(t *testing.T) {
	elfSO := append([]byte("\x7fELF\x02\x01\x01"), filler(100)...)
	sfx := zipBytes(t, append(peBytes(false), filler(70000)...), "setup.exe", "data.bin")
	tests := []struct {
		name string
		file string
		data []byte
		max  model.FileCheckLevel
	}{
		{"docx", "a.docx", zipBytes(t, nil, "[Content_Types].xml", "word/document.xml"), model.FileCheckNone},
		{"docm macros", "a.docm", zipBytes(t, nil, "[Content_Types].xml", "word/vbaProject.bin"), model.FileCheckNone},
		{"xlsx encrypted", "a.xlsx", cfbBytes(9, make([]byte, 16)), model.FileCheckNone},
		{"xls html", "a.xls", []byte("<html><table><tr><td>1</td></tr></table></html>"), model.FileCheckNone},
		{"xls xml", "a.xls", []byte("<?xml version=\"1.0\"?><Workbook/>"), model.FileCheckNone},
		{"doc rtf", "a.doc", []byte(`{\rtf1\ansi hello}`), model.FileCheckNone},
		{"odt", "a.odt", zipBytes(t, nil, "mimetype", "content.xml", "META-INF/manifest.xml"), model.FileCheckNone},
		{"epub", "a.epub", zipBytes(t, nil, "mimetype", "META-INF/container.xml"), model.FileCheckNone},
		{"apk", "a.apk", zipBytes(t, nil, "AndroidManifest.xml", "lib/x86/libx.so"), model.FileCheckNone},
		{"jar", "a.jar", zipBytes(t, nil, "META-INF/MANIFEST.MF", "a/B.class"), model.FileCheckNone},
		{"xpi", "a.xpi", zipBytes(t, nil, "manifest.json", "background.js"), model.FileCheckNone},
		{"appx", "a.msix", zipBytes(t, nil, "AppxManifest.xml", "app.exe"), model.FileCheckNone},
		{"nupkg", "a.nupkg", zipBytes(t, nil, "[Content_Types].xml", "a.nuspec", "tools/install.ps1"), model.FileCheckNone},
		{"whl", "a.whl", zipBytes(t, nil, "pkg/__init__.py", "pkg-1.0.dist-info/METADATA"), model.FileCheckNone},
		{"ts text", "app.ts", []byte("export const x: number = 1;\n"), model.FileCheckNone},
		{"ts stream", "seg.ts", tsBytes(), model.FileCheckNone},
		{"m4a as mp3", "song.mp3", append([]byte("\x00\x00\x00\x20ftypM4A "), filler(100)...), model.FileCheckNote},
		{"webp as jpg", "a.jpg", append([]byte("RIFF\x00\x00\x00\x00WEBP"), filler(100)...), model.FileCheckNote},
		{"png as jpg", "a.jpg", append([]byte("\x89PNG\r\n\x1a\n"), filler(100)...), model.FileCheckNote},
		{"mp4 as gif", "a.gif", append([]byte("\x00\x00\x00\x20ftypisom"), filler(100)...), model.FileCheckNote},
		{"setup in zip", "setup.zip", zipBytes(t, nil, "setup.exe", "readme.txt"), model.FileCheckNone},
		{"self-extractor", "setup.exe", sfx, model.FileCheckNone},
		{"tar.gz", "src.tar.gz", append([]byte("\x1f\x8b\x08"), filler(100)...), model.FileCheckNone},
		{"jquery.min.js", "jquery.min.js", []byte("/*! jQuery */!function(e,t){\"use strict\";}(this);\n"), model.FileCheckNone},
		{"versioned installer", "python-3.12.0-amd64.exe", peBytes(false), model.FileCheckNone},
		{"rtl name with marks", "‏ملف.pdf", append([]byte("%PDF-1.7\n"), filler(100)...), model.FileCheckNone},
		{"shell text", "notes.txt", []byte("#!/bin/sh\necho hello\n"), model.FileCheckNone},
		{"raw img", "disk.img", filler(5000), model.FileCheckNone},
		{"iso hybrid", "linux.iso", isoBytes(), model.FileCheckNone},
		{"udf iso", "movie.iso", udfBytes(), model.FileCheckNone},
		{"so", "libx.so", elfSO, model.FileCheckNone},
		{"unknown bytes as mp4", "a.mp4", filler(5000), model.FileCheckNone},
		{"mz only", "a.exe", append([]byte("MZ"), filler(100)...), model.FileCheckNone},
		{"no extension", "README", []byte("hello"), model.FileCheckNone},
		{"vhd fixed", "disk.vhd", append(filler(70000), append([]byte("conectix"), make([]byte, 504)...)...), model.FileCheckNone},
		{"com anything", "a.com", []byte("hello world"), model.FileCheckNone},
		{"empty", "a.mp4", nil, model.FileCheckNone},
		// EPUB 3 content documents may carry scripts the reading app runs.
		{"epub with scripts", "book.epub", zipBytes(t, nil, "mimetype", "META-INF/container.xml",
			"OEBPS/content.opf", "OEBPS/js/reader.js", "OEBPS/js/quiz.js"), model.FileCheckNone},
		// A drawing whose licence comment is longer than the first KiB is not a web page.
		{"svg with a long licence", "logo.svg", append(append([]byte("<!--"), repeatText(" licence text ", 200)...),
			"-->\n<!-- second -->\n<svg xmlns=\"http://www.w3.org/2000/svg\"/>"...), model.FileCheckNone},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fc := checkBytes(t, tc.file, tc.data, "")
			level := model.FileCheckNone
			if fc != nil {
				level = fc.Level
			}
			if level > tc.max {
				t.Fatalf("result = %+v (%s), want at most %v", fc, Summary(fc, tc.file), tc.max)
			}
		})
	}
}

func TestCheckName(t *testing.T) {
	tests := []struct {
		name     string
		controls bool
		served   string
		level    model.FileCheckLevel
		reason   string
		summary  string
	}{
		{"invoice.pdf.exe", false, "", model.FileCheckDanger, "double_extension",
			"“invoice.pdf.exe” is a Windows program whose name imitates a PDF document."},
		{"flagexe.txt", true, "", model.FileCheckNote, "name_had_controls",
			"The name this file came with contained hidden text-direction characters; Downex removed them."},
		{"flagtxt.exe", true, "", model.FileCheckDanger, "name_hid_direction",
			"The name this file came with used hidden characters that reverse text, a trick to make a Windows program look like another type of file. Downex removed them; the real name is “flagtxt.exe”."},
		{"clip.exe", false, "video/mp4; codecs=avc1", model.FileCheckDanger, "served_as_other",
			"The server said this was a video (video/mp4), but it is a Windows program."},
		{"letter.scr", false, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", model.FileCheckDanger, "served_as_other",
			"The server said this was a document (application/vnd.openxmlformats-officedocument.wordprocessingml.document), but it is a Windows program."},
		{"photo.jpg   .exe", false, "", model.FileCheckDanger, "name_padding",
			"“photo.jpg   .exe” is a Windows program whose real extension is hidden behind spaces."},
	}
	for _, tc := range tests {
		fc := CheckName(tc.name, tc.controls, tc.served)
		if fc == nil || fc.Level != tc.level || fc.Reason != tc.reason || fc.Stage != model.FileCheckStageName {
			t.Fatalf("CheckName(%q) = %+v, want %v %s", tc.name, fc, tc.level, tc.reason)
		}
		if got := Summary(fc, tc.name); got != tc.summary {
			t.Fatalf("Summary(%q) =\n  %s\nwant\n  %s", tc.name, got, tc.summary)
		}
	}
	for _, ok := range []string{"movie.mp4", "jquery.min.js", "python-3.12.0-amd64.exe", "setup.exe", "‏تقرير.pdf", "a.b.c.zip"} {
		if fc := CheckName(ok, false, ""); fc != nil {
			t.Fatalf("CheckName(%q) = %+v, want nothing", ok, fc)
		}
	}
	if fc := CheckName("setup.exe", false, "application/octet-stream"); fc != nil {
		t.Fatalf("generic type = %+v", fc)
	}
}

func TestCheckHead(t *testing.T) {
	head := append(peBytes(false), filler(HeadBytes-1024)...)
	fc := CheckHead("movie.mp4", false, "video/mp4", head)
	if fc == nil || fc.Level != model.FileCheckDanger || fc.Stage != model.FileCheckStageEarly || fc.Reason != "program_disguised" {
		t.Fatalf("CheckHead = %+v", fc)
	}
	want := detailDanger + " " + detailEarly
	if got := Detail(fc); got != want {
		t.Fatalf("Detail = %q, want %q", got, want)
	}
	// An archive under a media name is CAUTION until the final check lists it.
	zipHead := append([]byte("PK\x03\x04"), filler(100)...)
	if fc := CheckHead("movie.mp4", false, "", zipHead); fc == nil || fc.Level != model.FileCheckCaution || fc.Reason != "archive_disguised" {
		t.Fatalf("zip head = %+v", fc)
	}
}

func TestTexts(t *testing.T) {
	if Heading(model.FileCheckDanger) != "This file is not what its name says" ||
		Heading(model.FileCheckCaution) != "This may not be the file you wanted" ||
		Heading(model.FileCheckNote) != "" {
		t.Fatal("headings")
	}
	if LevelWord(model.FileCheckDanger) != "Danger" || LevelWord(model.FileCheckCaution) != "Caution" || LevelWord(model.FileCheckNote) != "Note" {
		t.Fatal("level words")
	}
	d := &model.FileCheck{Level: model.FileCheckDanger}
	if NotificationText(d, "movie.mp4") != "movie.mp4 is not what its name says." {
		t.Fatal(NotificationText(d, "movie.mp4"))
	}
	c := &model.FileCheck{Level: model.FileCheckCaution}
	if NotificationText(c, "video.mp4") != "video.mp4 may not be the file you wanted." {
		t.Fatal(NotificationText(c, "video.mp4"))
	}
	if Detail(c) != "" || Detail(&model.FileCheck{Level: model.FileCheckNote}) != "" {
		t.Fatal("CAUTION and NOTE have no detail")
	}
	inc := &model.FileCheck{Level: model.FileCheckCaution, ListingIncomplete: true}
	if Detail(inc) != "Downex could not list every file inside it." {
		t.Fatal(Detail(inc))
	}
	fc := checkBytes(t, "photo.jpg", append([]byte("\x89PNG\r\n\x1a\n"), filler(10)...), "")
	if ContentLabel(fc) != "PNG image" || ClaimLabel(fc) != "JPEG image" {
		t.Fatalf("labels %q %q", ContentLabel(fc), ClaimLabel(fc))
	}
	for _, r := range Reasons() {
		if len(r) == 0 || len(r) > 64 || strings.Trim(r, "abcdefghijklmnopqrstuvwxyz_") != "" {
			t.Fatalf("reason %q is not [a-z_]{1,64}", r)
		}
		if Summary(&model.FileCheck{Reason: r, Level: model.FileCheckDanger, Content: "pe", Claim: "mp4", Served: "video/mp4", Member: "a.exe"}, "a.exe") == "" {
			t.Fatalf("reason %q has no summary", r)
		}
	}
}

func TestEveryRunExtHasALabel(t *testing.T) {
	for _, e := range RunExts() {
		if extLabel(e).noun == "" {
			t.Errorf("run extension %q has no label", e)
		}
	}
	for k := range contents {
		if contents[k].label.noun == "" || contents[k].fam == famNone {
			t.Errorf("content %q", k)
		}
	}
}

func TestCheckFileReadErrorBeforeHead(t *testing.T) {
	_, err := CheckFile(context.Background(), failingReader{}, 1000, "a.mp4", false, "")
	if err == nil {
		t.Fatal("want an error when the head cannot be read")
	}
}

type failingReader struct{}

func (failingReader) ReadAt(p []byte, off int64) (int, error) { return 0, errTest }

var errTest = &testErr{}

type testErr struct{}

func (*testErr) Error() string { return "test read error" }
