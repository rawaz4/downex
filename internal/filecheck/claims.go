package filecheck

import (
	"slices"
	"strings"
	"unicode"
)

// family groups contents and claims for the verdict rules (§9.5).
type family int

const (
	famNone family = iota
	famProgram
	famScript
	famDisk
	famArchive
	famWeb
	famText
	famMedia
	famImage
	famDocument
	famOther
)

// label is a type's name with its indefinite article ("a", "Windows program").
type label struct{ article, noun string }

func (l label) full() string {
	if l.noun == "" {
		return ""
	}
	return l.article + " " + l.noun
}

func a(noun string) label  { return label{"a", noun} }
func an(noun string) label { return label{"an", noun} }

type contentInfo struct {
	label label
	fam   family
}

// contents is the label table of §9.6, keyed by contentKey.
var contents = map[string]contentInfo{
	"pe":            {a("Windows program"), famProgram},
	"mz":            {a("DOS program"), famProgram},
	"elf":           {a("Linux program"), famProgram},
	"macho":         {a("Mac program"), famProgram},
	"macho-fat":     {a("Mac program"), famProgram},
	"java-class":    {a("Java class file"), famOther},
	"script":        {a("script"), famScript},
	"msi":           {a("Windows installer"), famProgram},
	"msp":           {a("Windows installer patch"), famProgram},
	"mst":           {a("Windows installer transform"), famProgram},
	"ole":           {an("Office document (97–2003)"), famDocument},
	"lnk":           {a("Windows shortcut"), famProgram},
	"xar":           {a("Mac installer package"), famProgram},
	"deb":           {a("Linux package"), famProgram},
	"rpm":           {a("Linux package"), famProgram},
	"ar":            {an("ar archive"), famArchive},
	"zip":           {a("ZIP archive"), famArchive},
	"crx":           {a("browser extension"), famProgram},
	"rar4":          {a("RAR archive"), famArchive},
	"rar5":          {a("RAR archive"), famArchive},
	"7z":            {a("7z archive"), famArchive},
	"gzip":          {a("gzip archive"), famArchive},
	"xz":            {an("xz archive"), famArchive},
	"bzip2":         {a("bzip2 archive"), famArchive},
	"zstd":          {a("Zstandard archive"), famArchive},
	"tar":           {a("tar archive"), famArchive},
	"cab":           {a("Windows cabinet archive"), famArchive},
	"ooxml":         {a("Word / Excel / PowerPoint document"), famDocument},
	"odf":           {an("OpenDocument file"), famDocument},
	"epub":          {an("EPUB e-book"), famDocument},
	"apk":           {an("Android app"), famProgram},
	"jar":           {a("Java program"), famProgram},
	"appx":          {a("Windows app package"), famProgram},
	"ipa":           {an("iPhone app"), famProgram},
	"xpi":           {a("browser extension"), famProgram},
	"nupkg":         {a("software package"), famProgram},
	"vsix":          {an("extension package"), famProgram},
	"whl":           {a("Python package"), famProgram},
	"iso9660":       {a("disc image (ISO)"), famDisk},
	"udf":           {a("disc image (UDF)"), famDisk},
	"vhd":           {a("virtual hard disk"), famDisk},
	"vhdx":          {a("virtual hard disk"), famDisk},
	"pdf":           {a("PDF document"), famDocument},
	"rtf":           {an("RTF document"), famDocument},
	"isobmff":       {an("MP4 video"), famMedia},
	"isobmff-audio": {an("MPEG-4 audio file"), famMedia},
	"qt":            {a("QuickTime movie"), famMedia},
	"3gp":           {a("3GPP video"), famMedia},
	"heif":          {a("HEIF image"), famImage},
	"avif":          {an("AVIF image"), famImage},
	"craw":          {a("camera raw image"), famImage},
	"jp2":           {a("JPEG 2000 image"), famImage},
	"webm":          {a("WebM video"), famMedia},
	"matroska":      {a("Matroska video"), famMedia},
	"riff-avi":      {an("AVI video"), famMedia},
	"riff-wave":     {a("WAV audio file"), famMedia},
	"riff-webp":     {a("WebP image"), famImage},
	"riff-acon":     {an("animated cursor"), famImage},
	"ogg":           {an("Ogg media file"), famMedia},
	"flac":          {a("FLAC audio file"), famMedia},
	"asf":           {a("Windows Media file"), famMedia},
	"flv":           {a("Flash video"), famMedia},
	"mpeg-ps":       {an("MPEG video"), famMedia},
	"mpegts":        {an("MPEG transport stream"), famMedia},
	"m2ts":          {an("MPEG transport stream"), famMedia},
	"id3":           {an("MP3 audio file"), famMedia},
	"mpeg-audio":    {an("MP3 audio file"), famMedia},
	"adts":          {an("AAC audio file"), famMedia},
	"png":           {a("PNG image"), famImage},
	"jpeg":          {a("JPEG image"), famImage},
	"gif":           {a("GIF image"), famImage},
	"tiff":          {a("TIFF image"), famImage},
	"bmp":           {a("BMP image"), famImage},
	"ico":           {an("icon"), famImage},
	"psd":           {a("Photoshop image"), famImage},
	"html":          {a("web page"), famWeb},
	"xml":           {an("XML document"), famWeb},
	"svg":           {an("SVG drawing"), famWeb},
	"text":          {a("text file"), famText},
}

