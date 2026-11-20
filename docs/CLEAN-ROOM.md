# Clean-room rules

Downex is an independent implementation. Its behaviour is specified in this repository's
`docs/*-SPEC.md` files, written in our own words from public protocol standards, published
documentation and Downex's own design.

- Implement only from the specs in `docs/` and from public standards (RFCs, W3C/WHATWG,
  browser extension docs, ISO/IEC media specs).
- Do not copy code, binaries, assets, icons, strings, data files, file formats, registry layouts,
  wire protocols or identifiers from any other product.
- Nothing taken from another product (its code, documentation, files or notes about it) is pasted
  into code, comments, tests or commit messages.
- Every feature PR cites the spec section (or public standard) it implements.
- No DRM/encryption circumvention. Downex's own code never deciphers site-specific signatures or
  solves site challenges, and contains no site extractor. It may run yt-dlp and Deno (which runs
  yt-dlp's challenge solver), third-party programs distributed separately and downloaded from their
  publishers, as external processes to resolve a web page (`SITE-MEDIA-SPEC.md`); their code,
  strings and data files are never copied or ported into Downex.
- The extension's site URL shapes (YouTube and the curated video sites of `VIDEO-DETECT-SPEC.md` §3.8)
  are written from the public addresses those sites show in the browser, never from yt-dlp's
  extractor patterns.
- Save what's playing (`SITE-MEDIA-SPEC.md` §2.9) reads only generic standards: HTTP ranges (RFC 9110),
  ISO-BMFF boxes (ISO/IEC 14496-12/14) of files the page's own player already fetched, and the browser's
  own request log and resource timing. It never reads a site's own data fields or page scripts, and it
  matches no site-specific DOM ids.
- Third-party dependencies must have compatible licences. Downex does not use FFmpeg: joining and
  remuxing media (TS → MP4, audio + video into one MP4) is pure Go (`internal/remux` with the
  mp4ff library, `MEDIA-SPEC.md` §5), without re-encoding. yt-dlp and Deno are never bundled: each
  user's Downex downloads and verifies them from the publishers' releases on first use
  (`SITE-MEDIA-SPEC.md` §4).
