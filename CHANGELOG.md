# Changelog

All notable changes to Downex are listed here, newest first. Versions follow
[semantic versioning](https://semver.org).

## Unreleased

## 1.1.1 - 2026-11-20

A bug-fix release: smoother updates, a cleaner Confirm download window, and Quit that closes all
of Downex.

### Fixed

- **Updates offer themselves.** Downex checks for a new version a few seconds after it starts,
  every 4 hours after that, and when you open its window, so the update pop-up appears by
  itself; no need to check in Settings.
- **Smooth in-app updates.** The update downloads in the background, with its progress in the
  pop-up and as a thin line across the top of the window. Closing the window no longer stops it,
  and pressing Update now a second time joins the running update instead of failing.
- **Quit closes all of Downex.** Quit in the tray (Windows) or the menu-bar icon (macOS), and
  ⌘Q in the window on macOS, now close every Downex window and stop Downex.
- **No more "Move to Trash" on macOS.** Once you have opened Downex, it removes the macOS
  quarantine flag from its own app and helpers, so browsers and the login item start them
  without asking again.
- **yt-dlp and Deno set themselves up silently.** Downex downloads and updates them in the
  background right after it starts, with no toast or notice.
- **The Confirm download window looks right.** It opens at its size, centred, with no empty
  frame first; it keeps the size you give it, cannot be stretched into an empty band, and in dark
  mode its title bar matches its body. A long video address is shortened in the middle, so the
  video's id stays visible.
- **The finish window always shows.** A download you start from the Confirm download window
  always gets its download complete window, also when it finishes in a moment; it comes above
  the browser.
- **A server that does not answer shows in the progress window.** When a download you watch fails,
  its progress window comes above the browser with the error and Retry, instead of a tray
  notification.
- **Browser extension:** the Chrome Web Store copy of the extension can talk to Downex, and the
  extension draws its icons without HTML markup (store review).

## 1.1.0 - 2026-11-20

### What's new

- **Save what's playing on social sites, with no login.** Click the Downex icon on a video on
  TikTok, Instagram, Facebook, Threads, VK and other sites, and Downex saves the video you are
  watching, with its sound, as one MP4. No account and no sign-in needed. (Chrome, Edge, Brave
  and other Chromium browsers; not yet in Firefox.)
- **A Download button on many more sites:** TikTok, Instagram, Facebook, X, Reddit, Pinterest,
  Threads, Snapchat, LinkedIn, Bluesky, Tumblr, 9GAG, Imgur, VK, Telegram and more, in feeds,
  reels, grids, carousels and stories. It is a small, tidy button that grows to "Download" when
  you point at it, and the best quality is marked **Recommended**.
- **Music and podcast pages** (SoundCloud, Bandcamp, Mixcloud and others) save their audio as
  `.m4a` or `.mp3`.
- **Carousels and pages with several videos:** pick the one you want from a list.
- **Save just part of a video** with a timeline trimmer: drag two handles, then download only
  that part.
- **Polite mode** slows Downex down while calls, games or streaming need the line, and speeds it
  back up when the line is quiet.
- **Rescue, renew and check:** finish a failed browser download from what the browser already
  saved, continue a download whose link expired with a new link, and get a warning when a
  downloaded file is not what its name says.
- **A Confirm download window above the browser**: a download you start in the
  browser asks in its own small window on top, without opening the main window.
- **Faster and safer:** a quicker start with a long download history, more reliable streams, and
  several security fixes.

### Save what's playing (Chromium browsers)

- The Download panel lists *This video* tagged *Playing*, at the quality the browser played, and
  *Download* saves exactly that video with its sound as one MP4 (H.264, HEVC, AV1 and VP9 with
  AAC are joined without re-encoding; a whole file is copied). Downex uses the files the page's own
  player already loaded for that video, found by its length; it needs no post address and no
  sign-in. This works on TikTok (For You, Explore tiles that play a preview, video pages),
  Instagram (reels, reel and post pages), Facebook (reels, videos), Threads, VK and any other site
  whose player streams the video. Not on YouTube, which keeps its own qualities, and not yet in
  Firefox (docs/SITE-MEDIA-SPEC.md §2.9). A part of the video cannot be chosen for such a save yet.
- The right post is saved: a player that loads the next post into the same element (TikTok For
  You) is probed again; a feed's neighbouring item, a preloaded next video of a similar length, a
  stream's single piece and a second post of the same length are never saved in its place; in a
  grid of previews the clicked tile's own stream leads.
- The panel names the clicked post ("author - caption"; never a feed's title, "<Site> video" when
  it has no name), shows both codecs of a video saved with its sound ("VP9 + AAC"), lists
  identical rows once, and says "Getting this video…" until the video has loaded. The dead-end
  texts "Open this video's own page to download it." and "Open this video's comments, then click
  Download again" are gone. Facebook titles no longer start with view and reaction counts.
- New option *Watch videos as they play* (on): each tab remembers for 30 minutes the addresses and
  sizes of the media files its pages loaded (never their contents or cookies); they reach Downex on
  this computer only when you click Download.

### Download button and panel on social sites

- On TikTok, Instagram, Facebook, X, Reddit, Pinterest, Threads, Snapchat, LinkedIn, Bluesky,
  Tumblr, 9GAG, Imgur, VK and Telegram (and their players embedded in other pages) the button is a
  compact mark, 34 px over players and 28 px on feed cards and grid tiles, placed in a corner free
  of the site's own controls. It widens to "Download" (and to the best known quality and size) when
  you point at it or reach it with the keyboard, shows a ring while it works and a check once the
  video was sent. YouTube's labelled button is unchanged.
- Videos get the button on more surfaces: muted looping feed clips, profile grid tiles, carousels,
  Instagram stories and highlights, and embedded Instagram, X, Facebook, Reddit, Pinterest and
  Bluesky posts. Facebook videos had no button before. Private messages never get one, and a post
  viewer or sign-up prompt over a feed hides the button of the video under it. On other sites a
  short background loop gets only a quiet mark while the pointer is over it.
- The highest quality with sound is badged **Recommended** and preselected (YouTube unchanged), and
  a played video is listed as soon as Downex matched it.
- TikTok panels list only the qualities the browser's player loaded; a video that has not played
  says "Play this video on its page, then download it".
- The extension knows more sites: Bluesky, Pinterest, LinkedIn, Kick, Bilibili, VK, Odysee,
  BitChute, 9GAG, Imgur, Telegram, Tumblr and Rumble, more Facebook, Instagram and X addresses, and
  share links such as `vm.tiktok.com`, `pin.it`, `b23.tv` and Reddit's `/s/`. Vimeo pages are sent
  as the player's address, which lists a public video without a sign-in.
- Two new right-click items: **Download video from this link with Downex** and **Show Downex
  download panel** (also for `blob:` players, small or looping videos and where the button is
  hidden).

### Sites and sign-in

- TikTok videos download. TikTok's video servers answer only web browsers, so a video sent from
  the browser's *Download* button is fetched by the browser itself in the video's tab, at the
  address the player used, and passed to Downex in acknowledged pieces of at most 256 KiB. It shows
  in the list like any other download (pause, resume, parts, file check). Keep a tab of the site
  open until it is saved; several such downloads run at once, and they resume after Downex or the
  browser restarted while a tab of the site is open. Other sites whose video servers refuse Downex
  go the same way. Nothing pretends to be a browser and no site check is solved.
- Public videos never use your sign-in. "Use my sign-in for videos that need it" is off for new
  installs (a choice you stored is kept) and is used for YouTube only: when a video needs a sign-in
  (age-checked or members-only, never a bot check), Downex asks once more on the same click with
  that one site's cookies, and the panel says so. The cookies are never stored or logged; the
  temporary cookie file only you can read is deleted after the run.
- More sites download through yt-dlp: Bilibili's split video and audio are joined; plainly named
  codecs (`h264`, `aac`, `bytevc1`) are read; Instagram reels without sound are saved and marked
  "No sound". Rows that name nothing and watermarked copies are left out. Bot protection reads
  "the site refused the request (HTTP 403, often bot protection)", and sites get their proper names
  (Bilibili, Kick, 9GAG, Odysee, Telegram, Fox News, "Apple Podcasts", "ABC News"; generic pages by
  their host).
- A refused site download in the window says why and what helps ("Open the video in your browser
  and click Download there") instead of offering a Retry that could not help.

### Audio pages

- A page without a video (SoundCloud, Bandcamp, Mixcloud, podcasts) saves its audio: AAC (a file,
  an HLS or a DASH stream) as `.m4a`, joined in pure Go without re-encoding, and MP3 as `.mp3`.
  Opus and Vorbis are refused with a clear reason (Downex has no FFmpeg), as are previews and raw
  `.aac` files. Expired stream links (SoundCloud's last about 5 minutes) are fetched again from the
  page. Audio goes to the Audio category; `downex media --site --list` and `--audio ID` handle it.
  There is no in-page button on audio players: use the popup or the right-click menu.

### Pages with several videos

- An Instagram carousel or a news page with several players lists its videos (up to 20) instead of
  being refused as a playlist. The browser panel shows "Video 2 of 3" with a tile per video and
  opens on the one you clicked; the popup, Confirm download and Add → Video have a "Video" choice;
  `downex media --site --list` shows them and `--item ID` picks one. A download keeps its video by
  the site's id, so it never saves another one when the page changed.

### Parts of a video

- Choose From and To (or a chapter) in the Confirm and Add dialogs, in the browser panel's timeline
  trimmer, or with `downex media … --from 1:20 --to 2:45` / `--chapter N`. Downex downloads only
  what the part needs and cuts the `.mp4` without re-encoding, named with the times
  (`Talk (01m20s-02m45s).mp4`). Playback starts at From; some players start up to a few seconds
  earlier, at the keyframe before it.
- The trimmer (YouTube and other sites): drag two handles (the player shows the frame under the
  handle), click a chapter, press *Start here* / *End here* or *Preview*, or type a time; the
  handles are keyboard sliders too. Sizes and lengths show the part's estimate ("1:25 selected ·
  ~90 MB at 1080p") in the panel, the dialogs and the details pane.
- An older Downex resuming such a download saves the whole video under the part's name.

### Rescue, expired links and file check

- **Rescue failed browser downloads:** when a download fails or stops in Chrome, Edge or Firefox,
  the extension's toolbar menu (and, if you turn it on, a notification) offers to finish it with
  Downex from the bytes already saved, after checking that the server still has the same file. In
  the app: Add ▾ → *Rescue partial file…*; on the command line: `downex rescue <file> <url>`.
- **Expired links continue:** a download whose link expired, was revoked or now leads to a web page
  keeps its data and shows "Link expired". Give it a new link (*New link…*, `downex renew`) or
  download it again from its page: Downex proves that the new link serves the same bytes before it
  continues. Settings → Downloads → *Expired links*: continue automatically (default), ask, or off.
  Ordinary file downloads only.
- **File check:** Downex warns when a downloaded file is not what its name says (for example a
  program named as a video, a double extension or hidden text-direction characters). The check only
  warns; it never changes, moves or deletes a file. Settings → Integration → Safety, and
  `downex check <file|id>`.

### Polite mode

- Off by default (Settings → Downloads, the status bar's speed-limit menu, the command palette,
  `downex settings --polite on`). Downex measures the line with small ICMP probes (TCP connect
  probes for servers that don't answer ping) to the servers it already downloads from, needs no
  administrator rights and sends nothing anywhere else. Its state shows in the status bar, the tray,
  `downex daemon status` and the extension popup (docs/POLITE-SPEC.md).

### Desktop window

- **Confirm download window:** a download you start in the browser, with confirmation on, opens a
  compact "Confirm download" window of its own (its own title bar and taskbar button) above the
  browser; the main window stays where it is. Several downloads are shown one at a time; *Start
  download* opens the progress window when that is on. Progress windows of browser downloads also
  open above the browser, without staying always on top or taking the keyboard focus. The WebView
  window (`-WebViewUI`) keeps the confirmation in its main window.
- A file whose name contains "#" or "?" ("Show #37.mp4", "game#1.exe") keeps its type: the right
  icon, and the confirmation before opening a program.
- The Confirm and Add dialogs show a part's own length for a video of unknown length (Instagram).

### Speed and reliability

- The download list loads about 2.7x faster with a long history (10,000 downloads in 87 ms instead
  of 236 ms), at start and whenever a new download picks a free filename.
- Streams whose playlists a CDN sends compressed (seen on Pinterest) open again: playlists are
  requested without a range and decompressed; a compressed answer to a fragment is refused.
- Pinterest streams no longer save without sound when only one quality's playlist was seen; a
  stream that really has no sound says "No sound".
- A failed or removed video download no longer leaves its `.dxparts` folder behind.
- The extension no longer leaves entries on the browser's Errors page ("Invalid downloadId",
  content scripts left over after an update, closed tabs and ports).

### Security and privacy

- The browser relay no longer accepts the Firefox add-on ID `browser@downex.local`, which Downex
  does not own; released extensions use `extension@downex.app`. Registering again removes the old
  ID from the browsers' host files.
- A signed Windows Downex installs an update only when its Setup is signed by the same publisher.
  Today's unsigned builds are unchanged: the signed update manifest checks them.
- Played videos are never fetched from this computer or the local network, including carrier-grade
  NAT and tailnet addresses (100.64.0.0/10), reserved and site-local ranges, and IPv6 addresses
  that carry a local IPv4 address. Only files the player fetched as media are read again, never on
  YouTube's hosts.
- Video files and streams chosen in a panel or the popup go without your cookies unless the sign-in
  option is on; a video that becomes DRM-protected while its panel is open is refused.

### API (additive, no breaking change)

- Proto: `MEDIA_ORIGIN_PLAYED`, `PlayedSource`, `ProbeMediaRequest.played`,
  `AddMediaRequest.played`, `TakeBrowserFetch`/`PutBrowserFetch`, `cookie_domain` on
  ProbeMedia/AddMedia, `MediaInfo.items`, `entry_id` and `items_left_out`, `RenewLink`,
  `Download.file_check`, and the settings `check_downloaded_files` and `renew_links`. Relay
  features `played`, `browserFetch`, `signIn` and `renew`, and the `items` list.

### Known limits and what was not checked

- Save what's playing works in Chromium browsers only; Firefox keeps the other rows. The browser
  fetch for TikTok and the new panels were not checked in Firefox.
- Checked on real sites, signed out, in a fresh Chrome profile: TikTok, Instagram reels and a
  carousel, a Facebook reel, SoundCloud, Bandcamp, Mixcloud and a CNN page. Not checked on the real
  sites: X, Reddit, Pinterest, Threads, Snapchat, LinkedIn, Bluesky, Tumblr and embedded posts,
  signed-in stories, long videos in a background tab.
- macOS: everything is built for macOS too, but Polite mode's probes, the Confirm download window,
  the part dialogs, expired links, rescue and the file check were not tried on a Mac.
- Expired links were not checked against real signed links, and Polite mode not on a real
  congested line.

## 1.0.0

The first public release of Downex, a free and open-source download manager for Windows and
macOS (GPL-3.0-or-later).

- **Downloads:** each file over up to 32 connections, HTTP/2, speed limits for everything, per
  queue and per download, and per-site connection limits. Progress is saved only once it is on
  disk, so a download continues after a pause, a quit, a crash or a restart; a file that changed
  on the server is detected and never mixed with the old one.
- **Videos and streams:** unencrypted HLS and DASH streams, and the video of a web page (YouTube
  and the other sites yt-dlp supports), saved as one `.mp4` file in the quality you pick, without
  re-encoding. Downex fetches and verifies yt-dlp and Deno from their publishers the first time
  they are needed. Encrypted and DRM-protected content is refused.
- **Browser extension** for Firefox, Edge, Chrome, Brave and Chromium: it hands downloads and
  videos to Downex with the page's sign-in, and can ask first for the name, folder and category.
- **Desktop app:** a native window on Windows and macOS with a progress window per download,
  queues with schedules and an action when they finish, categories with their own folders, a
  command palette, tray and menu-bar icons, notifications, a clipboard monitor, proxies and site
  logins, a virus-scan hook, and light and dark themes.
- **Command line:** `downex` does everything the window does.
- **Updates:** Downex checks its signed update feed and asks in its window whether to update now
  or later.
- **Privacy:** no account, no telemetry; the window, the command line and the extension talk to
  the background service only on your own computer.

Known limits: the Windows Setup is not code-signed and the macOS app is not notarised (see the
README for the first start); Windows on Arm and Linux have no release yet; the browser extension
is in no browser store yet (it is not on the Chrome Web Store, and the Firefox Add-ons and Edge
Add-ons listings are not ready), so it is added by hand: loaded unpacked in Developer mode in
Chrome, Edge, Brave and Chromium, and as a temporary add-on in Firefox, which removes it when
Firefox closes (see the README).