// zipKinds are the container kinds a ZIP's members reveal (§9.3).
var zipKinds = map[string]bool{
	"ooxml": true, "odf": true, "epub": true, "apk": true, "jar": true, "appx": true,
	"ipa": true, "xpi": true, "nupkg": true, "vsix": true, "whl": true,
}

// contentKey is the key of c in the label table and in claims: some ids are refined
// by their brand.
func contentKey(id, brand string) string {
	switch id {
	case "isobmff":
		switch brand {
		case "audio":
			return "isobmff-audio"
		case "qt", "3gp", "heif", "avif", "craw":
			return brand
		}
	case "zip":
		if zipKinds[brand] {
			return brand
		}
	}
	return id
}

func contentLabel(id, brand string) label {
	if id == "pe" && brand == "dll" {
		return a("Windows program library (DLL)")
	}
	return contents[contentKey(id, brand)].label
}

func contentFamily(id, brand string) family {
	return contents[contentKey(id, brand)].fam
}

// claim is what a name's extension (or a specific served type) says a file is (§9.4).
type claim struct {
	ext     string // the extension it came from; "" for a served type's claim
	label   label
	fam     family
	accepts map[string]bool // content keys; "*" accepts anything
	binProg bool            // a binary program: text instead of it is suspicious (R6)
	zipDoc  bool            // a ZIP-based document (A1)
	plain   bool            // a plain archive whose members are judged (A2)
}

func (c *claim) accepted(key string) bool {
	return c.accepts["*"] || c.accepts[key]
}

var claims = map[string]*claim{}

func set(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

func reg(fam family, l label, accepts map[string]bool, exts string, mods ...func(*claim)) {
	for _, ext := range strings.Fields(exts) {
		c := &claim{ext: ext, label: l, fam: fam, accepts: accepts}
		for _, m := range mods {
			m(c)
		}
		claims[ext] = c
	}
}

func binProg(c *claim) { c.binProg = true }
func zipDoc(c *claim)  { c.zipDoc = true }
func plain(c *claim)   { c.plain = true }

var (
	acceptMP4    = set("isobmff", "isobmff-audio", "qt", "3gp")
	acceptZipAny = set("zip", "ooxml", "odf", "epub", "apk", "jar", "appx", "ipa", "xpi", "nupkg", "vsix", "whl")
	acceptText   = set("text", "html", "xml", "svg", "script", "unknown")
	acceptLegacy = set("ole", "rtf", "html", "xml", "text") // Office web exports
	acceptOOXML  = set("zip", "ooxml", "ole")               // a password-protected one is a compound file
)

func init() {
	// Video and audio.
	reg(famMedia, an("MP4 video"), acceptMP4, "mp4 m4v f4v")
	reg(famMedia, an("MPEG-4 audio file"), acceptMP4, "m4a m4b m4p m4r f4a")
	reg(famMedia, a("QuickTime movie"), acceptMP4, "mov qt")
	reg(famMedia, a("3GPP video"), acceptMP4, "3gp 3g2")
	reg(famMedia, a("Matroska video"), set("matroska", "webm"), "mkv mk3d")
	reg(famMedia, a("Matroska audio file"), set("matroska", "webm"), "mka")
	reg(famMedia, a("WebM video"), set("webm", "matroska"), "webm")
	reg(famMedia, an("AVI video"), set("riff-avi"), "avi")
	reg(famMedia, a("WAV audio file"), set("riff-wave"), "wav")
	reg(famMedia, an("MP3 audio file"), set("id3", "mpeg-audio"), "mp3")
	reg(famMedia, an("AAC audio file"), set("adts", "id3"), "aac")
	reg(famMedia, a("FLAC audio file"), set("flac", "id3"), "flac")
	reg(famMedia, an("Ogg media file"), set("ogg"), "ogg oga ogv opus spx")
	reg(famMedia, a("Windows Media file"), set("asf"), "wma wmv asf")
	reg(famMedia, a("Flash video"), set("flv"), "flv")
	reg(famMedia, an("MPEG video"), set("mpeg-ps", "mpegts"), "mpg mpeg vob")
	// .ts is also TypeScript and Qt translations.
	reg(famMedia, an("MPEG transport stream"), set("mpegts", "text", "xml", "html", "script", "unknown"), "ts m2t")
	reg(famMedia, an("MPEG transport stream"), set("m2ts", "mpegts"), "m2ts mts")
	// Images.
	reg(famImage, a("JPEG image"), set("jpeg"), "jpg jpeg jpe jfif")
	reg(famImage, a("PNG image"), set("png"), "png")
	reg(famImage, a("GIF image"), set("gif"), "gif")
	reg(famImage, a("WebP image"), set("riff-webp"), "webp")
	reg(famImage, a("BMP image"), set("bmp"), "bmp")
	reg(famImage, an("icon"), set("ico", "png"), "ico cur")
	reg(famImage, a("TIFF image"), set("tiff"), "tif tiff")
	reg(famImage, a("camera raw image"), set("tiff"), "dng nef cr2 arw")
	reg(famImage, a("HEIF image"), set("heif"), "heic heif")
	reg(famImage, an("AVIF image"), set("avif"), "avif")
	reg(famImage, a("Photoshop image"), set("psd"), "psd")
	reg(famImage, a("JPEG 2000 image"), set("jp2"), "jp2")
	reg(famImage, an("SVG drawing"), set("svg", "xml"), "svg")
	reg(famImage, a("compressed SVG drawing"), set("gzip"), "svgz")
	// Documents.
	reg(famDocument, a("PDF document"), set("pdf"), "pdf")
	reg(famDocument, a("Word document"), acceptLegacy, "doc dot")
	reg(famDocument, an("Excel workbook"), acceptLegacy, "xls xlt")
	reg(famDocument, a("PowerPoint presentation"), set("ole"), "ppt pot pps")
	reg(famDocument, an("email message"), set("ole"), "msg")
	reg(famDocument, a("publication"), set("ole"), "pub")
	reg(famDocument, a("diagram"), set("ole"), "vsd")
	reg(famDocument, a("Word document"), acceptOOXML, "docx dotx docm dotm", zipDoc)
	reg(famDocument, an("Excel workbook"), acceptOOXML, "xlsx xltx xlsm xltm xlsb xlam", zipDoc)
	reg(famDocument, a("PowerPoint presentation"), acceptOOXML, "pptx potx pptm potm ppsx ppsm ppam sldx", zipDoc)
	reg(famDocument, a("diagram"), acceptOOXML, "vsdx", zipDoc)
	reg(famDocument, an("OpenDocument text"), set("zip", "odf"), "odt ott", zipDoc)
	reg(famDocument, an("OpenDocument spreadsheet"), set("zip", "odf"), "ods ots", zipDoc)
	reg(famDocument, an("OpenDocument presentation"), set("zip", "odf"), "odp otp", zipDoc)
	reg(famDocument, an("OpenDocument drawing"), set("zip", "odf"), "odg odf", zipDoc)
	reg(famDocument, an("EPUB e-book"), set("zip", "epub"), "epub", zipDoc)
	reg(famDocument, an("RTF document"), set("rtf"), "rtf")
	// Text and web.
	reg(famText, a("text file"), acceptText,
		"txt text log md csv tsv json yaml yml ini cfg conf srt vtt ass ssa m3u m3u8 pls cue nfo torrent")
	reg(famText, an("XML document"), acceptText, "xml")
	reg(famWeb, a("web page"), set("html", "text", "xml"), "html htm xhtml shtml mht mhtml")
	// Archives.
	reg(famArchive, a("ZIP archive"), acceptZipAny, "zip zipx", plain)
	reg(famArchive, a("comic book archive"), acceptZipAny, "cbz", plain)
	reg(famArchive, a("RAR archive"), set("rar4", "rar5"), "rar", plain)
	reg(famArchive, a("comic book archive"), set("rar4", "rar5"), "cbr", plain)
	reg(famArchive, a("7z archive"), set("7z"), "7z")
	reg(famArchive, a("gzip archive"), set("gzip"), "gz tgz")
	reg(famArchive, an("xz archive"), set("xz"), "xz txz")
	reg(famArchive, a("bzip2 archive"), set("bzip2"), "bz2 tbz tbz2")
	reg(famArchive, a("Zstandard archive"), set("zstd"), "zst tzst")
	reg(famArchive, a("tar archive"), set("tar"), "tar")
	reg(famArchive, a("Windows cabinet archive"), set("cab"), "cab", plain)
	// Program containers (the installer family).
	reg(famProgram, an("Android app"), acceptZipAny, "apk aab xapk apks")
	reg(famProgram, a("Java program"), acceptZipAny, "jar war ear")
	reg(famProgram, a("browser extension"), acceptZipAny, "xpi")
	reg(famProgram, a("Windows app package"), acceptZipAny, "appx appxbundle msix msixbundle")
	reg(famProgram, a("software package"), acceptZipAny, "nupkg")
	reg(famProgram, an("extension package"), acceptZipAny, "vsix")
	reg(famProgram, a("Python package"), acceptZipAny, "whl")
	reg(famProgram, an("iPhone app"), acceptZipAny, "ipa")
	reg(famProgram, a("browser extension"), set("crx", "zip"), "crx")
	// Disk images.
	reg(famDisk, a("disc image (ISO)"), set("iso9660", "udf"), "iso")
	reg(famDisk, a("disk image"), set("iso9660", "udf", "unknown"), "img")
	reg(famDisk, a("Mac disk image"), set("dmg", "unknown"), "dmg")
	reg(famDisk, a("virtual hard disk"), set("vhd"), "vhd")
	reg(famDisk, a("virtual hard disk"), set("vhdx"), "vhdx")
	// Programs.
	reg(famProgram, a("Windows program"), set("pe", "mz"), "exe sys scr cpl ocx efi mui", binProg)
	reg(famProgram, a("Windows program library (DLL)"), set("pe", "mz"), "dll", binProg)
	reg(famProgram, a("Windows installer"), set("msi", "msp", "mst", "ole"), "msi", binProg)
	reg(famProgram, a("Windows installer patch"), set("msi", "msp", "mst", "ole"), "msp", binProg)
	reg(famProgram, a("Windows installer transform"), set("msi", "msp", "mst", "ole"), "mst", binProg)
	reg(famProgram, a("Windows program"), set("*"), "com")
	reg(famProgram, a("Windows shortcut"), set("lnk"), "lnk", binProg)
	reg(famProgram, a("Linux program library"), set("elf", "script", "unknown"), "so", binProg)
	reg(famProgram, a("Linux program"), set("elf", "script", "unknown"), "bin run appimage", binProg)
	reg(famProgram, a("Mac program library"), set("macho", "macho-fat"), "dylib", binProg)
	reg(famProgram, a("Mac installer package"), set("xar"), "pkg", binProg)
	reg(famProgram, a("Linux package"), set("deb"), "deb", binProg)
	reg(famProgram, a("Linux package"), set("rpm"), "rpm", binProg)
	reg(famProgram, a("script"), set("script", "text"), "sh bash zsh command")
	reg(famProgram, a("script"), set("text", "script"), "py pl rb ps1 bat cmd vbs js")
}

// runExts (§9.4): programs, scripts, installers, shortcuts and disk images that the
// system runs, mounts or follows when they are opened.
var runExts = set(strings.Fields(`exe scr com pif cpl msi msp msix msixbundle appx appxbundle
	appinstaller application gadget msc bat cmd ps1 psm1 vbs vbe js jse wsf wsh wsc hta sct jar
	lnk url website scf reg chm hlp settingcontent-ms library-ms search-ms xll diagcab iso img vhd
	vhdx dmg pkg mpkg app command tool terminal workflow scpt scptd applescript webloc fileloc
	inetloc sh run appimage desktop`)...)

// RunExts returns the extensions (lower case, no dot) the name rules treat as programs
// (§9.4), sorted for tests.
func RunExts() []string {
	out := make([]string, 0, len(runExts))
	for e := range runExts {
		out = append(out, e)
	}
	slices.Sort(out)
	return out
}

// decoyExts (§9.4): the inner extensions of a double extension.
var decoyExts = set(strings.Fields(`pdf doc docx docm xls xlsx xlsm ppt pptx pptm odt ods odp rtf
	txt csv epub jpg jpeg png gif bmp webp heic tif tiff mp4 m4v mov avi mkv webm wmv flv mpg mpeg
	3gp mp3 m4a wav flac aac ogg wma zip rar 7z html htm`)...)

// runLabels name the run extensions that have no claim of their own.
var runLabels = map[string]label{
	"pif": a("Windows program"), "gadget": a("Windows program"), "application": a("Windows program"),
	"appinstaller": a("Windows app package"), "msc": a("Windows program"), "xll": a("Windows program"),
	"diagcab": a("Windows program"), "psm1": a("script"), "vbe": a("script"), "jse": a("script"),
	"wsf": a("script"), "wsh": a("script"), "wsc": a("script"), "hta": an("HTML application"),
	"sct": a("script"), "scf": a("Windows shortcut"), "url": a("shortcut"), "website": a("shortcut"),
	"webloc": a("shortcut"), "fileloc": a("shortcut"), "inetloc": a("shortcut"),
	"settingcontent-ms": a("shortcut"), "library-ms": a("shortcut"), "search-ms": a("shortcut"),
	"reg": a("registry file"), "chm": a("compiled help file"), "hlp": a("help file"),
	"mpkg": a("Mac installer package"), "app": a("Mac program"), "tool": a("script"),
	"terminal": a("script"), "workflow": a("script"), "scpt": a("script"), "scptd": a("script"),
	"applescript": a("script"), "desktop": a("shortcut"),
}

// extLabel names what an extension says a file is ("" label when nothing).
func extLabel(ext string) label {
	if c := claims[ext]; c != nil {
		return c.label
	}
	if l, ok := runLabels[ext]; ok {
		return l
	}
	if runExts[ext] {
		return a("program")
	}
	return label{}
}

// lastExt returns name's last extension in lower case, after trimming trailing dots
// and spaces as Windows does ("" = none).
func lastExt(name string) string {
	name = strings.TrimRight(name, ". ")
	i := strings.LastIndexByte(name, '.')
	if i <= 0 {
		return ""
	}
	return strings.ToLower(name[i+1:])
}

// genericTypes say nothing about what a file is.
var genericTypes = set("", "application/octet-stream", "binary/octet-stream",
	"application/force-download", "application/x-download", "application/unknown")

// servedExt maps specific served types to the extension whose claim they make.
var servedExt = map[string]string{
	"video/mp4": "mp4", "video/x-m4v": "m4v", "audio/mp4": "m4a", "audio/x-m4a": "m4a",
	"video/quicktime": "mov", "video/webm": "webm", "audio/webm": "webm", "video/x-matroska": "mkv",
	"audio/mpeg": "mp3", "audio/aac": "aac", "audio/flac": "flac", "audio/ogg": "ogg", "video/ogg": "ogv",
	"audio/wav": "wav", "audio/x-wav": "wav", "video/x-msvideo": "avi", "video/x-flv": "flv",
	"video/mpeg": "mpg", "video/mp2t": "m2ts", "video/x-ms-wmv": "wmv", "audio/x-ms-wma": "wma",
	"image/jpeg": "jpg", "image/png": "png", "image/gif": "gif", "image/webp": "webp",
	"image/bmp": "bmp", "image/tiff": "tiff", "image/heic": "heic", "image/heif": "heif",
	"image/avif": "avif", "image/svg+xml": "svg", "image/x-icon": "ico", "image/vnd.microsoft.icon": "ico",
	"application/pdf": "pdf", "application/rtf": "rtf", "text/rtf": "rtf",
	"application/msword": "doc", "application/vnd.ms-excel": "xls", "application/vnd.ms-powerpoint": "ppt",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   "docx",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         "xlsx",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": "pptx",
	"application/vnd.oasis.opendocument.text":                                   "odt",
	"application/vnd.oasis.opendocument.spreadsheet":                            "ods",
	"application/vnd.oasis.opendocument.presentation":                           "odp",
	"application/epub+zip": "epub", "application/zip": "zip", "application/x-zip-compressed": "zip",
	"application/vnd.rar": "rar", "application/x-rar-compressed": "rar", "application/x-7z-compressed": "7z",
	"application/gzip": "gz", "application/x-gzip": "gz", "application/x-xz": "xz", "application/x-bzip2": "bz2",
	"application/zstd": "zst", "application/x-tar": "tar", "application/vnd.ms-cab-compressed": "cab",
	"application/vnd.android.package-archive": "apk", "application/java-archive": "jar",
	"application/x-msdownload": "exe", "application/vnd.microsoft.portable-executable": "exe",
	"application/x-msdos-program": "exe", "application/x-dosexec": "exe", "application/x-msi": "msi",
	"application/x-iso9660-image": "iso", "application/x-apple-diskimage": "dmg",
	"text/html": "html", "application/xhtml+xml": "html", "text/plain": "txt", "text/csv": "csv",
	"application/json": "json", "application/xml": "xml", "text/xml": "xml",
	"text/javascript": "js", "application/javascript": "js", "application/x-sh": "sh",
}

// servedClaim is the claim a specific served type makes when the name makes none: the
// mapped extension's accepted contents, or a whole family for other video/, audio/,
// image/ and text/ types (§9.4). nil for generic and unknown types.
func servedClaim(mt string) *claim {
	if genericTypes[mt] {
		return nil
	}
	if ext, ok := servedExt[mt]; ok {
		c := *claims[ext]
		c.ext = ""
		c.label = servedLabel(mt, c.label)
		return &c
	}
	major, _, _ := strings.Cut(mt, "/")
	var fam family
	switch major {
	case "video", "audio":
		fam = famMedia
	case "image":
		fam = famImage
	case "text":
		return &claim{label: a("text file"), fam: famText, accepts: acceptText}
	default:
		return nil
	}
	acc := map[string]bool{}
	for k, info := range contents {
		if info.fam == fam {
			acc[k] = true
		}
	}
	return &claim{label: servedLabel(mt, label{}), fam: fam, accepts: acc}
}

// servedLabel is what a served type says a file is, as the summaries name it (§9.6):
// a video, an audio file, an image, a PDF document, a text file, a document.
func servedLabel(mt string, fallback label) label {
	major, _, _ := strings.Cut(mt, "/")
	switch {
	case major == "video":
		return a("video")
	case major == "audio":
		return an("audio file")
	case major == "image":
		return an("image")
	case mt == "application/pdf":
		return a("PDF document")
	case mt == "text/html" || mt == "application/xhtml+xml":
		return a("web page")
	case major == "text":
		return a("text file")
	case officeType(mt):
		return a("document")
	}
	return fallback
}

// officeType reports the Office document media types (§9.5 N4).
func officeType(mt string) bool {
	switch {
	case mt == "application/msword", mt == "application/rtf", mt == "text/rtf",
		strings.HasPrefix(mt, "application/vnd.ms-excel"),
		strings.HasPrefix(mt, "application/vnd.ms-powerpoint"),
		strings.HasPrefix(mt, "application/vnd.ms-word"),
		strings.HasPrefix(mt, "application/vnd.openxmlformats-officedocument."),
		strings.HasPrefix(mt, "application/vnd.oasis.opendocument."):
		return true
	}
	return false
}

// alarmFamily is the family a served type claims for rule N4 (famNone: not one of
// video/, audio/, image/, application/pdf or an Office type).
func alarmFamily(mt string) family {
	major, _, _ := strings.Cut(mt, "/")
	switch {
	case major == "video", major == "audio":
		return famMedia
	case major == "image":
		return famImage
	case mt == "application/pdf", officeType(mt):
		return famDocument
	}
	return famNone
}

// normType lower-cases a media type and drops its parameters.
func normType(t string) string {
	t, _, _ = strings.Cut(t, ";")
	return strings.ToLower(strings.TrimFunc(t, unicode.IsSpace))
}
