# Downex design system: "Studio" (2026-10)

Status: **decided** (owner, 2026-10-28, final); **built** in every lane and through the final review
(2026-10-28: the review's high and medium findings fixed, see §8 "Final review"). This is the source of truth for the redesign of the desktop
window (`ui/frontend`, Svelte 5 + Wails v2) and the extension popup (`ext/src/entrypoints/popup`), with the
options and confirm pages aligned to the same tokens. It is a UI redesign: behaviour, bindings, wire formats,
privacy rules, the MP4-only rule and the one-row-per-quality rule stay as they are, except for the small,
listed host changes in §6.2 (frameless main window, `customTitleBar` startup flag, `FreeSpace` binding).

Sources:

- Three design studies were made for this redesign: **A "Glass"**, **B "Fluent"** and **C "Studio"**.
  They are kept outside this repository; the mockup names in this document (`C-studio/png/*.png`,
  `A-glass/*.png`, `B-fluent/*.png`, `C-studio/build.mjs`) are files of those studies.
- Direction **C "Studio"** is the base: its mockups are built by `C-studio/build.mjs` + `studio.css`,
  whose values this document refines.
- Grafted from **A "Glass"** (`A-glass/command-palette.png`, `A-glass/main-light.png`): the command palette
  (running downloads with mini progress, plain-language explanations of commands, Ctrl+Enter = show in
  folder) and the details pane's number layout (big speed, time left, "Done ≈ 11:59").
- Grafted from **B "Fluent"** (`B-fluent/main-light.png`, `main-dark.png`, `add-video.png`,
  `popup-page.png`): one box per connection with its own speed, a speed chart with a y-axis, a readable
  "When done: keep running" control, the inline Retry on failed rows, "More options" collapsed with a summary
  of what it hides, the "No cookies sent" chip, and the popup's per-site switches.
- The design review's faults in C are fixed here (§1.2).
- All of it is Downex's own design: no asset, icon or layout of another product is copied.

Reading order for a lane: §1 rules, §2 foundations, the components you use in §3, your screens in §4, the
contracts in §5 and §6, your lane in §7.

---

## 1. Rules

### 1.1 Owner rules (non-negotiable)

1. **Mint identity** in both themes, and colourful file icons that are big, solid and distinct for each
   category.
2. **No generic AI look**: no purple gradients, no glows or glowing or coloured shadows, no emoji, no
   gradient text, no aurora or blob backgrounds, no glassmorphism in the desktop app.
3. **Dense and useful like a classic desktop download manager**, and premium rather than basic: hairline structure, aligned tabular numbers,
   real data in every cell, nothing decorative that costs a row.
4. **Subtle, purposeful motion only**, and none under `prefers-reduced-motion` (§2.7).
5. Every video is one **MP4**, and quality lists show **one row (or card) per quality class** under the
   standard names (`ui/frontend/src/lib/quality.ts`, kept in sync with Go; never edit it).
6. Existing behaviour, bindings, wire formats and privacy rules stay. Where this document asks for a control
   that a binding already supports (global speed limit, the main queue's post-queue action, a download's
   speed limit), it uses the existing call and its existing validation.

### 1.2 Faults in the C mockups and how this spec fixes them

| Fault in C | Fix (section) |
| --- | --- |
| Only one list density | Comfortable/Compact switch, persisted per viewer; Comfortable is the default (§2.3, §3.11) |
| Crowded page header | Selection actions with labels; the bulk pair goes icon-only below 1366 px; everything else goes in "More" (§4.3) |
| Dark theme loses Mint and has weak structure | Mint-tinted chrome, Mint active nav, selection and live state; visible row dividers; a card border that is 1.46:1 against the work card (§2.1) |
| Documents icon looks like Google Docs (blue page, white lines) | New icon set with a distinct silhouette for each category and no "lines on a page" glyph (§2.6.3) |
| Popup download buttons have no label, titles are cut ("Openi…"), dead space | Labelled primary Download button, icon buttons with tooltip and accessible name, titles clamped to two lines, no fixed empty areas (§4.11) |
| Bare power icon for "when done" | Text control: "When done: keep running ▾" (§3.14) |
| Completed rows show "Completed · 25 Oct" | Completed rows show only "Completed"; the Added column has the date (§3.12) |
| Segmented row bar becomes a dotted line with many connections | Rows draw one solid fill; the progress window's connection map has at most 8 cells, each at least 12 px wide, under a fixed merge rule (§3.13) |

### 1.3 Research fixes that apply everywhere

- Numbers use the UI font with tabular figures (`font-variant-numeric: tabular-nums` on `body`).
  Monospace is used **only** for URLs and file paths.
- Active rows show a solid progress bar with the percentage in the same cell (the segment map is the progress window's).
- The details pane is gone when nothing is selected.
- Custom title bar with the logo (Windows main window; §4.2, §6.2).
- Status bar with a speed sparkline, global speed-limit menu, when-done menu and the free space of the
  download drive.
- Real empty state: paste a link, Add, get the extension.
- No radio dots in tab switches (segmented controls instead, §3.4).
- No raw OS dropdowns: a custom select menu that works from the keyboard (§3.5).
- Settings is a fixed-size window with side navigation: General, Downloads, Network, Integration, Video sites,
  Appearance (theme, density) and Updates, where the update URL sits under Advanced (§4.9).
- Add video is a video card with quality cards, one for each class (§4.6).
- The progress window and the complete dialog get a proper header (file icon, bold name, state, big %) and
  human codec text such as "1080p · H.264 · AAC 128 kbit/s" (§4.8).
- The demo data (`lib/mock.ts`) has no `.webm`, `.mkv`, `.mov`, `.flac` or `.ogg` rows (§7.1).
- One logo, the brand "D" of download segments (`internal/brand`), for the app and the extension (§2.6.4).

---

## 2. Foundations

### 2.1 Colour tokens

All colours are CSS custom properties on `:root` in `ui/frontend/src/app.css`. Dark mode applies through
`@media (prefers-color-scheme: dark) { :root:not([data-theme='light']) {…} }` and again under
`:root[data-theme='dark']`, as the file does today. The theme store sets `data-theme`. The extension defines
**the same names and values** in `ext/src/assets/ui.css`, through `prefers-color-scheme` only.

Measured contrast ratios (WCAG 2.x relative luminance) are listed where text sits on a ground. Text tokens
reach at least 4.5:1 on every ground they are used on. Component boundaries that identify a control reach at
least 3:1.

#### Grounds and lines

| Token | Light | Dark | Use |
| --- | --- | --- | --- |
| `--chrome` | `#eef2f0` | `#0a0e0c` | Title bar, sidebar, status bar, `html`/`body`. Mint-tinted on purpose |
| `--surface` | `#ffffff` | `#171e1b` | The work card (list), dialogs' body, popup body |
| `--surface-2` | `#f7f9f8` | `#1b2320` | Table header, details pane, dialog footer, palette footer, wells |
| `--raised` | `#ffffff` | `#212a26` | Menus, popovers, cards on a surface, segmented "on" thumb, toasts |
| `--field` | `#ffffff` | `#121815` | Text fields and select triggers |
| `--line` | `#dfe5e2` | `#303b36` | Card border, section dividers, header bottom line |
| `--line-soft` | `#e9eeeb` | `#27312c` | Row dividers (visible in dark: 1.26:1 on surface) |
| `--line-strong` | `#c9d1cd` | `#3d4943` | Outline buttons, chips, kbd, separators in the status bar |
| `--line-field` | `#808b85` | `#76837d` | Field and select borders: 3.53:1 on white; 4.55:1 / 3.73:1 on dark field / raised |

#### Ink

| Token | Light | Dark | Lowest measured contrast |
| --- | --- | --- | --- |
| `--ink` | `#0f1613` | `#e8efeb` | 16.3 / 12.5 |
| `--ink-2` | `#2f3b35` | `#c5d0ca` | 10.3 / 9.2 |
| `--muted` | `#56625c` | `#9eaba4` | 5.6 / 6.1 |
| `--faint` | `#65706a` | `#8b9891` | 4.56 (on chrome) / 4.72 (on selection) |

`--faint` is the smallest text tone. Never put it on `--press` (4.31:1) except for the moment of a click.

#### Mint (accent) and interaction

| Token | Light | Dark | Notes |
| --- | --- | --- | --- |
| `--accent` | `#10804f` | `#3ccb8b` | Fills: primary button, switch on, progress fill base, selection bar, focus |
| `--accent-hover` | `#0c6c42` | `#5ad79f` | |
| `--accent-press` | `#0a5a37` | `#2fb377` | |
| `--accent-text` | `#0b6a40` | `#6fdcaa` | Mint **text** (6.0 / 7.75 on its soft ground) |
| `--accent-on` | `#ffffff` | `#04210f` | Text on `--accent` (4.97 / 8.22) |
| `--accent-soft` | `#e2f3ea` | `rgb(60 203 139 / 0.14)` | Live chip, active tab count, quality card "on", field focus ring |
| `--accent-line` | `#a3d9bd` | `rgb(60 203 139 / 0.42)` | Borders of Mint-soft elements |
| `--sel` | `#eaf6ef` | `rgb(60 203 139 / 0.10)` | Selected row, active nav item, highlighted menu item |
| `--sel-hover` | `#e1f1e8` | `rgb(60 203 139 / 0.14)` | Selected and hovered |
| `--sel-line` | `#b3dec7` | `rgb(60 203 139 / 0.38)` | 1 px inset ring of a selection (selection is never shown by tint alone) |
| `--hover` | `#f2f6f4` | `rgb(255 255 255 / 0.035)` | Row and button hover |
| `--press` | `#e7ece9` | `rgb(255 255 255 / 0.06)` | Pressed |
| `--focus` | `#10804f` | `#6fdcaa` | Focus ring (4.40:1 on chrome, 4.97:1 on white; 10:1 in dark) |

The Mint identity in dark comes from: the green-black chrome, the brand tile, Mint on the active nav item
(tint, bar and icon), Mint selection with its 3 px bar, Mint progress and live speed, the primary button and
switches, and Mint focus rings. Grey-only states, such as a grey active nav item, are not allowed.

#### Semantic states

| State | Text token (light / dark) | Soft ground | Line | Mark (§2.6.2) |
| --- | --- | --- | --- | --- |
| Downloading / probing | `--accent-text` | `--accent-soft` | `--accent-line` | Filled Mint circle with a down arrow |
| Queued | `--muted` | `--surface-2` | `--line` | Clock outline |
| Paused | `--warn` `#935600` / `#e6aa4c` | `--warn-soft` `#fbf0dc` / `rgb(230 170 76 / 0.12)` | `--warn-line` `#e6c58c` / `rgb(230 170 76 / 0.4)` | Two bars |
| Failed | `--danger` `#bf3328` / `#f2857a` | `--danger-soft` `#fbe7e4` / `rgb(242 133 122 / 0.12)` | `--danger-line` `#efb3ab` / `rgb(242 133 122 / 0.4)` | Diamond with "!" |
| Completed | `--faint` | none | none | Check |
| To confirm (awaiting) | `--info` `#2a5dbd` / `#84aef7` | `--info-soft` `#e6eefc` / `rgb(132 174 247 / 0.13)` | `--info-line` `#b5c9f0` / `rgb(132 174 247 / 0.4)` | Rounded square with an arrow |
| Canceled | `--faint` | none | none | Dash |

Aliases for components: `--st-downloading`, `--st-queued`, `--st-paused`, `--st-failed`, `--st-completed`,
`--st-confirm` and `--st-canceled` point at the text tokens above. Measured on their soft grounds:
info 5.30 / 5.95, danger 4.75 / 5.65, warn 5.21 / 6.57.

#### Segmented progress

| Token | Light | Dark | Contrast with the track |
| --- | --- | --- | --- |
| `--seg-track` | `#dce3df` | `#2c3833` | |
| `--seg-fill` | `#178553` | `#34bd80` | 3.56 / 5.08 |
| `--seg-edge` | `#0b4a2f` | `#b5f3d5` | Live front of a connection (2 px) |
| `--seg-paused` | `#9c680c` | `#d3a24a` | 3.66 / 5.25 |
| `--seg-failed` | `#c8433a` | `#e86b60` | 3.73 / 3.90 |
| `--seg-idle` | `#8a958f` | `#5d6a64` | Queued with earlier progress |

#### File-type colours

Tiles carry white glyphs (`--tile-glyph: #ffffff` in both themes). `--c-*-deep` is the second tone of a tile
(the clapper arm, the carton lid, the back sheet, the hexagon's shaded half). It is computed once, not with
`color-mix` at runtime, so that canvases and the extension can read it.

| Kind | Light hue / deep | White glyph on light hue | Dark hue / deep | White glyph on dark hue |
| --- | --- | --- | --- | --- |
| `video` | `#6a45d4` / `#4e339d` | 6.15 | `#7f5fe6` / `#5943a1` | 4.49 |
| `audio` | `#cf3577` / `#992758` | 4.74 | `#e0508f` / `#9d3864` | 3.68 |
| `archives` | `#c85a12` / `#94430d` | 4.26 | `#d5702a` / `#954e1d` | 3.40 |
| `documents` | `#2f6fd8` / `#2352a0` | 4.79 | `#4a83e6` / `#345ca1` | 3.70 |
| `programs` | `#0b8496` / `#08626f` | 4.42 | `#178f9f` / `#10646f` | 3.84 |
| `images` (extension only) | `#5d7f12` / `#455e0d` | 4.66 | `#6a8f22` / `#4a6418` | 3.77 |
| `other` | `#727d77` / `#545d58` | 4.27 | `#76827c` / `#535b57` | 4.00 |

Every tile is at least 3.4:1 against `--surface` and `--chrome` in both themes. Token names are `--c-video`,
`--c-video-deep` and so on (the desktop has no `images` category; `categories.ts` stays as it is).

#### Brand, chart, scrim and shadows

| Token | Light | Dark |
| --- | --- | --- |
| `--brand-tile` / `--brand-hairline` / `--brand-bar` / `--brand-head` | `#1a221e` / `#34403a` / `#93e0ba` / `#cef1e0` | same (it is the app icon) |
| `--chart-line` | `var(--accent)` | `var(--accent)` |
| `--chart-fill` | `rgb(16 128 79 / 0.12)` | `rgb(60 203 139 / 0.14)` |
| `--chart-grid` | `#e3e8e5` | `#26302b` |
| `--scrim` | `rgb(14 22 18 / 0.38)` | `rgb(0 0 0 / 0.55)` |
| `--shadow-sm` | `0 1px 2px rgb(16 28 22 / 0.06)` | `0 1px 2px rgb(0 0 0 / 0.35)` |
| `--shadow-md` | `0 1px 2px rgb(16 28 22 / 0.06), 0 4px 12px rgb(16 28 22 / 0.07)` | `0 1px 2px rgb(0 0 0 / 0.35), 0 6px 16px rgb(0 0 0 / 0.3)` |
| `--shadow-lg` | `0 24px 64px rgb(12 22 17 / 0.22), 0 2px 6px rgb(12 22 17 / 0.08)` | `0 24px 64px rgb(0 0 0 / 0.6), 0 0 0 1px rgb(255 255 255 / 0.05)` |
| `--winctl-close` | `#c42b1c` (white glyph 5.66:1) | same |

Shadows are always neutral (black or green-black at low alpha). **Never** use an accent-coloured or blurred
coloured shadow; that is a glow. The only coloured outline is the solid focus ring.

#### Legacy aliases (temporary)

So that components work while lanes migrate them, `app.css` keeps a commented block **"legacy aliases:
remove after the redesign"** that maps the old names to the new tokens: `--bg`→`--chrome`,
`--surface-3`→`--press`, `--border`→`--line`, `--border-strong`→`--line-strong`, `--border-field`→`--line-field`,
`--text`→`--ink`, `--text-muted`→`--muted`, `--text-faint`→`--faint`, `--accent-strong`→`--accent-hover`,
`--accent-contrast`→`--accent-on`, `--selection`→`--sel`, `--selection-strong`→`--sel-hover`,
`--selection-edge`→`--sel-line`, `--focus-ring`→`--focus`, `--nav-hover`/`--row-hover`→`--hover`,
`--nav-active`→`--raised`, `--nav-active-edge`→`--line`, `--ok`→`--accent-text`, `--warn-edge`→`--warn-line`,
`--muted-state`→`--faint`, `--st-awaiting`→`--st-confirm`, `--progress-track`/`--seg-pending`→`--seg-track`,
`--progress-edge`→`--line-field`, `--progress-fill`/`--progress-done`/`--seg-done`→`--seg-fill`,
`--progress-paused`→`--seg-paused`, `--progress-failed`→`--seg-failed`, `--seg-front`→`--seg-edge`,
`--seg-gap`→`--surface`, `--spark-line`→`--chart-line`, `--spark-fill`→`--chart-fill`, `--cat-*`→`--c-*`,
`--shadow-1`→`--shadow-sm`, `--shadow-2`→`--shadow-lg`, `--backdrop`→`--scrim`, `--font-sans`→`--font-ui`,
and the old `--fs-*`, `--space-*`, `--radius-*`, `--dur-*` keep their values. Lanes write **only new token
names** in the files they own. The alias block is deleted once `rg -- '--(bg|text|border|cat-|fs-|space-|radius-)'
ui/frontend/src` finds no use (a follow-up for the foundation owner).

### 2.2 Typography

```css
--font-ui: 'Segoe UI Variable Text', 'Segoe UI', system-ui, -apple-system, 'Noto Sans', Ubuntu, Cantarell, sans-serif;
--font-display: 'Segoe UI Variable Display', 'Segoe UI Variable Text', 'Segoe UI', system-ui, -apple-system, 'Noto Sans', sans-serif;
--font-mono: 'Cascadia Mono', 'Cascadia Code', Consolas, ui-monospace, 'SF Mono', Menlo, 'DejaVu Sans Mono', monospace;
```

The root font size stays at the user's setting. Sizes are in rem (px values at the default 16 px):

| Token | rem | px | Weight / tracking | Use |
| --- | --- | --- | --- | --- |
| `--type-tag` | 0.625 | 10 | 650, +0.04em, caps | Codec tags (AV1, H.264), HLS/DASH tag |
| `--type-caps` | 0.65625 | 10.5 | 600, +0.07em, uppercase | Sidebar group heads, menu headings, popup sections |
| `--type-meta` | 0.71875 | 11.5 | 400 | Row sub-line (host · category), legends, connection speeds |
| `--type-small` | 0.75 | 12 | 400/500 | Status bar, hints, chips, kbd 11 |
| `--type-cell` | 0.78125 | 12.5 | 400/600 | Status cells, percentages, details values |
| `--type-body` | 0.8125 | 13 | 400/500 | Body, row names, buttons, menus |
| `--type-lead` | 0.875 | 14 | 500/650 | Palette rows, quality class name, field text in dialogs |
| `--type-section` | 0.96875 | 15.5 | 650, -0.01em, display | Details title, popup and dialog section titles, video title |
| `--type-dialog` | 1 | 16 | 650, -0.01em, display | Dialog titles |
| `--type-page` | 1.25 | 20 | 650, -0.02em, display | Page title in the work card |
| `--type-figure` | 1.75 | 28 | 650, -0.02em, display | Big numbers: details speed, progress %, complete-dialog size |

- `body { font: 400 var(--type-body)/1.4 var(--font-ui); font-variant-numeric: tabular-nums; }`.
- `.tnum` keeps the **UI font** and only sets `font-variant-numeric: tabular-nums` (today it switches to
  monospace). `.mono` is the only monospace class, for URLs and paths: `font: 400 0.75rem/1.4 var(--font-mono);
  letter-spacing: -0.01em`.
- Numbers and units: one non-breaking space between them (`5.8 MB/s`). Units follow `lib/format.ts`:
  **decimal** (the owner's rule of 2026-10-31): 1 KB = 1,000 B, 1 MB = 1,000,000 B, 1 GB = 10^9 B, labels
  B, KB, MB, GB, TB and B/s, KB/s, MB/s, GB/s, never KiB/MiB/GiB. A value that would round to 1000 of a
  unit shows in the next one (999,999 B → "1.0 MB"); exact byte counts stay exact. Speed-limit pickers
  offer KB/s and MB/s; typed K/M/G are decimal, KiB/MiB/GiB still binary. Stored values stay bytes.
- Truncation: names end in an ellipsis on one line in rows. Wherever space allows (popup titles, details title,
  progress-window name, video card title), names are clamped to two lines (`-webkit-line-clamp: 2`), never one.
- Weights in use: 400, 500, 600, 650. No 700 or more except the brand word (650 in display).

### 2.3 Spacing, sizing and density

Spacing is a 2/4 px grid: `--sp-1` 2, `--sp-2` 4, `--sp-3` 6, `--sp-4` 8, `--sp-5` 10, `--sp-6` 12, `--sp-7` 16,
`--sp-8` 20, `--sp-9` 24, `--sp-10` 32 px.

Main window metrics, with 1280×800 as the reference size (the default window stays 1200×760, minimum 900×600):

| Part | Size |
| --- | --- |
| Title bar | 44 px high |
| Sidebar | `--sidebar-w: 14rem` (224 px); nav rows 29 px; group heads 10.5 caps with 16 px above |
| Status bar | 32 px high |
| Work card | Starts at the sidebar's edge and under the title bar, ends above the status bar; 1 px `--line` border on top, left and bottom; radius 12 px on the inline-start corners; `--shadow-sm` |
| Page header (in the card) | 58 px |
| Table header | 30 px, `--surface-2`, lines above and below |
| Row, Comfortable | `max(44, round(rem × 2.875))` = 46 px: two lines (name 13/500; sub-line "host · Category" 11.5 `--faint`), file tile 24 px |
| Row, Compact | `max(24, round(rem × 1.5))` = 24 px: one line, file tile 16 px, no sub-line (host in the tooltip) |
| Details pane | Comfortable `--details-h: 14.125rem` (226 px); Compact `12.5rem` (200 px); collapsed by the user: 44 px header only; nothing selected: **not rendered** |
| Buttons | 30 px (md), 26 px (sm, popup and details actions), 32 px (dialog footer) |
| Icon buttons | 30×30 (title bar 32×30) |
| Fields and select triggers | 36 px in dialogs, 30 px in the settings grid and status-bar popovers, 26 px inline |
| Window controls | 46×44 each |

With Compact rows at 1280×800 and one download selected: 800 − 44 − 32 − 58 − 30 − 200 = 436 px, which is
18 rows. With nothing selected (no pane) 26 rows show. Comfortable shows about 9 rows with the pane and
13 without.

Density is a per-viewer choice: `localStorage['downex.density']` = `'comfortable'` (default) or `'compact'`,
applied as `<html data-density>` by `lib/stores/density.svelte.ts`, like the theme store. Storage failures
are ignored (the choice lasts for the session).

### 2.4 Radii

`--r-xs` 4 px (codec tags, segment cells' outer ends 3 px), `--r-sm` 6 px (inline selects, status-bar items,
kbd 5 px), `--r-md` 8 px (buttons, menu items 7 px, popup buttons 7 px), `--r-lg` 10 px (quality cards,
fields 9 px, segmented track 9 px), `--r-xl` 12 px (work card, video card, popovers 11 px, palette, popup
cards 11 px), `--r-2xl` 14 px (dialogs), `--r-pill` 999 px (chips, pills, switches).

### 2.5 Elevation

| Level | What | Treatment |
| --- | --- | --- |
| 0 | Chrome (title bar, sidebar, status bar) | Flat `--chrome`, no lines between its parts |
| 1 | Work card, details pane, video card, popup cards | `--surface` or `--surface-2` + 1 px `--line` + `--shadow-sm` |
| 2 | Menus, select popovers, tooltips, toasts, hover action pill | `--raised` + 1 px `--line` + `--shadow-md` (menus and toasts `--shadow-lg`) |
| 3 | Dialogs, command palette | `--raised` + 1 px `--line` (palette `--line-strong`) + `--shadow-lg`, over `--scrim` (dialogs) or a light dim (palette: `rgb(14 22 18 / 0.14)`, dark `rgb(0 0 0 / 0.35)`) |

The scrim may use `backdrop-filter: blur(1.5px)`, except under `prefers-reduced-transparency` and forced
colours. Nothing else is blurred.

### 2.6 Iconography

#### 2.6.1 Line icons (`Icon.svelte`)

Original line icons on a 24×24 grid: `stroke: currentColor`, `stroke-width: 1.6`, round caps and joins, no
fill (except `resume`, `resume-all` and `play`, which are filled). Sizes are 12, 14, 16 (default in buttons),
18 and 20. Colour: `--muted` beside text in ghost buttons, `--accent-text` for Mint actions, inherit
elsewhere. **Keep every existing name** (`logo` stays until §2.1's cleanup). Add these names (paths from
`C-studio/build.mjs`'s `IC` where it has them):

`tray` (All downloads), `queue`, `clock`, `gauge` (speed limit), `power` (when done), `drive` (free space),
`globe` (site), `history`, `filter`, `more` (three dots, stroke 2.6), `chevron-down`, `chevron-right`,
`chevron-up`, `check`, `external`, `paste`, `refresh`, `shield` (No cookies sent:
`M12 3.5l7 2.6v5.4c0 4.3-2.9 7.6-7 9-4.1-1.4-7-4.7-7-9V6.1z`), `play`, `density`
(`M4.5 6h15M4.5 10h15M4.5 14h15M4.5 18h15`), `sidebar-panel` (details pane), `win-minimise`
(`M6 12h12`), `win-maximise` (`M6.5 6.5h11v11h-11z`), `win-restore`
(`M8.5 8.5h9v9h-9zM6.5 15.5v-9h9`), `win-close` (`M6.5 6.5l11 11M17.5 6.5l-11 11`, stroke 1.3). The window
glyphs are drawn at 10 px in 46×44 buttons, like the Windows caption buttons, but they are our own paths.

`system` (the theme that follows the system: the title bar's theme button, Settings → Appearance, the
palette) is a **display on a stand** (0.8.2):
`M4.75 4.75h14.5a1 1 0 0 1 1 1v9.5a1 1 0 0 1-1 1H4.75a1 1 0 0 1-1-1v-9.5a1 1 0 0 1 1-1zM12 16.25v3.5M8.5 19.75h7`.
The earlier ring with a full-height bar had the silhouette of `info` and read as "!" at 14–17 px (the
half-filled disc it meant cannot show in a stroke-only set). Keep it distinct from `progress` and `stream`,
the other screens of the set (`Icon.test.ts`).

#### 2.6.2 Status marks (`StatusMark.svelte`, 14×14, filled)

Each status has its own colour **and** its own shape, so neither carries the meaning alone. The props and the
`MarkKind` names stay. From `C-studio/build.mjs` `MARK`:

- downloading: circle r6.2 in `--accent` with a down arrow in `--accent-on`
- queued: circle outline r5.3 in `--faint` with clock hands
- paused: two rounded bars in `--warn`
- failed: diamond in `--danger` with a white "!"
- completed: check stroke 1.7 in `--faint` (the existing draw-in `morph` stays)
- awaiting: rounded square r3.5 in `--info` with a white arrow
- canceled: a dash in `--faint`

#### 2.6.3 File-type tiles (`FileIcon.svelte`), the redesigned set

Each category has its own **silhouette**, so the six read apart at 16 px even in greyscale. They are solid
and two-tone (hue + deep) with white details, never a "page with lines". Sizes are 16 (Compact rows, sidebar,
popup file rows), 20, 24 (Comfortable rows, palette), 32 (details and progress headers), 40 (complete dialog)
and 48 (empty states). All share `viewBox="0 0 32 32"`. The component sets
`style="--hue: var(--c-<kind>); --deep: var(--c-<kind>-deep)"`, is `aria-hidden` unless given a `title`, and
uses `forced-color-adjust: auto`.

| Kind | Silhouette | Glyph | Why it is not a known product icon |
| --- | --- | --- | --- |
| video | Clapperboard: a body and an arm tilted 11° | Play triangle in the body; three slanted stripes on the arm | No rounded "play pill" (not a video-site logo) |
| audio | Disc (circle) | Five-bar waveform | No music note (not a music-store or player logo) |
| archives | Carton: dark lid over a lighter body | Tape strip down the lid, label slot | No zipper folder (not the OS "compressed folder") |
| documents | Two stacked sheets: deep back sheet offset up-right, front sheet with a folded corner | Bookmark ribbon | No blue page with white text lines (not an office suite's document) |
| programs | Hexagon with its right half shaded | Prompt `>_` | No 2×2 squares (not an OS logo), no black terminal window |
| images (extension) | Landscape rounded rectangle | Sun and two peaks | Generic picture metaphor in olive, not a photo app's pinwheel |
| other | Rounded square r7 | Three dots | |

The SVG bodies to use exactly (white = `var(--tile-glyph)`):

```html
<!-- video -->
<g transform="rotate(-11 4.5 10.5)"><rect x="4.5" y="5" width="24" height="5.6" rx="1.6" fill="var(--deep)"/>
  <path d="M9.2 5h3.4l-2.6 5.6H6.6zM16.2 5h3.4L17 10.6h-3.4zM23.2 5h3.4L24 10.6h-3.4z" fill="#fff"/></g>
<rect x="4.5" y="11.5" width="24" height="17" rx="3" fill="var(--hue)"/>
<path d="M14 16.1v7.8a.9.9 0 0 0 1.37.77l6.2-3.9a.9.9 0 0 0 0-1.54l-6.2-3.9A.9.9 0 0 0 14 16.1z" fill="#fff"/>
<!-- audio -->
<circle cx="16" cy="16" r="13.5" fill="var(--hue)"/>
<path d="M9.6 13.8v4.4M12.8 10.8v10.4M16 8.6v14.8M19.2 11.4v9.2M22.4 13.4v5.2" stroke="#fff" stroke-width="2.4" stroke-linecap="round" fill="none"/>
<!-- archives -->
<rect x="5" y="11" width="22" height="18" rx="2.8" fill="var(--hue)"/>
<rect x="3" y="4.5" width="26" height="8" rx="2.2" fill="var(--deep)"/>
<path d="M13.6 4.5h4.8v12.3l-2.4-1.5-2.4 1.5z" fill="#fff"/>
<rect x="9.5" y="21.5" width="13" height="3" rx="1.5" fill="#fff" fill-opacity=".9"/>
<!-- documents -->
<rect x="10" y="2.5" width="17.5" height="22" rx="2.6" fill="var(--deep)"/>
<path d="M7 6.5h10.2l6.3 6.3V27a2.5 2.5 0 0 1-2.5 2.5H7A2.5 2.5 0 0 1 4.5 27V9A2.5 2.5 0 0 1 7 6.5z" fill="var(--hue)"/>
<path d="M17.2 6.5l6.3 6.3h-4.1a2.2 2.2 0 0 1-2.2-2.2z" fill="#fff" fill-opacity=".45"/>
<path d="M8.5 6.5h4.6v9.2l-2.3-1.8-2.3 1.8z" fill="#fff"/>
<!-- programs -->
<path d="M16 3.2l11.1 6.4v12.8L16 28.8 4.9 22.4V9.6z" fill="var(--hue)" stroke="var(--hue)" stroke-width="2.4" stroke-linejoin="round"/>
<path d="M16 16.1l11.1-6.4v12.7L16 28.8z" fill="var(--deep)" stroke="var(--deep)" stroke-width="2.4" stroke-linejoin="round" opacity=".55"/>
<path d="M9.8 12.6l4.4 3.4-4.4 3.4M15.8 20.6h5.4" stroke="#fff" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" fill="none"/>
<!-- images (extension only) -->
<rect x="3" y="6" width="26" height="20" rx="4" fill="var(--hue)"/>
<circle cx="10.6" cy="12.2" r="2.5" fill="#fff"/>
<path d="M6.5 22.6l6.1-6.4 3.7 3.8 3.4-3.6 5.8 6.2v.4a1 1 0 0 1-1 1H7.5a1 1 0 0 1-1-1z" fill="#fff"/>
<!-- other -->
<rect x="4" y="4" width="24" height="24" rx="7" fill="var(--hue)"/>
<circle cx="10.4" cy="16" r="2" fill="#fff"/><circle cx="16" cy="16" r="2" fill="#fff"/><circle cx="21.6" cy="16" r="2" fill="#fff"/>
```

These were rendered at 16 to 48 px on all four grounds during the design (light surface and chrome, dark
surface and chrome) and read apart at every size. A custom category takes the tile of its `categoryKind`
(`lib/categories.ts`, unchanged).

#### 2.6.4 The logo (`BrandMark.svelte`), one for app and extension

The mark is the app icon from `internal/brand`: a "D" made of four download segments, square on the left and
round on the right, whose third bar is split, its last chunk still arriving. Tile `--brand-tile`, hairline
`--brand-hairline`, bars `--brand-bar` and the live chunk `--brand-head`, in **both** themes. It is exactly
the exe, taskbar, tray and extension toolbar icon. The mark on its 116×126 box:

```html
<path d="M0 0h68a12 12 0 0 1 0 24H0zM0 34h104a12 12 0 0 1 0 24H0zM0 68h74v24H0zM0 102h68a12 12 0 0 1 0 24H0z" fill="var(--brand-bar)"/>
<path d="M84 68h20a12 12 0 0 1 0 24H84z" fill="var(--brand-head)"/>
```

Layout, following `internal/brand` `appTile`:

- Below 32 px: a full-bleed tile with radius 0.219×size and the mark 0.625×size wide, centred, without a
  hairline (title bar 24 px, popup 22 px).
- From 32 px: the tile is inset by 8/256, with radius 52/256 and a hairline of max(1, 1.5/256×size); the
  mark is 116/256 of the size, centred.

The old chevron `logo` glyph and the popup's arrow-into-tray glyph are retired from brand use.
`ext/public/icon/*.png` are already generated from `internal/brand` by `ui/build/genicon.go` and do not change.

### 2.7 Motion

| Token | Value | Use |
| --- | --- | --- |
| `--dur-1` | 90 ms | Press |
| `--dur-2` | 120 ms | Hover and colour changes |
| `--dur-3` | 160 ms | Menus and popovers (rise 4 px + fade), segmented thumb slide, switch knob |
| `--dur-4` | 240 ms | Dialog in (scale 0.98→1 + fade), toast in (rise 8 px), details pane height |
| `--dur-5` | 320 ms | Row reorder (existing `transform` transition) |
| exit | 140 ms, `--ease-in` | Dialog and toast out (existing `Dialog.svelte` close) |
| `--ease-out` | `cubic-bezier(0.2, 0.8, 0.3, 1)` | Entrances, value tweens |
| `--ease-in` | `cubic-bezier(0.4, 0, 1, 1)` | Exits |

Allowed motion, all of it purposeful:

- Value tweens (`ticking`, 360 ms) of speed, time left and bar fill, for a lone change only: a value that
  changes again within 720 ms (a running download reports every 250 ms) is shown at once, so no frame loop
  runs while something downloads (2026-10-29: the loop cost about a CPU core in WebView2 and download speed).
- Row arrival (existing 900 ms Mint-soft tint fade) and departure ghosts.
- The completion check's draw-in.
- Menus rising, dialogs scaling in, the details pane opening. The sparkline is redrawn once per sample
  and does not slide (an SVG slide is a layout and paint per frame).
- Progress fills (rows' segment bars, the progress window's connection boxes) step with each report and
  do not slide: a 360 ms slide restarted every 250 ms kept the window animating for the whole download, and
  downloads ran about 5% slower with the window open (2026-10-30).

**Not allowed:**

- The Studio "sheen" across live bars.
- Blinking live edges.
- Pulsing status dots.
- Infinite decorative animation of any kind.

The only infinite animations are indeterminate states: the "Getting video info" spinner (0.9 s rotation) and
the unknown-size hatch drift (1.2 s linear). Both become static under reduced motion. Under
`prefers-reduced-motion: reduce` the existing global rule (durations of 0.01 ms) stays, and JavaScript
animations check `motionOk()`.

### 2.8 Accessibility

- **Contrast**: text at least 4.5:1 on every ground it uses (the tables in §2.1 hold the measured values);
  control boundaries and meaningful graphics at least 3:1 (fields `--line-field`, progress fill against the
  track, selection ring with the 3 px Mint bar). Percentages are always shown as text next to a bar, so the
  bar is never the only carrier.
- **Focus**: `:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }`. Exceptions:
  - Fields and select triggers: border `--focus` + `box-shadow: 0 0 0 1px var(--focus), 0 0 0 4px
    var(--accent-soft)`: a 2 px `--focus` edge (a hue change of the border alone is 1.4:1), then a solid soft
    spread, not a blur. In forced colours a 2 px `Highlight` outline (offset 1 px).
  - The keyboard's option in a listbox, palette or menu: the tint plus a 2 px inset `--focus` ring (the tint
    alone is under 3:1); the pointer's option only the tint. Small print on a tinted ground is `--muted`.
  - A scrolling region (the details pane's body) takes the focus when it overflows (`tabindex="0"`, a named
    region), so the arrow keys scroll it.
  - Table rows and cells: a 2 px inset outline (offset -2 px) on the cursor row and -3 px on the active cell.
  - The primary button: the ring stays outside, in `--focus`.
  - Window controls: a 2 px inset outline.
- **Keyboard**: everything a mouse can do has a keyboard path. Custom selects, menus, segmented controls,
  tabs, the palette and the grid follow the WAI-ARIA APG patterns given in §3. Hover-only conveniences (row
  hover actions, inline Retry, the Review chip) stay `aria-hidden` with `tabindex="-1"`. The keyboard has the
  same actions through Space, Enter, the context menu and shortcuts, as today.
- **Targets**: at least 24×24 CSS px (Compact rows are 24 px high; icon buttons 26 to 30 px).
- **Names**: icon-only buttons always have `aria-label` + `title`. The tooltip adds the shortcut only where
  one already exists ("Settings (Ctrl+,)"). The redesign adds no new global shortcuts; the mockups'
  Ctrl+Shift+P and Ctrl+L are not adopted.
- **Forced colours**: borders on segment cells, fields, chips and cards (`CanvasText`/`ButtonText`); the
  selection uses `Highlight`; file tiles keep their silhouettes; no information by colour alone.
- **RTL**: logical properties only (`inset-inline-*`, `padding-inline`, `margin-inline-start`); segment
  bars mirror (the canvas map already does).
- **Live regions**: unchanged. Rows never announce progress; "I" reads it on demand; toasts are polite;
  the connection banner alerts once.

---

## 3. Components

The **owner** is the lane that implements the component (§7). "F" is the foundation.

### 3.1 Buttons (F: classes in `app.css`)

| Variant | Class | Look |
| --- | --- | --- |
| Primary | `.btn.primary` | `--accent` fill, `--accent-on` 600 text, inset top highlight `inset 0 1px 0 rgb(255 255 255 / 0.18)` and `0 1px 2px rgb(8 60 36 / 0.25)` (light only). Hover `--accent-hover`, press `--accent-press` |
| Secondary | `.btn` | `--raised`, 1 px `--line-strong`, `--shadow-sm`, `--ink-2` 500 |
| Ghost | `.btn.ghost` | Transparent, `--ink-2`, icon in `--muted`; hover `--hover` |
| Danger | `.btn.danger` | `--danger` fill, white text in light, `#2a0a07` in dark (as today) |
| Danger text | `.btn.danger-text` | Ghost with `--danger` text |
| Icon | `.icon-btn` | 30×30 ghost, icon `--muted` → `--ink` on hover |

Heights are 30 (md), 26 (`.btn.sm`) and 32 (`.btn.lg`, dialog footers); padding-inline 11 px; gap 7 px;
radius `--r-md`. Unavailable actions in the toolbar use `aria-disabled="true"` (they keep focus, as today):
`--faint` text, icon at 70% opacity, no hover.

### 3.2 Split button (F: `SplitButton.svelte`)

A primary button and a 26 px chevron part (`aria-label="More ways to add"`, `aria-haspopup="menu"`,
`aria-expanded`) separated by a 1 px `rgb(0 0 0 / 0.14)` (dark 0.25) line. The menu opens below and aligns
to the end. Props: `label`, `icon`, `onclick`, `items: MenuItem[]`, `onselect(id)`, `disabled`, `title`,
`keyshortcuts`. The Add split button's menu: "Add downloads…" (Ctrl+N), "Add video or stream…". Both open
`AddDialog` in that mode.

### 3.3 Menus (F: `ContextMenu.svelte` generalised, `MenuButton.svelte`)

`ContextMenu` keeps its props, its `MenuItem` fields, its keyboard behaviour (arrows, Home and End,
type-ahead, Esc and Tab close) and its test. It gains these optional fields and props:

- `MenuItem.kind?: 'item' | 'radio' | 'checkbox'` (role `menuitemradio`/`menuitemcheckbox`, `aria-checked`,
  a check in a 14 px gutter in `--accent`)
- `MenuItem.heading?: string` (a caps heading above the item)
- `MenuItem.note?: string` (secondary text at the end, in `--faint`)
- a `footnote?: string` prop (11.5 `--faint` under a separator)
- an `anchor?: { el: HTMLElement; align: 'start' | 'end'; direction: 'down' | 'up' }` placement mode, which
  flips when there is no room and stays at least 8 px from the viewport

Item look: 30 px high, radius 7, highlight = `--sel` + inset `--sel-line`. Danger items use `--danger`.
Minimum width 220 px, padding 5 px, `--raised`, `--shadow-lg`, radius 11, rise animation `--dur-3`.

`MenuButton` is a trigger (any button variant) + an anchored `ContextMenu`: `aria-haspopup="menu"`,
`aria-expanded`, opens on click, Enter, Space or ArrowDown (ArrowUp opens on the last item), and returns
focus to the trigger on close.

### 3.4 Segmented control (F: `SegmentedControl.svelte`): picking a value

Two components with distinct jobs (0.8.2): a **segmented control picks one value** of a few; **Tabs**
(§3.4.1) **switch between views** of one place. One control never does both, and neither draws its state
twice.

A track with a `--chrome` ground, 1 px `--line`, 2 px padding and radius 9: 30 px high (as md buttons),
26 px for `sm`. Segments (24 / 20 px, 12 px padding, 12.5/500 `--muted`) are **equal-width grid columns**
(`--segments` = the number of options), each as wide as the widest label, so the thumb never changes
width between choices. The chosen segment is a `--raised` thumb (radius 7) with a
`0 0 0 1px var(--line-strong)` ring and `--shadow-sm`, its label **`--accent-text` at 600** (6.7:1 light,
about 8.8:1 dark on `--raised`) and its icon `--accent`: Mint carries the state, so it never rests on the
thumb's faint lift alone (§2.1) and there is **no bar under the label**. The thumb slides in `--dur-3` (none
under reduced motion); until it is measured the chosen segment draws the raised look itself. Forced
colours: a `Highlight` outline and label. **No radio dots, ever.**

- Native `<input type="radio">` inputs, visually hidden, inside `<label>`s styled as segments, in a
  `<fieldset>` with an sr-only `<legend>`. Native arrow-key behaviour applies; `getByLabelText`,
  `getByRole('radio')` and `.checked` keep working. Used for: Add mode (Files / Video or stream), proxy
  mode, theme, density, the schedule's days/once.
- `fill`: the control fills its container (up to `--seg-max`), the segments sharing it. With
  `selectWhenNarrow` as well, the control is a size container and, below 24rem of its own width, shows a
  `Select` of the same choices (named like the group) instead of the segments: the 4-option proxy mode.
- Props: `value` (bindable), `options: {value, label, icon?, count?, disabled?, title?}[]`, `name`,
  `legend` or `ariaLabel`, `size`, `disabled`, `fill`, `selectWhenNarrow`.

### 3.4.1 Tabs (F: `Tabs.svelte`): switching views

A full-width row on a 1 px `--line` hairline. Tabs are 32 px (`sm` 28 px, the progress window) with 12 px
(`sm` 10 px) padding, text 13/500 `--muted`; hover `--ink` on `--hover` with `--r-sm` top corners; the
chosen tab `--ink` at 600 with its icon in `--accent` (a hidden bold twin of each label keeps the tab as wide
as when chosen, so nothing shifts). **One indicator**: a 2 px `--accent` bar (radius `2px 2px 0 0`) on the
hairline, as wide as the chosen **label** and under it, that slides (`transform` and `inline-size`,
`--dur-3`, `--ease-out`; none under reduced motion). `--accent` is 4.97:1 on `--surface` in light and about
8.2:1 in dark (3:1 needed for a non-text indicator). Before it is measured (and without layout) the chosen
label underlines itself.

- `role="tablist"` (named by `ariaLabel`) with `role="tab"` buttons, a roving `tabindex`, arrows with
  automatic activation (RTL aware), Home and End, disabled tabs skipped; `aria-controls` = `panelId`; tab
  ids are `tabId(idPrefix, value)` for the panel's `aria-labelledby`.
- Focus: `2px solid var(--focus)`, offset −2 px. More tabs than room: the row scrolls sideways (no
  scrollbar, scroll snapping), its edges fade (`mask-image`), and the chosen tab is kept in view.
- Per tab: `icon`, `count` (the pill), `dot` (a 6 px Mint dot after the label, not read out: the
  progress window's "limit on"), `disabled`, `title`.
- `flush` (the progress window): the first label starts where the content beside it does. The row reaches
  into the container's padding by a tab's padding (12 px, `sm` 10 px) and the hairline stays under the
  content, so every tab keeps the same padding on both sides and its hover and focus marks are symmetric
  (never a first tab with a trimmed start padding).
- Forced colours: the hairline in `CanvasText`, the indicator and the dot in `Highlight`.
- Used for: the progress window's Status / Speed limit. The Settings side navigation takes the same look
  when the dialog is narrow (§3.20). Queue lists, the sidebar and the toolbar chips are not tabs.

### 3.5 Select (F: `Select.svelte`), the custom select menu

This replaces every native `<select>` in the desktop app (the extension builds the same pattern in vanilla
TypeScript, §4.11).

- **Trigger**: `<button type="button" role="combobox" aria-haspopup="listbox" aria-expanded aria-controls
  aria-activedescendant>` that also carries `value` and `data-value` attributes. It takes a normal
  `<label for={id}>`, so `getByLabelText('Queue')` finds it, and `.value` reads the value. Look: `--field`,
  1 px `--line-field`, radius 9, height 36 / 30 / 26 (`size="md" | "sm" | "inline"`), the chosen label (plus
  a `--muted` detail), and a chevron-down in `--muted` at the end. `inline` is borderless until hover, with a
  `--line` border on hover (details pane "Speed limit").
- **Popover**: `role="listbox"`, rendered in the component, **always in the DOM** and `hidden` while closed,
  `position: fixed` with coordinates computed from the trigger (so it escapes dialog overflow), 4 px under
  it, flipping upward when there is no room. The coordinates are made relative to the box fixed elements
  resolve to, read from a fixed probe at 0 on every side: the window, or an ancestor with a filter or a
  transform (a dialog's blurred backdrop starts under the 44 px title bar; review 2026-10-29: the lists
  opened 48 px under their trigger and ran off the window). Width at least the trigger's, max height 320 px with scroll; `--raised`,
  radius 11, `--shadow-lg`. Options are `role="option"` 30 px high with `aria-selected` and a Mint check
  gutter; `detail` is 12 px `--muted` after the label and `meta` (a size) sits at the end. Groups use
  `role="group"` + a caps heading. Disabled options use `aria-disabled`.
- **Keyboard** (APG select-only combobox):
  - Closed: Enter, Space, Alt+Down or Down opens on the selected option; Up opens on it too. Type-ahead
    selects without opening.
  - Open: Up and Down move; Home and End; PageUp and PageDown move by 10; type-ahead; Enter or Space selects
    and closes; Alt+Up selects and closes; Esc closes without change; Tab selects the active option and
    moves on. A click outside closes.
- **API**: `value` (bindable), `options: readonly SelectOption<V>[]` (`{ value, label, detail?, meta?,
  disabled?, title?, group? }`), `onchange?(value)`, `id`, `ariaLabel`, `ariaLabelledby`, `ariaDescribedby`,
  `disabled`, `invalid`, `size`, `icon?`, `placeholder?`.
- **Test helpers** (F, `lib/testing.ts`): `optionValues(combobox)`, `optionLabels(combobox)` and
  `async chooseOption(combobox, value)`. The last opens the select, clicks the option with that `data-value`
  and waits for a tick. Tests that used `HTMLSelectElement.options` or `fireEvent.change(select, …)` move to
  these.

### 3.6 Switch and checkbox (F)

- **Switch**: a native `<input type="checkbox" role="switch">` with class `.switch` (`appearance: none`),
  30×18, radius 999. Off: `--line-strong` track with a 1 px inset `--line-field` edge (3.5:1; the grey fill
alone is 1.5:1) and a white knob with `--shadow-sm`. On: `--accent` track.
  The knob slides in `--dur-3`. Rows use `Switch.svelte`: the label (13/500 `--ink`) and hint (12
  `--faint`) on the start side, the switch on the end; the whole row is the label (it toggles), at least
  44 px high. `.checked` keeps working in tests.
- **Checkbox**: native with `accent-color: var(--accent)`, 16 px (unchanged), for multi-choice lists and
  "Don't show this dialog again".

### 3.7 Chips, pills, tags and kbd (F: classes)

- `.chip`: 22 px, radius pill, 1 px `--line`, `--surface-2`, 12 px `--ink-2`, icon 12 px `--muted`. Facts
  such as "5:17", "Up to 4K", "Saved as MP4" and **"No cookies sent"** (shield icon; shown only where it is
  true, §4.6).
- `.chip.live`: `--accent-soft`, no border, `--accent-text` 600, with a 7 px dot. It does not pulse.
- `.chip` as a filter button in the page header (`aria-pressed`): "3 downloading", "7 queued" and
  "1 failed" (failed uses `--danger` text).
- `.pill` with the tones `ok`, `info`, `warn`, `fail` and `done`: 20 px, radius pill, 11/600, on the soft
  ground of the tone (`done` = `--chrome` + `--muted`). Used for states in the details header ("Downloading
  · 42%"), the popup's hand-offs and the progress-window header.
- `.tag`: 17 px, radius 5, 10/650 caps +0.04em, the hue at 11% (`color-mix` is fine here) with the hue as
  text (5.25:1). In dark the text is `color-mix(--tag-hue 55%, --ink)` (#aea0e8: 6.5:1 on a row, 5.5:1
  selected, 4.75:1 on the palette's current option), as in the extension. For HLS, DASH and the site name
  of a media download.
- `.codec`: 10/650 caps tag on `--chrome` with a `--line` border (AV1, H.264).
- `kbd`: 11/500 `--muted`, 1 px `--line-strong` with a 2 px bottom border, radius 5, `--surface`.

### 3.8 Fields (F: classes)

`.input`: `--field`, 1 px `--line-field`, radius 9, 36 px (`.input.sm` 30), padding-inline 11, 13 px `--ink`,
placeholder `--faint`. Focus per §2.8. `aria-invalid` gives a `--danger` border and a `--danger` 12 px
message under it. A field with a leading icon (globe, folder) puts the icon in `--faint` at 11 px from the
start. URL and path fields use `.mono` text. A "found" state shows a Mint check + "Found" 12/600 at the end
(Add video). Labels are 12.5/600 `--ink`, with hints 12 `--faint` at the end of the label row.

### 3.9 Dialog (F: `Dialog.svelte`, restyled, API kept)

`--raised`, radius 14, 1 px `--line`, `--shadow-lg`, over `--scrim`. The header has 16 px top padding, the
title in `--type-dialog`, and the close button (×, `.icon-btn`) at the end; with an `icon` and a description, the title
and the description form one block beside the icon (the `headerAside` after the title; 0.8.2: no line of
its own for the description). The body has 16 px vertical
padding, 20 px inline padding and a 16 px gap. The footer is `--surface-2` with a `--line` top border,
12 px vertical and 20/16 px inline padding; summary text 12.5 `--muted` with `<b>` in `--ink`; buttons at
the end, primary last.

New optional props: `icon` (snippet before the title: a file tile or a glyph), `headerAside` (snippet after
the title, for example the Add mode control), `footerStart` (summary snippet), `size: 'sm' | 'md' | 'lg' |
'xl'` (26 / 38 / 48 / 60 rem; `width` still works), `fixedHeight` (Settings, Queues: a least height), `flush` (no body padding,
for layouts that bring their own) and `ariaLabel` (an accessible name that differs from the visible title:
the Add dialog shows "Add" but stays named "Add downloads" / "Add video or stream"). Open and close animations and the focus trap stay.

#### Dialog sizing (0.8.2)

- **Sizes in rem**, so a dialog grows with the text: width `min(size, 100vw − 32px)`; a `fixedHeight`
  is a least height, `min-block-size: min(height, 100vh − --modal-top − 2 × --dialog-gap)`, in rem
  (`true` = 46rem): switching pages never lowers it, and a longer page makes the dialog taller, up to the
  window's room, with its top where it was (the space above it gives way only when the dialog needs it;
  a flex column on the backdrop). Settings is 70rem wide and at least 46rem high, Queues 72 × 48 rem; in
  the default window both are the room's height (684 px), in a 1920×1080 window a long page or queue
  grows into the room instead of scrolling. No fixed pixel heights.
- **The room around a dialog** (`--dialog-gap`) is 16 px, 8 px in a window under 640 px high (900×600,
  or 1200×760 at 125 % = 960×608): a dialog has 684 px at 1200×760, 676 in the owner's 1190×752, 548 and
  540 in the small windows.
- **The dialog is a size container** named `dialog` (`container: dialog / inline-size`). Content adapts
  with `@container dialog (…)` (or a part's own container: `settings-pane`, `queue-editor`, `quality`,
  `schedule`), never with viewport `@media` queries, which cannot fire inside a window that is at least
  900 px wide. Thresholds are in rem. A container cannot query itself (a layout of the container's own
  element keys to the dialog's width: Queues' editor). A container is no containing block for fixed
  popovers in current Chromium and WebKit, but the backdrop's blur (`backdrop-filter`) is: `Select`
  places its list against that box (§3.5).
- **Use the width before scrolling.** A dialog with more to show widens and puts parts side by side:
  Confirm download and Add → Video or stream with qualities are 56rem with the qualities in a column of
  their own (§4.6, §4.7), a file's Confirm 44rem, Queues puts the schedule beside the settings (§4.10),
  Settings' long pages take two columns (§3.20). The order of the parts in the page is the order Tab
  takes them; CSS places them (grid areas or columns), so reading and keyboard order stay together.
- **One scroll region per dialog**: the body (or, in a flush layout, its one content pane). A list inside a
  scrolling area does not scroll on its own (Queues' Order list, Move to queue's list): the dialog grows
  to the window first. Two panes side by side may each scroll, never one inside the other (Queues' list
  of 14 queues beside its editor in a small window, as Settings' navigation beside its pane).
- **The fit rule** (2026-10-29, the owner's Confirm download with six qualities scrolled by 114 px in a
  1190×752 window): no dialog shows a scrollbar in any of its content states at 1200×760 and 1190×752,
  with 16 and 20 px text, light and dark; nor at 960×608 (125 %) and 900×600, unless the state's content
  provably cannot fit there (`ALLOW` in the script, each entry with its heights). Content without a bound
  (a long list: 14 queues, a queue's Order, host rules and site logins, the notices, the palette) may
  scroll. What scrolls does so in one region, in a dialog grown to the window's height (1920×1080 too),
  with its footer in view. Every Settings page fits the default window at 16 px text (Downloads with its
  default categories, Video sites, About: §3.20); the long forms at 20 px text (Settings → Downloads,
  Integration, Video sites, About; Queues' schedule) are in `ALLOW`.
- **What the fit rule also holds** (review 2026-10-29): text cut off by an ellipsis or a line clamp has its
  whole text in the tooltip the pointer gets there; a field's label never breaks onto two lines (a label
  row puts its hint under the label when there is no room beside it); a folder field shows its whole
  folder; a video's card stays above its qualities, and no quality column is empty; a `Select`'s list
  opens 4 px from its trigger, inside the window.
- **Checks** (`ui/frontend/scripts/measure-dialogs.mjs`, headless Chrome or Edge on the demo UI with
  `?fixture=` states from `lib/mock.ts`; jsdom has no layout): checks 1 to 6 as before (Settings General,
  Appearance and Updates at 1200×760; Queues' one scroll region; every dialog fits 900×600 at 16 and 20 px
  without sideways scrolling or a few pixels of scrolling; the progress views at 100, 125 and 150 %; Add →
  Video found; the progress tabs), and check 7, the fit rule above, over 87 states (30 Confirm, 18 Add,
  Download again, Restart, Remove, Open, Move to queue, 9 Queues, 12 Settings, the palette and 9 progress
  views) at 1200×760, 1190×752, 960×608 (125 %), 900×600 and 1920×1080, with the checks of the list
  above in every state and each `Select` opened in the states marked `selects`.
  `node scripts/measure-dialogs.mjs` (exit code 1 on a failure; about 45 minutes); `--only`,
  `--fit-only`, `--breakdown`, `--json FILE`, `--fail-shots DIR`, `--state-shots DIR [--shots-like DIR]`,
  `--root DIR` (a frozen copy, so the tree can change while it runs).

### 3.10 Progress primitives (F)

- `ProgressBar.svelte` (unchanged API): a single 6 px bar (8 px in dialogs), track `--seg-track`, fill by
  state, % text 12.5/600 tabular. Used by the tools toast, the Add dialog's setup and the progress window
  when there are no segments.
- `SegmentBar.svelte` (new): the row's solid bar and the progress window's connection map, under §3.13.
- `SegmentMap.svelte` (API kept): the whole-file canvas map of the progress window's details. It reads the
  new tokens (`--seg-track`, `--seg-fill`, `--seg-edge`, `--surface` for gaps).
- `Sparkline.svelte` (API kept + `variant?: 'panel' | 'mini'`): `mini` is 64×16, has no axes and no
  baseline, uses the Mint line and fill, and serves the status bar. The details pane's chart with a y-axis is
  `SpeedChart.svelte` (L2).

### 3.11 Table rows: Comfortable and Compact (L2)

Columns, in order (rem widths; resizable and dropped from the right as today): Name (flexible, min 12),
Size 5, Progress 11, Speed 5.5, Time left 5, Conn. 3.25, Status 9.5, Added 4.75. Numbers align to the end,
use tabular figures, and are 13 `--ink-2`. Live speed is 600 `--ink`. Done sizes and the Added date are
`--faint`.

| Part | Comfortable (default) | Compact |
| --- | --- | --- |
| Height | 46 px | 24 px |
| Name cell | Tile 24 + 11 px gap; name 13/500 `--ink` + tag; sub-line "host · Category" 11.5 `--faint` | Tile 16 + 8 px gap; name 13/400 `--ink` + tag; no sub-line |
| Bar | 6 px high | 4 px high |
| % text | 12.5/600 | 12/600 |
| Status | Mark 14 + text 12.5 | Mark 12 + text 12.5 |
| Divider | 1 px `--line-soft` | 1 px `--line-soft` |

Host = `URL(finalUrl || url).hostname` without `www.`; Category = the download's category name (or the
kind's label). Both are escaped for bidi as today.

- Hover: `--hover`.
- Selected: `--sel` + `inset 0 0 0 1px var(--sel-line)` + a 3 px `--accent` bar at the inline start (7 px
  from top and bottom, rounded on its outer end).
- Keyboard cursor: per §2.8.
- Hover actions (existing pause / resume / confirm / folder + remove): a `--raised` pill with
  `--shadow-md`, 26 px buttons, at the inline end over Status and Added, appearing in `--dur-2`.

### 3.12 Row cells by state (L2)

| State | Progress cell | Speed / Time left / Conn. | Status cell |
| --- | --- | --- | --- |
| downloading | SegmentBar (fill) + % | Live values (tweened); Conn. = `d.connections` | Mark + "Downloading" in `--accent-text` 600 |
| probing | Indeterminate bar | empty | Mark + `statusText` ("Getting video info" / "Connecting") |
| queued, no progress | empty | empty | Clock mark + "Queued #n" (`--ink-2`, `#n` in `--faint`) |
| queued with progress | SegmentBar (`--seg-idle`) + % `--muted` | empty | as above |
| paused | SegmentBar (`--seg-paused`) + % in `--warn` | empty | Mark + "Paused" `--warn` 600 |
| failed | SegmentBar (`--seg-failed`) + % in `--danger` | empty | Mark + "Failed" `--danger` 600 + inline **Retry** link button (`--accent-text` 600, B). "Needs restart" (validator_changed) shows **Restart…** instead; no inline action when `cannotRetry(errorCode)`. The reason is in the cell's tooltip (`d.error`, as today) and the details pane |
| awaiting_confirmation | **Review ›** chip (`--info` text, `--info-soft`, 1 px `--info-line`, 24 px, radius 7) | empty | Mark + `statusText` (stays "Waiting for confirmation") in `--info` 600 |
| completed | **empty** | empty | Check + **"Completed"** only (`--faint`); the date is in Added |
| canceled | empty | empty | Dash + "Canceled" `--faint` |

Retry, Restart… and Review are mouse conveniences like the hover actions (`data-row-action`, `aria-hidden`,
`tabindex="-1"`). The keyboard uses Space (resume), the context menu ("Resume", "Restart…") and Enter
(confirm), which are unchanged. Retry calls the existing `onaction('resume', d)`; Review calls
`onaction('confirm', d)`; Restart… calls a new optional `onaction('restart', d)`, which is added to
`RowAction`.

### 3.13 Segmented progress bar and the merge rule (F: `lib/segments.ts` + `SegmentBar.svelte`)

The row bar shows how a file arrives without ever becoming a dotted line.

```ts
export interface SegCell { readonly fraction: number; readonly live: boolean }
/** At most `maxCells` equal byte spans of the file; each span's fill = its received bytes / its length. */
export function segmentCells(d: Pick<Download,'size'|'received'|'state'|'segments'|'kind'>, maxCells = 8): SegCell[]
```

1. Unknown size (`size <= 0`): a single indeterminate cell (hatched while downloading and `received > 0`).
2. Media downloads (`kind === 'media'`) and downloads without segments: a single cell with
   `fraction = progressFraction(d)`, `live = state === 'downloading'`.
3. Otherwise `n = clamp(segments.length, 1, maxCells)`, and the bar is split into `n` **equal byte spans**
   `[k·size/n, (k+1)·size/n)`. For each span, `received` = the sum over segments of the overlap of
   `[s.start, s.start + min(s.received, len(s)))` with the span (len uses `end < 0 ⇒ size − 1`, as
   `SegmentMap` does). `fraction = received / spanLength`, snapped to 1 at ≥ 0.995. A started cell
   (fraction > 0) is drawn at least 2 px wide. `live` = downloading and some segment whose front
   `s.start + s.received` (with `s.received < len`) lies in the span.
4. **List rows** (`row`, `compact`, `mini`) draw `rowCell(d)`: **one solid fill** of the overall
   progress (`progressFraction`, full when completed, indeterminate when unknown), however many
   connections there are. Eight connections each 70% through their range would otherwise read as a
   dashed line; the connection layout is the progress window's map and the details pane's Parts boxes.
5. The **progress window** (`lg`) draws `barCells(d, width)` = `[rowCell(d)]` (owner's choice
   2026-11-10, was the connection map): **one solid fill** of the overall progress in every state and
   for every kind of download, as the rows draw it, with the live edge while it runs. The connection
   layout is the details' segment map (Show details) and the details pane's Parts boxes.

Drawing: cells are flex items with widths proportional to their span (equal), separated by 2 px gaps that
show the row ground, with track `--seg-track` and a fill from the inline start in the state colour (§3.12).
The outer ends have radius 3 px and the inner corners are square. A live cell has a static 2 px `--seg-edge`
line at the end of its fill. There is no sheen and no blinking. The bar is decorative (`aria-hidden`); the %
text is the value (the row's "I" announces it as today).

Size variants: `row` (6 px), `compact` (4 px), `mini` (4 px, 90 px wide, for palette rows).

### 3.14 Status bar (L1)

32 px, on `--chrome`, 12 px `--muted`, items 24 px high with radius 6 and 8 px padding; buttons get `--hover`.
1 px `--line-strong` separators, 14 px high. From the start:

1. **Connection**: a 7 px dot (`--accent` connected, `--warn` connecting or demo, `--danger` down) +
   "Connected" / "Connecting…" / "downexd unavailable" / "Demo mode". The tooltip has the version and the
   error (as today's text).
2. **Speed**: a `Sparkline variant="mini"` of the total speed over the last 60 s
   (`DownloadsStore.totalHistory`, F) + "↓ 17.9 MB/s" (the number 600 `--ink`, arrow in `--accent`).
3. "3 active · 7 queued".
4. (end-aligned) **Global speed limit**: a `MenuButton` with the gauge icon + "Unlimited ▾" or
   "Limit 10 MB/s ▾" (`--warn` text while limited). The menu, which opens up, has the heading "Global
   speed limit" and radio items Unlimited, 20, 10, 5 and 1 MB/s; then "Custom…", which opens Settings →
   Downloads with the limit field focused; and the footnote "Per-download and queue limits still apply."
   Choosing an item does the full settings round-trip: `getSettings()`, `{...s, globalSpeedLimit}`,
   `updateSettings()`, `applySettings()`, then a toast "Global speed limit: 10 MB/s". It is disabled
   while stale.
5. **When done**: a `MenuButton` with the power icon + **"When done: keep running ▾"**, and for the other
   actions "When done: quit Downex", "… sleep", "… hibernate", "… shut down". Power actions show in
   `--warn`. The menu has the heading "When the main queue is done" and radio items from
   `postActionChoices(capabilities, current)`, labelled "Keep running" (none), "Quit Downex", "Sleep",
   "Hibernate" and "Shut down", then "Other queues…", which opens the queue manager. Choosing an item calls
   `queues.update(MAIN_QUEUE_ID, {...spec, postAction})`. The control is hidden when queues are unsupported
   and disabled while stale. The 60 s countdown banner is unchanged.
6. **Free space**: the drive icon + "D: 230 GB free". The label is the volume of `settings.defaultDir`:
   `X:` for a Windows drive, "Network folder" for UNC, else the folder's last component.
   - Value: from `api.freeSpace(defaultDir)` (F, §6.1), read at start, when the settings change, after each
     completion, and every 60 s while the window is visible.
   - Warning: the item turns `--warn` with the tooltip "Not enough free space for the active and queued
     downloads (about N more needed)" when free < Σ(size − received) of downloading, queued and paused
     downloads of known size.
   - Hidden when the binding is missing (`unimplemented`) or the read fails.
7. "28 items · 1 selected" (as today, with "of N" when filtered).

### 3.15 Details pane (L2)

The pane is **not rendered** when nothing is selected. With several selected, it is a 44 px strip:
"3 downloads selected · 4.2 GB" plus Pause, Resume and Remove (ghost sm). With one selected, the full pane
(height per density) on `--surface-2` with a `--line` top border:

- **Header** (14 px top padding, 20 px inline):
  - Tile 32, then the name in `--type-section` (2-line clamp) with the sub-line "youtube.com · Video ·
    1080p (Full HD) · H.264 + AAC" (12 `--muted`; the quality part comes from `codecSummary` for media),
    and a state pill ("Downloading · 42%").
  - At the end: Pause or Resume (`.btn.sm` outline; a completed download has "Download again…" with the
    restart icon there, 0.8.2, §4.7), Show in folder (ghost), Copy link (ghost), ⋯ (opens the
    same context menu as the row, anchored to the button) and the collapse chevron (keeps
    `downex.detailsCollapsed`).
  - Awaiting downloads show "Review…" (primary sm). Failed downloads show Retry, Restart… or Add site login…
    as today.
- **Body**, three columns (1.32fr / 1fr / 0.92fr), separated by 1 px `--line`:
  1. **Facts** (`dl` grid, 76 px labels 12 `--faint`, values 12.5 `--ink-2`):
     - Address: `.mono` with an ellipsis and a copy icon.
     - Saved to: `.mono` with a folder icon.
     - Received: "1.43 GB of 3.40 GB · 42%" with the number 600 `--ink`.
     - Speed limit: an inline `Select` (Unlimited, 20, 10, 5 and 1 MB/s, "Custom…" opens the progress
       window's Speed limit tab). It calls `api.setDownloadOptions(id, {speedLimit})` through a new optional
       `onlimit` prop; completed downloads show text only.
     - Queue: "Main queue · position 2".
     - Added: "Today, 14:02 · from Chrome", using `sourceText` (which says "Browser").
     - Then the media rows (Stream, Video, Audio, Output) and Final URL and Completed when present, as today.
     The errors and hints blocks stay above the columns, restyled as `--danger-soft` and `--info-soft` notes.
  2. **Connections** (B): the header "Connections" + "8 connections · 1.43 of 3.40 GB" (12 `--faint`).
     Then one **box per connection** from `d.connectionInfo` (sorted by `index`), up to 16 (two rows of 8;
     beyond that a final "+N" box). Each box is a 26 px rounded cell showing that connection's current range
     (received part in `--seg-fill`, live front `--seg-edge`, retrying in `--seg-paused`, error in
     `--seg-failed`), with its speed under it (11.5 tabular, "0.8 MB/s", `--faint` when idle). Under the
     boxes: "Together 6.2 MB/s", the **sum** of the shown connection speeds, so the boxes add up to it,
     with the legend Received, Connection and Remaining. Media downloads title the panel "Parts" or
     "Fragments" (`pieceNames`), show "12 of 340" and have boxes labelled with their fragment number.
     Without live connections (paused, queued, stale), the panel shows the static segment overview
     (`segmentCells(d, 8)` at 26 px) and "Not running".
  3. **Speed** (A + B): the big speed in `--type-figure` ("5.8 MB/s") with "6m 04s left" (13/600) and
     "Done ≈ 11:59" (12 `--muted`, the clock time from `formatClock(now + eta)`, F) under it. Then
     `SpeedChart` (L2): 60 s of `historyOf(id)` with a **y-axis** at the end (labels 0, half and max
     rounded to a 1/2/5 step, 11 px `--faint`, dashed `--chart-grid` lines), a Mint line + fill, and an
     end dot of r3 (`--surface` fill, Mint stroke). The footer reads "Avg 5.2 · Peak 5.9 MB/s · Limit none".
     It is decorative with a text alternative, like `Sparkline`'s `aria-label`.

### 3.16 Command palette (L1)

A `min(37.5rem, 100%)` panel (600 px at 16 px; wider with larger text), top-anchored 7 px from the top, centred over the title bar; `--raised`, radius 12, 1 px
`--line-strong`, `--shadow-lg`, over a light dim. The dialog's name stays "Command palette" and the
combobox's "Search commands".

- **Input row**: 46 px, search icon in `--accent`, 15 px text; at the end, "Commands and downloads"
  (12 `--faint`).
- **Groups** (caps headings):
  - With an empty query: **Running downloads** (downloading and probing, at most 5), then Commands in their
    order, then Go to.
  - With a query: commands and downloads are ranked together by `fuzzyScore` (commands on label, keywords
    and description; downloads on display name and host), in runs as today. **Ties put commands first.**
- **Command row**: 38 px; icon, label 13.5/500, the plain-language **description** (12 `--faint`), and the
  shortcut as `kbd`s at the end. `Command` gains `description?: string`. Every command gets one, for
  example:
  - Pause all: "Stops the 3 active downloads, keeps their progress"
  - Set speed limit…: "Now unlimited · applies to every download"
  - When done…: "Do nothing, sleep, shut down, or quit Downex"
  - Density: compact: "One line per download, about 18 rows"
  - Unavailable commands keep their `disabledReason`.
- **Download row**: tile 20, name with matched letters in `--accent-text` 600, then for running downloads
  `SegmentBar size="mini"` + % + speed + "6m 04s left", and for the rest the status + date + size
  (12 `--faint`). Enter runs `openDownload(d)` (as a row's Enter); **Ctrl+Enter shows it in its folder**.
  Each download row is a `role="option"` with an id; the palette's `aria-activedescendant` works as today.
- **New commands**: "Density: comfortable / compact", "Set global speed limit…" (opens the status-bar menu),
  "When done…" (opens the when-done menu), "Show details pane / Hide details pane" (existing), and
  (0.8.2) "Download again…" for the completed downloads of the selection (§4.7; "nothing completed in the
  selection" otherwise).
- **Footer** (`--surface-2`): `kbd ↑ ↓` move · `Enter` open/run · `Ctrl Enter` show in folder · `Esc` close
  · "2 downloads · 4 commands" at the end.

### 3.17 Toasts and banners (L1)

- **Toasts**: bottom-right, 16 px above the status bar; 360 px; `--raised`, 1 px `--line`, radius 11,
  `--shadow-lg`. A tone icon (Mint check, info, danger), title 13/600, detail (name, bidi-isolated), and
  actions as `.btn.sm` (primary last). Rise and fade in `--dur-4`; out 140 ms. Hold and release on hover
  and focus are unchanged. At most 3.
- **Tools toast** (yt-dlp/Deno setup): the same card at the bottom-left, with a `ProgressBar`.
- **Connection banner**: a strip across the top of the work card, `--danger-soft` with a `--danger-line`
  bottom border: icon, title 600, detail, "Retry now" (`.btn.sm`), and a countdown in `--muted`.
- **Post-action banner**: a 40 px strip at the very top (above the title bar's content, inside the window),
  `--warn-soft`, with the countdown in 600 tabular and Cancel (`.btn.sm`). The keyboard focus behaviour stays.

### 3.18 Empty states (L2 for the list)

Centred in the list area, max width 420 px:

- **No downloads at all**: a 48 px tray glyph in a 72 px `--accent-soft` circle with `--accent-text`
  (no illustration, no emoji).
  - Title "No downloads yet" (`--type-section`).
  - Text "Paste a link anywhere in this window (Ctrl+V), drop one here, or add one." (13 `--muted`).
  - Buttons: [Add download] (primary) and [Add video or stream] (secondary).
  - A third line: "Downloads from Chrome, Edge and Firefox arrive here with the Downex extension. **Get the
    browser extension**", where the link button opens Settings → Integration (which explains the extension and the
    relay). No external URL until store listings exist.
- **Filtered to nothing**: "No downloads match ‹query›" or "Nothing in ‹filter›" + [Clear filters].
- **Stale list**: the banner above (§3.17); the list stays visible, with dimmed live values.

### 3.19 Video card and quality cards (L3)

- **Video card** (compact, 0.8.2 fit rule): `--surface-2`, 1 px `--line`, radius 12, padding 10, 12 px gap.
  - **Poster** 96×54 (the popup's) with radius 8. No page image is ever fetched: a pale `--c-video` tint
    with a 28 px `--c-video` play disc. The duration is shown once, as the first chip (no badge on the
    poster).
  - Then the title `--type-section` (2 lines at most, `dir="auto"`, bidi-isolated: an Arabic or Kurdish
    title is right-aligned), then one line with the site (globe + "YouTube · youtube.com", 12.5 `--muted`)
    and the chips: duration, "Up to 4K", "Saved as MP4", "No cookies sent". The chips sit on the site's
    line where they fit and wrap under it otherwise.
  - **The address in the card** (`showAddress`, Confirm download): where the dialog has no address field,
    the site's name is followed by the address itself, "YouTube · https://www.youtube.com/watch?v=…", as
    a read-only `.mono` field without a frame: one line with an ellipsis, the whole address as its
    tooltip, selectable and copyable, in the tab order and named "Page" (a page's video) or "Address" (a
    stream). The host is then not repeated. Its value and tooltip are bidi-escaped (`escapeBidi`, as
    every address the user reads).
  - The title clamped to two lines has the whole title as its own tooltip (over the card's, the chosen
    quality's picture).
  - The popup uses a 96×54 poster.
- **Quality cards**: a `role="radiogroup"` named "Video quality". The quality section is a size container
  (`quality`): its cards are two to a row under 28rem (the quality column of Confirm and Add), three up to
  40rem, four from 40rem, with 8 px gaps. Each card is a `<label>` with a visually hidden native radio:
  6/10 px padding, at least 2.75rem (44 px) high, radius 10, 1 px `--line-strong`, `--raised`.
  - Line 1: the class name 14/650 display ("1080p") + the standard suffix 12 `--muted` ("(Full HD) 50fps").
  - Line 2: a `.codec` tag + the size at the end (12/600 `--ink-2`, "~" when approximate).
  - On: `--accent` border + `inset 0 0 0 1px var(--accent)`, `--accent-soft` ground, and a 16 px Mint tick
    circle at the top end.
  - Its tooltip is the whole quality name, then its picture ("4K (2160p) 60fps HDR · 3840×2160"; a
    stream's also its bitrate): a narrow column cuts the suffix with an ellipsis.
  - Arrow keys move between them (native radio group).
  - One card per quality class, in the order and names that downexd and `media.ts` give (never invent or
    merge classes). "No video (audio only)" appears only where `audioOnlyOffered` allows, as a last card.
- **Hint row**: "Quality" (label) + "One MP4 per resolution, best first" (hint). When the cards mix H.264
  with AV1 (or HEVC), a line under the grid: "AV1 is smaller; H.264 plays everywhere." (12 `--faint`,
  info icon; always one line: cut with an ellipsis where it must be, the whole note as its tooltip; in
  0.8.2 it was "H.264 plays everywhere; AV1 is smaller but needs a recent player.", which wrapped in the
  quality column of a 900×600 window). It names the codecs actually present ("AV1 and HEVC are smaller;
  …").

### 3.20 Settings shell (L4)

> **Native windows since 2026-11-10 (owner decision; the WebView window is retired and not
> maintained):** Settings has its own look per OS; the pages, words and behaviour are unchanged and
> shared (`ui/views/settings/skin.go`).
> - **Windows (Windows 11 Settings)**: 60 × 46 rem (70rem left too much empty room, owner
>   2026-11-11); the navigation is 14rem with "Settings" above it, 36 px items, the chosen one on
>   `--hover` with a 3 × 16 px Mint pill at its start; the pane shows the page's title (26 px 600
>   display), the close button over its top corner, and its content 32 px from each side (at most
>   760 px, centred, with larger text); section titles are 600 13 px; every row is its own card
>   (`--surface-2`, `--line-soft` edge, 8 px corners, 3 px apart, at least 62 px high, 18 px at the
>   sides); switches are Windows 11's (40 × 20, hollow when off).
> - **macOS (System Settings)**: 52 × 40 rem; the navigation is 12rem, 30 px items, each icon white on
>   its own coloured 22 px tile, the chosen item filled with Mint; no page title in the pane; group
>   titles 600 12.5 px in `--muted`; rows grouped in a `--surface-2` card with a `--line-soft` edge and
>   10 px corners, at least 44 px high; switches are macOS's (38 × 22, an 18 px knob).
> - Both: the dialog keeps one height on every page (a long page scrolls in the pane, it no longer grows
>   the dialog); the narrow dialog's row of tabs and forced colours keep the look below. The progress
>   window's switch keeps app.css's.
>
> The rest of this section describes the WebView window's Settings, which the native windows followed
> until then.

`min(70rem, 100vw − 32px)` wide and at least `min(46rem, 100vh − --modal-top − 2 × --dialog-gap)` high
(1120 × 684 in the default 1200×760 window); switching pages never lowers it, and a longer page grows
it up to the window's room with its top in place (§3.9 "Dialog sizing"). A pane of 50rem or more (16 px
text in the default window) puts a long page in two columns: Integration as balanced CSS columns
(sections never split; read and tabbed down the first column, then the second), About as two columns of
its own (This copy and Video tools | Folders and the notices), Video sites as its settings and licences
beside the tools. Every page fits the default window at 16 px text (0.8.2 review): Downloads puts its
three limits in a row and the default folder beside "Save downloads into their category's folder", with
Add category in the Categories head (561 px for the 570 px pane); Video sites puts the releases hint
under its label and select, the licence links' addresses in their tooltips; About puts the diagnostics
hint in the header and Check for tool updates beside its title. With 20 px text or in a small window the
long pages scroll in the pane.

- **Side navigation**: 12.5rem on `--surface-2` with a `--line` end border, `role="tablist"
  aria-orientation="vertical"` (the tab names stay, so tests and assistive technology keep working). Items
  are 32 px with a 16 px icon and the label. The selected item gets `--sel`, a Mint icon and 600, and keeps
  the "needs attention" "!" mark. When the dialog is narrower than 44rem (larger text in a small window,
  `@container dialog`), the navigation becomes a row of tabs above the pane with the Tabs look (§3.4.1):
  labels only, on a `--line` hairline, one 2 px Mint bar under the chosen label, scrolling sideways if it
  has to.
- **Pane**: a size container (`settings-pane`) for the pages' own narrow layouts (the category table,
  the proxy fields).
- **Content**: scrolls in 24 px padding; section titles `--type-section` with 12 px under and 24 px between
  sections; each setting row is a label/hint/control grid; switches per §3.6.
- **Footer**: Cancel / Save as today. Appearance changes apply at once and are not part of Save; the page
  says "Saved on this computer right away."

---

## 4. Screens

### 4.1 Map: current component → new design → mockup → lane

| Current file (`ui/frontend/src/…`) | New design | Mockup it follows | Lane |
| --- | --- | --- | --- |
| `App.svelte` | Window grid: title bar / sidebar + work card / status bar; wires the new props, menus and palette downloads | `C-studio/png/main-light.png`, `main-dark.png`, `main-light-idle.png`, `main-dark-idle.png` | L1 |
| *(new)* `lib/components/TitleBar.svelte` | Custom title bar (§4.2) | `main-light.png` (top 44 px) | L1 |
| `lib/components/Toolbar.svelte` | The **page header** in the work card (§4.3); the search input moves to `TitleBar` | `main-light.png` page header, fixed per §1.2 | L1 |
| `lib/components/Sidebar.svelte` | Library / Queues / Status, file tiles, Mint active item, footer (§4.4) | `main-light.png`, `main-dark.png` | L1 |
| `lib/components/StatusBar.svelte` | §3.14 | `main-light-idle.png` (limit menu), `B-fluent/main-light.png` (when-done text) | L1 |
| `lib/components/CommandPalette.svelte` | §3.16 | `C-studio/png/main-light-palette.png` + `A-glass/command-palette.png` | L1 |
| `lib/components/Toasts.svelte` | §3.17 | none (tokens) | L1 |
| `lib/components/ConnectionBanner.svelte` | §3.17 | none (tokens) | L1 |
| `lib/components/PostActionBanner.svelte` | §3.17 | none (tokens) | L1 |
| `lib/components/DownloadTable.svelte` | Header, density row height, columns, empty states (§3.11, §3.18) | `main-light.png`, `main-dark-idle.png` | L2 |
| `lib/components/DownloadRow.svelte` | §3.11 to §3.13 | `main-light.png`, `B-fluent/main-light.png` (Retry) | L2 |
| `lib/components/DetailsPanel.svelte` | §3.15 | `C-studio/png/main-light.png` details + `A-glass/main-light.png` numbers + `B-fluent/main-light.png` connections and chart | L2 |
| *(new)* `SpeedChart.svelte`, `ConnectionBoxes.svelte`, `ListEmpty.svelte` | §3.15, §3.18 | as above | L2 |
| `lib/components/AddDialog.svelte` | §4.6 (Files and Video or stream) | `C-studio/png/add-video.png`, `add-video-dark.png` + `B-fluent/add-video.png` | L3 |
| `lib/components/MediaPicker.svelte` | Quality cards + audio `Select` (§3.19) | `add-video.png` | L3 |
| *(new)* `lib/components/VideoCard.svelte` | §3.19 | `add-video.png` | L3 |
| `lib/components/ConfirmDialog.svelte` | §4.7 | `add-video.png` (same language) | L3 |
| `lib/components/OpenDialog.svelte`, `RemoveDialog.svelte`, `RestartDialog.svelte` | Alert dialogs with an icon header (§4.7) | none (tokens) | L3 |
| `ProgressWindow.svelte` | Progress window and complete dialog (§4.8) | header language of `main-light.png` details; numbers from `A-glass/main-light.png` | L3 |
| `lib/components/SettingsDialog.svelte` (+ `SettingsNetwork`, `SettingsIntegration`, `SettingsVideo`, `SettingsUpdates`; new `SettingsDownloads`, `SettingsAppearance`) | §3.20, §4.9 | none: this document | L4 |
| `lib/components/QueueDialog.svelte`, `ScheduleEditor.svelte`, `MoveToQueueDialog.svelte` | §4.10 | none: this document | L4 |
| `lib/components/Icon.svelte`, `StatusMark.svelte`, `ProgressBar.svelte`, `SegmentMap.svelte`, `Sparkline.svelte`, `Dialog.svelte`, `ContextMenu.svelte` | Foundation primitives (§2.6, §3) | all | F |
| `ext/src/entrypoints/popup/*` | §4.11 | `C-studio/png/popup-page.png`, `popup-page-dark.png`, `popup-downloads.png`, `popup-offline.png` + `B-fluent/popup-page.png` (site switches) | L5 |
| `ext/src/entrypoints/options/*`, `confirm/*`, `ext/src/assets/ui.css` | Token alignment (§4.12) | none (tokens) | L5 |

### 4.2 Title bar (`TitleBar.svelte`, L1; host in §6.2)

This is the 44 px row on `--chrome` spanning the window. When the startup flag `customTitleBar` is true
(Windows main window), the whole bar is a drag region: `style="--wails-draggable: drag"`. Every interactive
child is `--wails-draggable: no-drag`. Double-clicking empty bar space toggles maximise
(`api.window.toggleMaximise()`), unless the host already does this natively (verify on Windows, §8).

1. **Brand**: `BrandMark` 24 + "Downex" (15/650 display, -0.015em). Not a button.
2. **Search** (centred, 460 px, `min(460px, 40vw)`): the existing list search moves here unchanged (same
   `aria-label` "Search downloads by name or URL", Ctrl+F focuses it, Esc clears it, it filters live).
   28 px, `--surface` (dark `#141a17`), 1 px `--line`, radius 8, `--shadow-sm`; search icon `--faint`,
   placeholder "Search downloads". At its end sits a **"Ctrl K"** kbd button (`aria-label="Commands"`,
   `aria-keyshortcuts="Control+K"`, tooltip "Commands (Ctrl+K)") that opens the palette. Typing in the field
   never runs commands.
3. **End**: the theme button (sun or moon, cycling system → light → dark with the existing labels) and
   Settings (gear, Ctrl+,), both 32×30 `.icon-btn`. Then, only with `customTitleBar`, the window controls:
   Minimise, Maximise or Restore (the glyph follows `api.window.isMaximised()`, re-read on `resize`) and
   Close (hover `--winctl-close` with a white glyph). They are 46×44, focusable `<button>`s with
   `aria-label`s, with no rounded hover and the hover reaching the window edge.

Without `customTitleBar` (macOS native frame, older hosts, tests), the same bar renders without window
controls and without a drag region.

### 4.3 Page header (`Toolbar.svelte`, L1)

`<header class="toolbar">` (the class and element stay for tests), 58 px, inside the work card:

- **Start**: the title (`--type-page`): "All downloads", or the active filter's name ("Video", "Paused",
  "Main queue"), + the count (13/500 `--faint`). Then the summary chips (§3.7): "3 downloading" (live),
  "7 queued" and "1 failed", each a filter toggle (`aria-pressed`) that only shows when its count is above 0.
- **End**, in this order:
  1. **Selection group**: Resume, Pause and Remove as `.btn.ghost` with icon + label (`aria-disabled` when
     unavailable, names unchanged).
  2. A 1 px divider.
  3. **Bulk pair**: Resume all and Pause all as icon + label from 1366 px; below that icon-only with
     `aria-label` "Resume all" / "Pause all" and tooltips.
  4. **More** (`MenuButton`, ⋯, `aria-label="More actions"`): Density (radio: Comfortable, Compact), Show or
     hide the details pane, Manage queues…, Select all (Ctrl+A).
  5. **Add** `SplitButton` (primary): "Add" + menu (§3.2).

Below 1180 px the selection labels hide (icons with names and tooltips stay). Below 960 px the summary chips
collapse into the title's count. The header never wraps to a second line.

### 4.4 Sidebar (`Sidebar.svelte`, L1)

`nav aria-label="Filters"` on `--chrome`, 224 px, padding 4/10/10. Group heads are caps 10.5 `--faint`.

- **Library**: "All downloads" (tray icon in `--accent`), then the configured categories with `FileIcon` 16
  (their `categoryKind`), then "Other". Counts are 12 tabular `--faint` at the end.
- **Queues**: the head has the manage button (sliders, `aria-label="Manage queues"`). Rows show the queue
  icon (`--accent` when running, `--muted` stopped) + name + a sub-label ("running", or the next schedule
  start "01:00") + the count. The sr-only Running/Stopped text stays.
- **Status**: To confirm, Active, Queued, Paused, Completed and Failed (the labels of
  `STATUS_FILTER_LABELS`, which is frozen; the mockup's "Downloading" is not adopted), each with its
  `StatusMark` 14.
- **Active item**: `--sel` ground, 1 px `--sel-line` ring, 600 `--ink`, icon in `--accent`, count 600
  `--ink-2`. Hover: `--hover`. Items are 29 px with radius 7; `aria-pressed` stays.
- **Footer** (with a `--line` top border): one honest line about browser hand-offs from the settings:
  "Browser downloads: ask first" or "Browser downloads: start at once" (`confirmBrowserDownloads`), with a
  7 px Mint or grey dot. Clicking it opens Settings → Integration. The window cannot know whether the
  extension is installed, so it never claims "Browser capture on".

The first buttons in the nav stay the category buttons, each with a `.label` span, in order: All downloads,
the categories, Other.

### 4.5 Main window states

- **Busy** (`main-light.png`, `main-dark.png`): rows per §3.11 and §3.12, the selected row with the details
  pane (§3.15).
- **Idle / nothing selected** (`main-light-idle.png`, `main-dark-idle.png`): no details pane; the list
  fills the card.
- **Palette open** (`main-light-palette.png`): §3.16.
- **Empty**: §3.18. **Unreachable daemon**: banner + stale list (actions disabled, as today).

### 4.6 Add dialog (`AddDialog.svelte`, `MediaPicker.svelte`, `VideoCard.svelte`, L3)

`Dialog size="md"` (38 rem; 612 px in the mockup), 56rem once a video's or stream's qualities are found.
The visible title is "Add" + `headerAside` = a radio-mode
`SegmentedControl` with "Files" (link icon) and "Video or stream" (play icon). The radio inputs keep the
names "Files" and "Video or stream". The dialog keeps its accessible name, "Add downloads" / "Add video or
stream", through `Dialog`'s `ariaLabel`. A new optional prop `initialMode?: 'files' | 'stream'` serves the
split button.

**Video or stream**:

1. The address field (globe icon, `.mono`, label "Video page or stream address") with a "✓ Found" end state
   once probed.
2. While probing: the existing "Getting video info… 4 s" row (spinner) or the yt-dlp setup progress.
3. `VideoCard` (title, site, duration, chips "Up to 4K", "Saved as MP4" and "No cookies sent"). The chip
   is shown here because the desktop Add dialog never sends cookies.
4. Quality cards (§3.19).
5. A row of two columns: **Audio language** (`Select` of `audioTracks`, labelled "Audio track" for
   assistive technology as today; the trigger shows "English (original) · AAC 129 kbit/s") and **Save to**
   (a `.mono` folder field with a "Change" ghost button and "230 GB free" in the label row from
   `api.freeSpace`; hidden if unavailable).
6. **More options** (B), collapsed: a disclosure (`<button aria-expanded aria-controls>`) "More options" +
   the summary of what is hidden in 12 `--faint`, such as "Main queue · Start now · conference-keynote-
   2026.mp4" (the name last, where the ellipsis cuts; the whole summary is its tooltip). It holds: File
   name (the `.mp4` hint under the field, as the queue's, so the label keeps to one line), Queue
   (`Select`), and "Add paused (start later)". It opens by itself when a hidden field has an error.
7. **Footer**: `footerStart` = "1080p · H.264 + English audio · about 519 MB"; buttons Cancel and the
   primary (labels as today: "Find qualities", "Add video", "Add stream", "Adding…").
8. **Found (0.8.2 fit rule)**: the dialog is 56rem; from 42rem the address and the card span the body, then
   the quality cards (a column of their own, two to a row) beside the audio track, Save to and More
   options (stacked), as Confirm download (§4.7); 12 px between the parts. The order in the page is the
   tab order: address, quality, audio, folder, More options. The address and its "Getting video info…"
   row are one part, so the card stays above the qualities while a video is added (review 2026-10-29:
   it dropped under More options); a failure takes a row after them.

**Files**: the URLs textarea (`.mono`, 4 rows), the folder field, and More options (File name, Connections,
Speed limit with a unit `Select`, Queue, Add paused) collapsed with its summary; open, its fields are two
to a row in tab order (File name, Connections; Speed limit, Queue), one under 32rem. The footer summary is "2
links · 8 connections · Main queue" and the primary is "Add download" / "Add 2 downloads". The playlist
suggestion and the failure list stay as `--info-soft` and `--danger-soft` notes.

### 4.7 Confirm, open, remove and restart dialogs (L3)

- **ConfirmDialog**: the `icon` is the file tile 32 of the download's kind. The title stays "Confirm
  download"; the description stays (it is the dialog's `aria-describedby`) and sits under the title beside
  the icon (§3.9). "N more waiting" is an `--info-soft` chip after the title (the full sentence, "2 more
  downloads are waiting for confirmation.", is its tooltip and its text for assistive technology), so it
  never costs the form a line. The body uses the same field language as Add:
  - **A file** (44rem, one column): Address (`.mono`, read-only, at most three lines with an ellipsis, the
    whole address as its tooltip, no scroller of its own: a signed storage link runs to 600 characters),
    File name with "Size: …" in the hint, Category (`Select`, which proposes the folder as today) beside
    Save to (Browse…), with "Choosing a category proposes its folder." at the end of Save to's label row
    (still the category's `aria-describedby`). With queues, Category and Queue side by side, the queue's
    hint under both, then Save to on a row of its own.
  - **A video or stream with qualities** (56rem; 0.8.2 fit rule, the owner's 1190×752 window): the
    `VideoCard` over both columns, with the page's address in it (`showAddress`: no Page field; §3.19),
    without the "No cookies sent" chip, since a browser hand-off may carry cookies. From 42rem the quality
    cards (`MediaPicker part="video"`, two to a row) are the start column and the fields the end column
    (1 : 1.25): Audio track, File name, Category (and Queue beside it, its hint under both), then Save to
    and Browse… on a row of their own, so the folder shows its path (review 2026-10-29: beside the
    category it had 206 px for the 239 of "C:\Users\you\Downloads\Video"). A refusal ("not enough free
    space…") goes under the qualities, in the room the fields column leaves there, so it costs no row
    (it follows the fields in the page). One column under 42rem. The order in the page is the tab order: the address, the qualities, the audio track, the
    name, the category, the queue, the folder, Browse…. The owner's video with six qualities is 509 px
    high (it scrolled by 114 px in 0.8.2); nothing scrolls at 1200×760, 1190×752, 960×608 (125 %) or
    900×600 with 16 px text.
  - An audio-only stream (no quality cards: the "Audio only" card exists only beside video cards) keeps
    one column at 44rem with its audio part.
  The footer is Cancel download (danger-text), Download later, then Start download (primary). The guard
  delay and the focus rules are unchanged.
- **OpenDialog, RemoveDialog, RestartDialog**: `Dialog size="sm"`, `role="alertdialog"` where they are
  today. The `icon` is a 32 px tile in the tone (`--danger-soft` + alert glyph for program/remove-and-delete,
  `--info-soft` for restart). Texts are unchanged; the names are bidi-isolated as today.
- **Download again (0.8.2, ENGINE-SPEC §7.1).** A completed download can be downloaded again. Where:
  - the context menu: its restart item reads "Download again…" when every selected download is complete,
    else "Restart…" (enabled for anything but downloads awaiting confirmation);
  - the details pane header, for a completed download: "Download again…" (`.btn.sm`, restart icon);
  - the command palette: "Download again…" (the completed ones of the selection). Not on the toolbar.
  `RestartDialog` takes `reason: 'completed'` (every target is complete) and `again: {name, exists, site}[]`
  (the completed ones; `App` asks `api.fileExists` first, a host that cannot tell counts as "there").
  - Title "Download again?"; description "Downex downloads “name” again from its address." ("Downex gets
    “name” again from its video page." for a web page's video: yt-dlp reads it again, so the addresses
    are fresh); for several, "Downex downloads N completed downloads again from their addresses."
  - While the file is there: two choice rows drawn as Move to queue's (§4.10: a label around a visually
    hidden radio, an icon, the choice over what it does, `--sel` + `--sel-line` and a Mint check on the
    chosen one; the chosen row has the focus): **Keep both** (default, `copy` icon: "The new copy is saved as
    “name (1).ext” (or the next free number).") and **Replace the current file** (`refresh` icon: "The
    current file stays until the new copy is complete. If downloading fails, it stays as it is."). The
    choice is remembered per viewer (`localStorage['downex.existingFile']`, inside try/catch) when the
    dialog is confirmed.
  - The file is gone: no choice; "“name” is no longer in its folder. Downex downloads it again from its
    address and saves it under the same name." Several, some gone: one choice for those that are there
    and "1 of them is no longer in its folder: it comes back under its own name."
  - A mixed selection keeps "Restart download(s)?" and its text, adds "“name” is complete: Downex
    downloads it again.", and shows the choice for the completed ones only (radiogroup "The current file
    of “name”").
  - Primary "Download again" (busy "Starting…"); Restart for the other reasons. The store sends
    `resume(id, {restart: true, existingFile})` for completed downloads only. One download started this
    way gets its progress window (DESKTOP-SPEC §7).

### 4.8 Progress window and complete dialog (`ProgressWindow.svelte`, `lib/progress.ts`, L3)

Separate processes, 520×300 (expanded 560) with the **native frame** (§6.2). The sizes stay as they are, in
`progress.ts` and `ui/progress.go`.

**The native title bar follows the app's theme (0.8.2).** The frame stays native (Snap, Alt+Space,
Narrator's title, per-monitor DPI), and its caption takes the theme chosen in Settings → Appearance:
System follows Windows, Light and Dark are forced, from the first frame (the host reads the stored choice,
`ui-theme.json`, before it creates the window; §6.2). The page's theme store (`stores/theme.svelte.ts`)
calls `api.setTheme(choice)` when it attaches (`main.ts`, in every window) and on every change, and
follows a change made in another window: the `storage` event of `downex.theme`, and a re-read when the
window gets the focus (a storage event may not cross WebView2 processes). A host without the binding
rejects; the page keeps its theme.

**Fitted to the content (0.8.2).** One routine (`fitWindow` in `ProgressWindow.svelte`, the arithmetic in
`progress.ts` `fitWindowHeight`/`windowFrame`) sizes the window for the **complete view** and the
**collapsed progress view**, so neither shows a needless scrollbar (a page 1 px short shows a full
scrollbar track in WebView2):

- It measures layout boxes, fractional (`getBoundingClientRect`): the page's in-flow blocks, and for the
  part that scrolls (`.complete`, `.panel`) what its content spans; plus the page's padding and gaps, the
  frame (outer height less the page's, taken once and again after a scaling change; 40 when unknown) and
  `fitSlack` (3 CSS px: Wails truncates window sizes to device pixels both ways), rounded up.
- The complete view: exactly that, from 260 to 720 px. The collapsed view: never below its usual 300 px
  (plain files keep it); a web page's video, with more facts, gets the height they need. On the Speed limit
  tab (no taller than a running download's Status) and on every switch between the tabs it only grows, so
  switching tabs never resizes the window. The expanded
  view keeps 560 px (the connections table scrolls there).
- It fits again when the content changes size (a `ResizeObserver` on the content and the blocks around
  it: a message, a fact, larger text, a narrower window) and when the display's scaling changes
  (`matchMedia('(resolution: …dppx)')`), never when the user resizes the window. The views scroll only in
  a window the user made smaller (`overflow: hidden auto`, never sideways).

**Progress view** (padding 16; ground `--surface`):

1. **Header** (56 px): `FileIcon` 32 · the name 14/650 display (one line with an ellipsis; the full name in
   `title`) over a state line (mark + `progressStatus`, 12.5, state colour) · the **big %** at the end in
   `--type-figure` tabular (`--ink`; `--warn` when paused, `--danger` when failed; "~" when approximate;
   absent when the size is unknown).
2. **Bar**: `SegmentBar` at full width, 8 px, with up to 8 cells (media: one cell).
3. **Tabs** (§3.4.1, `size="sm"`): "Status" / "Speed limit" as a full-width row on its hairline, one Mint
   bar under the chosen label (role `tab`; the names stay; the "limit on" mark is the tab's Mint `dot`,
   not read out: the Status panel's "Speed limit" row says it).
4. **Status panel**: a 2-column grid of facts:
   - Address (`.mono`, head…tail with copy)
   - File size
   - Downloaded
   - Transfer rate (Mint when live) + "· avg 7.6 MB/s" in `--muted`, regular weight, after 3 s of
     running
   - Time left + "Done ≈ 11:59"
   - Resumable
   - Quality: the **human codec text** "1080p · H.264 · AAC 128 kbit/s" (`codecSummary`, F) instead of
     today's long line; the picture and the raw codec strings move to the tooltip.
   Speed limit panel: the existing switch + value + unit `Select` (labelled "Speed limit unit") +
   Connections, with the same timing and validation.
5. **Actions**: Show details (chevron), then at the end Close, Pause/Resume and Cancel (as today).
6. **Expanded details**: the "Start positions and progress by connection" table restyled (tabular, 26 px
   rows, `StatusMark`s), and the `SegmentMap` above it.

**Complete dialog** (same window):

- Header: `FileIcon` 40 with a 16 px Mint check badge at its bottom end; "Download complete" (12.5/600
  `--accent-text`, the `h1`); the **bold name** (`--type-section`, 2 lines); and at the end the size in
  `--type-figure`.
- Facts: Address/Manifest/Page (copy), Saved to (copy), File size, Time taken (+ " · average 7.6 MB/s"
  when downexd counted the running time), Format, and Quality as the human codec text.
- "Don't show this dialog again" (checkbox) + its hint.
- Actions: Open (primary, Enter), Open with… (when available), Open folder, then Close (Esc) at the end.

The failure view gets the same header with the failed state, the error note in `--danger-soft` and the hint,
and keeps Retry, Restart… and Add site login… as today.

### 4.9 Settings (L4)

The shell is §3.20. Pages and their content (every existing field is kept; only its home changes):

| Page (tab name) | Icon | Content |
| --- | --- | --- |
| General | gear | **Start-up**: Launch at login, Show the tray icon. **Notifications**: When a download completes, When a download fails. **Windows**: Show a progress window when a download starts; End with the download complete dialog |
| Downloads | arrow-down | **Downloads**: Simultaneous downloads (1–16), Connections per download (1–32), Global speed limit (value + unit `Select`; `focusField: 'globalSpeedLimit'` focuses it), Default folder (Browse…). **Categories**: "Save downloads into their category's folder" + the category table (name, extensions, folder, browse, remove; Add category) |
| Network | globe | Proxy mode as a radio `SegmentedControl` (System / No proxy / Manual / PAC script) under its label and hint, `fill` up to 30rem, a `Select` where it has less than 24rem (`selectWhenNarrow`) + the mode's fields; host rules (their unit `Select`s); site logins (the editor and its "Address" label stay) |
| Integration | plug | **Browser**: Ask before saving downloads from the browser (`confirmBrowserDownloads`, moved here from General) + the extension's steps, which are the target of "Get the browser extension": Chrome or Edge (Load unpacked) and Firefox (Load Temporary Add-on…), each with this installation's absolute extension folder (`extension-chrome`/`extension-firefox`: `extension\chrome-mv3` or `firefox-mv3` next to the programs, `Contents/Resources/extension/…` in Downex.app) and Copy path / Open folder, then the relay with **Register with browsers again** (runs `downex-nmhost register` next to the programs, no shell; a polite status beside it on success, a `.note.fail` alert under it on failure). **Clipboard**: monitor. **Safety**: mark downloaded files, antivirus scan (command, arguments) |
| Video sites | stream | Unchanged content; the yt-dlp releases control becomes a `Select` (label "yt-dlp releases" stays; disabled as today) |
| Appearance | density | **Theme**: radio `SegmentedControl` System / Light / Dark (the theme store, applied at once). **List density**: Comfortable / Compact (the density store, applied at once), each with a one-line description ("Two lines per download: name, then site and category" / "One line per download, like a classic download manager"). Note: "Saved on this computer right away." |
| Updates | refresh | Check automatically (switch), Check now / Install, status. **Advanced** (a collapsed disclosure; summary "Update address: default" or "custom address set"): the update feed URL field and its validation |
| About (0.8.2, last) | info | §4.9.1 |

`validate.ts`: `SettingsPage` becomes `'general' | 'downloads' | 'network' | 'integration' | 'video' |
'appearance' | 'updates' | 'about'`. `firstErrorPage` sends maxConcurrent, speed, defaultDir, defaultConnections,
categories and categoryRows to `'downloads'`. The existing `initialPage` values keep working. New optional
props: `focusField?: 'globalSpeedLimit'`, `about?` (the About page's sources; it reads the saved settings
itself).

#### 4.9.1 About (`SettingsAbout.svelte`, helpers in `lib/about.ts`, 0.8.2)

Read-only; nothing on it is saved. From the top:

- **Header card** (`--surface`, 1 px `--line`, radius 10, a 3 px `--accent-soft` band at its start):
  `BrandMark` 44, "Downex" (`--type-page`, 650 display; the page's `h3`), "Version x.y.z" (the window's
  build, else downexd's) with, under it in the card, the hint "Diagnostics are versions, states and
  settings for a problem report: no addresses, file names, logins or proxy.", and at the end **Copy
  diagnostics** (`.btn.sm`).
- A `.note.warn` (`role="alert"`) when the window's and downexd's versions differ ("This window is version
  A, but downexd is version B. Quit Downex from its icon in the notification area and start it again…").
- **This copy** (a `dl` card, 6rem labels): Window ("0.8.2 · Windows x64 · WebView2 141.0.…", the
  WebView2 version from the user agent, `webviewVersion`), downexd ("0.8.2 · running since Today, 09:12 ·
  process 4242", "Not reachable"), Built with ("Go 1.27.0 · Commit 0123456789ab" for the native window; the
  WebView window adds its Wails version, "Go 1.27.0 · Wails 2.17.0 · Commit …"; the commit only when the
  build has one, "(with uncommitted changes)" when modified), Updates ("Off in this build
  (no signing key)" when downexd's `updatesSupported` is false, "Off (no update address)", "On · checked
  every day" / "On · checked when you ask") with a "Settings" link to the Updates page (named "Update
  settings"). The IPC endpoint is never shown.
- **Video tools**: "Check for tool updates" (`installTools(true)`; disabled while video pages are off in
  the saved settings) beside the title, its hint under the lead, then one 40 px line each for yt-dlp and
  Deno (name, version `.mono`, Downex copy / Found on PATH, a state pill).
- **Folders**: Data (the list of downloads and the settings), Logs, Tools and Downloads (the default
  folder), each with its path (`.mono`, "Not reported by this downexd" when absent), **Open**
  (`api.openAppFolder(kind)`: the host resolves the path from a fixed list and never takes one from the
  page) and a copy icon button ("Copy the path of the … folder").
- **Open-source notices**: "View open-source notices" opens a view of its own in the pane (no dialog over the
  dialog, one scroll region): "‹ About" back, the title, "Copy all", the file's introduction, then one
  `<details>` per component (its title `.mono`; open, its licence text on `--surface-2`). yt-dlp and Deno
  are listed there as separately downloaded programs; the page links to Video sites for their licences.
  No Downex licence is shown (that is the owner's decision).
- What it did ("Diagnostics copied…", "The path of the Logs folder was copied.") shows in one polite
  `role="status"` pill that keeps to the bottom of the pane for 4 s. It takes no room, and holds no
  control: a click on what it covers goes through (`pointer-events: none`; it lay over "View open-source
  notices" at 1190×752).
- The parts are two columns of their own from a 49rem pane (This copy, Video tools | Folders, the
  notices), one below it; 617 → 565 px at 1190×752 with 16 px text.
- **Copy diagnostics**: `api.diagnostics({webview})` (the host writes it without the endpoint, addresses,
  file names, logins, proxy or update address, with profile paths as %USERPROFILE%/%APPDATA%/
  %LOCALAPPDATA%), then the page hides the endpoint and the update address once more
  (`scrubDiagnostics`) before `copyText`.
- A pane narrower than 34rem (`@container settings-pane`) stacks the header's button and the facts' labels.

### 4.10 Queues (L4)

- **QueueDialog** (72rem): the list column on `--surface-2` (queue rows like the sidebar's, active =
  `--sel`) and the editor. Its heading is the queue name; it has Start/Stop, Name, Simultaneous downloads,
  the speed limit (value + unit `Select`) and "When the queue is done" (`Select` with `postActionChoices`).
  The schedule (`ScheduleEditor`: days/once as a radio `SegmentedControl`, the weekday toggles as
  `aria-pressed` chips, time fields) and the "Order" list (rows with `FileIcon` 16, name and status) follow.
  The `heading` and the "Order" list name stay. 72 × 48 rem at most (§3.9); in a dialog of 64.5rem or
  more (16 px text in the default window) the settings and the Order are the start column and the
  schedule the end column (Tab: settings, schedule, order; the schedule is a `schedule` container whose
  times take two columns with the hint under them). A queue without a schedule (its card is one switch:
  no `.times`) puts the schedule under the settings and gives the Order the end column from the top, so
  a long order uses the height (review 2026-10-29: the main queue's 14 downloads scrolled by 243 px
  beside 404 px of empty column; now 12). An Order name cut by its column is whole in its tooltip; the
  selected queue's note is `--muted` (4.43:1 in `--faint` on the selection tint in dark). The editor is the one scroll
  region (the Order list scrolls with it, not on its own). When the dialog is narrower than 40rem the
  queues become a row above the editor; when the editor is narrower than 30rem (`queue-editor`) the
  settings and the schedule's times take one column.
- **MoveToQueueDialog** (`size="sm"`): a list of selectable 34 px rows (native radios visually hidden, each
  row a label with the queue icon, name and "running / stopped / (now)"); the selected row is `--sel` with
  a Mint check. Each radio is named after its queue. The list has no scroller of its own: the dialog grows
  to the window, then its body scrolls (14 queues fit 1200×760 and 1190×752).

### 4.11 Extension popup (L5)

380 px wide, `--surface` ground, solid (no aurora, no blur), the same tokens as the app.

1. **Header** (12/10/10/14 px): `BrandMark` 22 + "Downex" (15/650) + the **status chip**:
   - "Connected · 3 active" (ok, Mint dot)
   - "Connecting…" (warn)
   - "Not running" (done-grey)
   - "Not set up" (fail, host missing)
   - "Update needed" (incompatible)
   At the end, the Options `.icon-btn` (gear, `aria-label="Options"`, tooltip "Options"). The texts come
   from `popup-view.ts` and `ui-format.ts`; the states are `statusState()`'s.
2. **Tabs** (underline style, `role="tablist"`): "This page" + count, "Downloads" + count. The active tab
   has a 2 px Mint underline and a Mint-soft count.
3. **This page**:
   - **Videos** (caps head + the page host at the end). Each video is a card (`--raised`, `--line`, radius
     11, `--shadow-sm`, padding 10):
     - A 96×54 poster: the kind tint + play glyph + duration when known. No page images are ever loaded.
     - Title 13/600 clamped to **two lines**.
     - A sub-line ("Playing · up to 4K · MP4", 11.5 `--faint`).
     - Actions: the quality picker (a custom select in vanilla TS: trigger "1080p · H.264 · 539 MB" with
       `role="combobox"`, listbox options per class, the same APG keyboard as §3.5; audio as a second
       select only when there is a choice) and a **labelled** primary "Download" button (26 px, icon +
       text). Its `aria-label` stays "Download ‹title›, ‹quality›" as today.
     - The states (sending, sent, refusal, sign-in, retry) render as today's chips under the actions.
   - "Find the video on this page" (secondary button) with the chip **"No cookies sent"**, shown only while
     the sign-in option is off.
   - **Streams and files**: rows with `FileIcon` 20, a two-line-clamped name, the meta "5.0 MB ·
     files.example.com", and an end icon button (28 px, download glyph, `aria-label="Download ‹name›"`,
     tooltip "Download"). Folds (short clips…) are disclosure rows.
   - **On ‹host›** (B's switch card, `--surface-2` card with 3 `Switch` rows; today's behaviour and storage
     are unchanged):
     - "Send downloads to Downex": checked = **not** disabled on this site, the inverse of today's "Disable
       on this site" checkbox, same stored rule. Hint "Hold Alt while clicking to keep one in the browser".
     - "Download buttons on videos" (`site-buttons`). Hint "Shown over the player on this site".
     - "Pause capture in this tab" (`tab-paused`). Hint "Until the tab is closed".
     - The existing hint updates (checking, unavailable on this page) stay.
   - **Footer** (`--surface-2`, `--line` top): the global switch "Capture browser downloads" with today's
     on and off notes.
   - **No dead space**: sections that have nothing are omitted (no empty card), and "Checking the page…"
     is one muted line.
4. **Downloads**: the head "Recent hand-offs". Rows have `FileIcon` 20 by kind, the name (two-line clamp)
   and a pill with the recorded result (`handoffMark` tones: ok "Sent to Downex", info "Waiting in Downex",
   warn "Already in Downex", fail "Not sent"), followed by the time and source (11.5 `--faint`). The popup
   does not watch downloads, so **no live progress, no %, no open/folder buttons**. Footer: the "Capture
   log" disclosure (as today).
5. **Not running / not set up** (`popup-offline.png`): instead of the tabs' content, a centred state: a
   44 px glyph tile, the title "Downex isn't running" / "Downex isn't set up in this browser", and the
   explanation ("Downex starts with the next download you send. Until then, downloads stay in the
   browser." / the host-missing fix with its command in `.mono`). The button is "Check again" (refresh).
   There is no "Start Downex" button: the popup cannot start the app, so it does not offer to.

Dark: `popup-page-dark.png` with the dark tokens.

### 4.12 Extension options and confirm pages (L5)

They keep their structure (options: the section rail + forms; confirm: summary, the link list, actions).
`ui.css` switches from "Glass" to the Studio tokens: solid `--surface` and `--chrome` grounds, no aurora,
no `backdrop-filter`; cards per §2.5; switches, buttons, fields and selects per §3. The options rail header
uses `BrandMark`. The min-size unit becomes the vanilla custom select. The in-page overlay
(`video-overlay.ts` `OVERLAY_SHARED_CSS`, `overlay-host.ts`, `offer-panel.ts`) is **out of scope**: its
pinned CSS test and frozen contracts stay. It can be aligned in a later task.

---

## 5. Contracts tests rely on (keep them)

Every lane keeps these accessible names, roles, ids and classes unless the lane owns every test that uses
them (then it updates those tests deliberately). If a lane needs a change in a file it does not own, it
writes the exact edit in its report's `needs_from_other_lanes`.

- **Rows** (L2): `role="grid"` named "Downloads"; rows `id="dl-row-{id}"` with `data-id`, `aria-selected`
  and `aria-labelledby="dl-name-{id} dl-status-{id}"`; status text `id="dl-status-{id}"` =
  `statusText(d)` (stays "Waiting for confirmation"); hover actions in `[data-row-action]` with
  `button[title="Pause" | "Resume" | "Confirm…" | "Open folder" | "Remove…"]`; the grid's keyboard model.
- **Page header** (L1): `header.toolbar` contains buttons named "Resume", "Pause", "Remove", "Resume all"
  and "Pause all" (icon-only keeps the name) with `aria-disabled` when unavailable.
- **Sidebar** (L1): `navigation` "Filters"; buttons with `aria-pressed`, a `.label` span, names starting
  with the label (`/^Night/`, `/^Main queue/`, `/To confirm/`); categories first; "Manage queues".
- **Palette** (L1): dialog "Command palette", combobox "Search commands", `role="option"` rows, Esc closes
  it, commands win ties.
- **Banners and toasts** (L1): region `/can’t reach downexd/` with "Retry now"; regions `/Going to sleep in/`
  and `/Shutting down in/`; status "Setting up video downloads" with progressbar "Downloading yt-dlp and
  Deno"; notice groups named by their title with actions "Show", "Review…", "Later" and "Open settings".
- **Dialogs** (L3/L4): dialog names "Add downloads", "Add video or stream", "Confirm download", "Settings",
  "Queues" and "Move to queue"; labels "URLs", "Video page or stream address", "File name", "Save to
  folder", "Category", "Queue", "Audio track", "Speed limit unit" and "Address"; buttons "Start download",
  "Download later", "Cancel download", "Find qualities", "Add video", "Add stream", "Add download", "Add N
  downloads", "Move" and "Cancel"; Settings tabs `role="tab"` named General, Network, Integration, Video
  sites and Updates (+ Downloads, Appearance); the radio "Video or stream"; the MoveToQueue radios by queue
  name with `.checked`; the queue heading and the "Order" list.
- **Progress window** (L3): tabs "Status" and "Speed limit" (now `Tabs`, §3.4.1: tablist "Download
  progress", the panel labelled by `tabId(uid, value)`, the "limit on" mark is the tab's `.dot`, hidden from
  the name); combobox "Speed limit unit" (now a `Select`; tests move to `chooseOption`). The window's sizes:
  `PROGRESS_WINDOW` and `fitWindowHeight`/`windowFrame` in `progress.ts` (where nothing can be measured,
  the complete view asks for 420 and the collapsed view keeps its size).
- **Segmented controls**: radio groups only (`role="group"` named by the legend, `getByRole('radio')`,
  `.checked`); never a `tablist`. The fieldset carries `--segments`. With `selectWhenNarrow`, a combobox
  named like the group is also in the DOM (hidden by a container query unless the control is narrow).
- **Quality**: the radiogroup "Video quality" replaces the select of that name; tests read the checked radio
  (`getByRole('radio', {checked: true})` inside it) instead of `.value`.

---

## 6. New and changed contracts

### 6.1 Frontend API (F: `lib/api.ts`, `lib/wails.ts`, `lib/mock.ts`, `lib/testing.ts`, `lib/backend.ts`)

```ts
// StartupView (and Capabilities) gain an OPTIONAL flag, so existing literals (startup.ts's fallback,
// App.svelte's default capabilities, test fixtures) still type-check; absent = false:
readonly customTitleBar?: boolean; // the host is frameless and the page draws the window controls (Windows main window)

// WindowControls gain (the Wails runtime's WindowMinimise / WindowToggleMaximise / WindowIsMaximised;
// missing ones do nothing / resolve false):
minimise(): void;
toggleMaximise(): void;
isMaximised(): Promise<boolean>;

// Api gains:
/** Bytes free on the volume that holds `dir` (absolute). Rejects ApiError 'unimplemented' when the host lacks the binding. */
freeSpace(dir: string): Promise<number>;
```

- `wails.ts`: `FreeSpace?(req: { dir: string }): Promise<unknown>`, which answers `{"freeBytes": "<int64
  decimal>"}`. Errors use the existing Connect-text mapping. A missing `FreeSpace` → `unimplemented`.
  `Startup` parses `customTitleBar` (absent = false).
- `mock.ts`: `freeSpace` → 214 GiB minus the bytes of the simulated completed downloads (deterministic);
  window controls are no-ops; the dev view (`backend.ts`) reports `customTitleBar: true` so that browser
  screenshots show the title bar. `testing.ts` `fakeApi` reports `false` by default and accepts an override.
- `stores/downloads.svelte.ts`: `totalHistory: readonly SpeedSample[]`, the total speed over `HISTORY_MS`,
  sampled with the per-download history.
- `stores/density.svelte.ts`: `density.choice`, `set()` and `toggle()`, persisted (§2.3).
- `lib/segments.ts`: `segmentCells` (§3.13).
- `lib/format.ts`: `formatClock(epochMs): string` ("11:59" in the format locale, 24 or 12 h per locale) and
  `formatDriveLabel(dir): string` (§3.14). Existing outputs are unchanged.
- `lib/media.ts`: `codecSummary(info: MediaInfo, pick?: TrackPick): string` → "1080p · H.264 · AAC 128
  kbit/s". It joins the short class ("1080p", "4K", the part of `qualityName` before " ("), the video
  `codecName`, and the audio `codecName` + `formatBitrate`; a muxed track's second codec stands for the
  audio. Existing outputs are unchanged.

### 6.2 Host (L1, Go: `ui/*.go`)

- `windowSpec` gains `frameless bool`: true for the **main** view on **Windows** only (`runtime.GOOS ==
  "windows"`); progress windows and macOS keep the native frame. `main.go` passes `Frameless:
  spec.frameless`. `windows.Options` keeps `Theme: SystemDefault`, `WebviewUserDataPath`, `DisablePinchZoom`
  and the default `DisableFramelessWindowDecorations: false` (Windows 11 shadow, rounded corners and resize
  borders).
- `BackgroundColour`: main view `#eef2f0` / `#0a0e0c` (new `--chrome`); progress view `#ffffff` / `#171e1b`
  (`--surface`).
- `startupInfo` gains `CustomTitleBar bool json:"customTitleBar"` (always sent) = `frameless` of the view.
  `progress_test.go` and `app_test.go` expectations are updated.
- **New binding** `func (a *App) FreeSpace(req json.RawMessage) (json.RawMessage, error)`:
  - Request `{"dir": "<absolute path>"}` → `{"freeBytes": "<int64 as decimal string>"}` via
    `platform.FreeSpace` (read-only use of `internal/platform`).
  - An empty or relative dir → `invalid_argument: dir must be an absolute path` (never echoing the path).
  - A failed read → `unavailable: could not read the free space`.
  - The path is never logged.
  - `TestBindingsAreExactlyTheContract` gains `"FreeSpace": {[raw], [raw, error]}`.
  - Tests: a temp dir answers > 0, and relative or empty dirs are refused.
- Windows behaviour to keep with the frameless window: resize from every edge and corner, Aero snap by
  dragging to the edges and Win+arrows, double-click on the title bar to maximise or restore, Alt+F4, the
  taskbar icon and its menu, minimise and restore animations. The Windows 11 snap-layout flyout on hovering
  Maximise needs native hit-testing that Wails v2 does not offer; it is **not** in scope (Win+Z still
  works).

### 6.3 Component props added (all optional, so the order of landing does not matter)

- `DetailsPanel`: `onpause?(d)`, `onlimit?(d, bytesPerSecond)`, `onmore?(x, y)`, `onprogress?(d)` (Custom…
  limit → progress window).
- `DownloadTable`: `onaddvideo?()`, `onextension?()`, `onclearfilters?()`.
- `DownloadRow`: `RowAction` adds `'restart'`.
- `AddDialog`: `initialMode?: 'files' | 'stream'`, `freeSpace?(dir): Promise<number>`.
- `SettingsDialog`: `focusField?: 'globalSpeedLimit'`; `initialPage` accepts the new pages; (0.8.2)
  `about?` (`SettingsAbout`'s sources).
- (0.8.2) `RestartDialog`: `reason` adds `'completed'`, `again?: AgainFile[]`, `onconfirm(existingFile)`.
  `App`: `build?: BuildInfo` (from Startup, for About).
- `CommandPalette`: `downloads?: readonly PaletteDownload[]`, `onopendownload?(id)`, `onshowfolder?(id)`;
  `Command.description?`.
- `StatusBar`: `limit`, `onlimit`, `postAction`, `onpostaction`, `freeBytes`, `driveLabel`, `needBytes`,
  `history` (all optional).
- (0.8.2 fit rule) `VideoCard`: `showAddress?` and `addressLabel?` (the address in the card, §3.19).
  `Dialog`: with `icon` and `description`, the description sits under the title (no new prop).

---

## 7. Lane plan

The **foundation** runs first. Then L2, L3, L4 and L5 can run in parallel. **L1 runs last**: it integrates
`App.svelte` and wires the optional props that L2 to L4 add. If L1 runs in parallel, it wires only the props
that exist and lists the rest in `needs_from_other_lanes`. File ownership is strictly disjoint. Files no
lane owns are **frozen**.

Common gates for every lane: `cd ui/frontend; pnpm check` (svelte-check `--fail-on-warnings`, tsc) and
`pnpm test`; for L5 `cd ext; pnpm check; pnpm test`; for L1's Go: `go vet ./ui`, `go test ./ui` (and
`GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go vet ./ui`). Screenshots per §8. No network in tests, no commits,
never touch `bin/`, the user's running Downex processes or real user data.

### 7.1 Foundation (F)

Files:

- `ui/frontend/src/app.css`
- `ui/frontend/src/test-setup.ts`
- `ui/frontend/index.html`
- `ui/frontend/src/lib/components/`:
  - existing: `Icon.svelte`, `StatusMark.svelte`, `ProgressBar.svelte`, `SegmentMap.svelte`,
    `Sparkline.svelte`, `Dialog.svelte`, `ContextMenu.svelte`, `ContextMenu.test.ts`
  - new: `FileIcon.svelte`, `BrandMark.svelte`, `Select.svelte`, `Select.test.ts`,
    `SegmentedControl.svelte`, `SegmentedControl.test.ts`, `Switch.svelte`, `SplitButton.svelte`,
    `MenuButton.svelte`, `MenuButton.test.ts`, `SegmentBar.svelte`, `FileIcon.test.ts`
- `ui/frontend/src/lib/`: `segments.ts`, `segments.test.ts`, `api.ts`, `wails.ts`, `wails.test.ts`,
  `mock.ts`, `mock.test.ts`, `backend.ts`, `testing.ts`, `format.ts`, `format.test.ts`, `media.ts`,
  `media.test.ts`, `motion.svelte.ts`, `actions/focus-trap.ts`, `actions/focus-trap.test.ts`
- `ui/frontend/src/lib/stores/`: `theme.svelte.ts`, `density.svelte.ts`, `density.test.ts`,
  `downloads.svelte.ts`, `downloads.test.ts`

Deliverables:

- Tokens (§2.1–2.5, 2.7) with the legacy alias block.
- Base classes (§3.1, 3.6–3.8), `.tnum` without monospace, body 13 px with tabular figures.
- The icon additions (§2.6.1), status marks (§2.6.2), `FileIcon` (§2.6.3) and `BrandMark` (§2.6.4).
- `Select` and its test helpers (§3.5); `SegmentedControl` (§3.4); `Switch`; `MenuButton`; `SplitButton`;
  the generalised `ContextMenu` (§3.3); `Dialog` props (§3.9); `SegmentBar` + `segmentCells` (§3.13);
  `Sparkline` mini; `SegmentMap` and `ProgressBar` on the new tokens.
- The density store, `totalHistory`, `formatClock`, `formatDriveLabel` and `codecSummary`; the API
  additions with Wails and mock implementations (§6.1).
- Demo data: video names only `.mp4` (city-timelapse-4k.mp4, conference-keynote.mp4,
  drone-footage-coast.mp4, tutorial-episode-#.mp4, nature-documentary-part#.mp4); audio `.mp3`/`.m4a` only
  (podcast-episode-#.mp3, lecture-audio.m4a, field-recording-forest.m4a, ambient-loop-#.mp3); the default
  seed gives a Studio-like list: 3 downloading (16, 8 and 4 connections; one a YouTube site video), 1 to
  confirm, 1 paused, 2+ queued, 1 failed ("HTTP 503"), an HLS completed and completed rest.
- Tests: Select keyboard (open/close, arrows, Home/End, type-ahead, Esc keeps the value, Enter/Tab choose,
  disabled options, label association), density persistence and storage failure, `segmentCells` (≤ 8 cells,
  min width, equal spans, overlap sums, live front, media and unknown size), `formatClock`, `codecSummary`,
  wails `FreeSpace`/`customTitleBar` parsing, and mock `freeSpace`.
- Existing tests stay green: components that are not migrated yet render through the aliases.

### 7.2 L1: Window shell and title bar (including the Go/Wails host)

Files:

- `ui/frontend/src/App.svelte`, `ui/frontend/src/App.shell.test.ts` (new)
- `ui/frontend/src/lib/components/`: `TitleBar.svelte` (new), `TitleBar.test.ts` (new), `Toolbar.svelte`,
  `Sidebar.svelte`, `StatusBar.svelte`, `StatusBar.test.ts` (new), `Toasts.svelte`,
  `ConnectionBanner.svelte`, `ConnectionBanner.test.ts`, `PostActionBanner.svelte`,
  `CommandPalette.svelte`, `CommandPalette.test.ts`
- `ui/frontend/src/lib/stores/toasts.svelte.ts`
- Go: `ui/main.go`, `ui/progress.go`, `ui/progress_test.go`, `ui/app.go`, `ui/app_test.go`,
  `ui/app_space.go` (new), `ui/app_space_test.go` (new)

Deliverables: §4.2–4.5, §3.14, §3.16, §3.17 and §6.2.

- Window grid (title bar 44, sidebar 14rem, work card, status bar 32).
- Wiring: density, details visibility (no pane when nothing is selected), the palette's downloads and
  descriptions, the global limit and when-done menus, free-space polling, the Add split modes, Settings
  focus targets, and the optional props of §6.3.
- Tests: title bar (window buttons only with `customTitleBar`, maximise/restore glyph, drag attributes),
  status bar menus (limit round-trip, when-done update, free-space warning and hiding), palette download
  results (Enter opens, Ctrl+Enter shows the folder, ties favour commands), density from the More menu,
  sidebar footer, and the FreeSpace binding.
- Mockups: `main-light.png`, `main-dark.png`, `main-light-idle.png`, `main-dark-idle.png`,
  `main-light-palette.png`; `A-glass/command-palette.png`; `B-fluent/main-light.png` (status bar).

### 7.3 L2: Download list and details

Files:

- `ui/frontend/src/lib/components/`: `DownloadTable.svelte`, `DownloadTable.test.ts`, `DownloadRow.svelte`,
  `DetailsPanel.svelte`, `DetailsPanel.test.ts`, `SpeedChart.svelte` (new), `ConnectionBoxes.svelte` (new),
  `ListEmpty.svelte` (new), `ConnectionBoxes.test.ts` (new)
- `ui/frontend/src/lib/display.ts` (additive only: existing outputs are unchanged)

Deliverables: §3.11–3.13 (rows: Comfortable/Compact from the density store, row height formula, columns,
state cells, inline Retry/Restart/Review), §3.15 (details pane with A's numbers, B's connection boxes and
y-axis chart, no pane when nothing is selected, a multi-select strip, compact height), and §3.18 (empty
states).

Tests: density row heights and the switch, completed rows with no bar and "Completed" only, inline Retry
only on failed retryable rows, the connection sum, the chart axis labels, the pane absent with no selection,
and the empty states' actions.

Mockups: `main-light.png`, `main-dark.png`, `main-light-idle.png`, `main-dark-idle.png`;
`A-glass/main-light.png`; `B-fluent/main-light.png`, `main-dark.png`.

### 7.4 L3: Add, confirm and progress windows

Files:

- `ui/frontend/src/lib/components/`: `AddDialog.svelte`, `AddDialog.test.ts`, `MediaPicker.svelte`,
  `MediaPicker.test.ts` (new), `VideoCard.svelte` (new), `ConfirmDialog.svelte`, `ConfirmDialog.test.ts`,
  `OpenDialog.svelte`, `RemoveDialog.svelte`, `RestartDialog.svelte`
- `ui/frontend/src/ProgressWindow.svelte`, `ui/frontend/src/ProgressWindow.test.ts`
- `ui/frontend/src/lib/progress.ts`, `ui/frontend/src/lib/progress.test.ts`,
  `ui/frontend/src/lib/stores/progress.svelte.ts`
- **The App integration tests** `ui/frontend/src/App.test.ts` and `ui/frontend/src/App.m6.test.ts`, because
  most of their cases drive these dialogs.

Deliverables: §3.19, §4.6–4.8 (video card, quality cards, More options with summary, "No cookies sent" where
true, native selects → `Select`, mode `SegmentedControl`, the progress window and complete dialog headers,
the human codec text in `qualityText`).

Tests: keyboard on the quality cards, More options summary and auto-open on error, the chip only in Add,
the progress header (big % states), and `qualityText` = "1080p · H.264 · AAC 128 kbit/s". App tests move to
`chooseOption` and radios where the controls changed.

Mockups: `add-video.png`, `add-video-dark.png`; `B-fluent/add-video.png`.

### 7.5 L4: Settings and queues

Files:

- `ui/frontend/src/lib/components/`: `SettingsDialog.svelte`, `SettingsDialog.test.ts`,
  `SettingsDownloads.svelte` (new), `SettingsAppearance.svelte` (new), `SettingsNetwork.svelte`,
  `SettingsIntegration.svelte`, `SettingsVideo.svelte`, `SettingsUpdates.svelte`, `QueueDialog.svelte`,
  `QueueDialog.test.ts`, `ScheduleEditor.svelte`, `MoveToQueueDialog.svelte`
- `ui/frontend/src/lib/`: `validate.ts`, `validate.test.ts`, `queues.ts`, `queues.test.ts`
- `ui/frontend/src/lib/stores/`: `queues.svelte.ts`, `queues.test.ts` (additive only)

Deliverables: §3.20, §4.9 and §4.10 (fixed-size side-navigation Settings with seven pages, Appearance with
theme and density, Updates with Advanced, `SettingsPage` and `firstErrorPage`, `focusField`, selects →
`Select`, the proxy mode `SegmentedControl`, queue editor and move dialog restyle).

Tests: the vertical tablist keyboard, page mapping of errors, Appearance applying at once without Save, the
Advanced disclosure, and the `focusField` focus.

Mockups: none (this document; tokens and components from F).

### 7.6 L5: Extension (popup, options/confirm tokens, unified logo)

Files:

- `ext/src/entrypoints/popup/`: `index.html`, `main.ts`, `style.css`, `popup-view.ts`,
  `popup-view.test.ts`, `select-menu.ts` (new), `select-menu.test.ts` (new)
- `ext/src/entrypoints/options/`: `index.html`, `main.ts`, `style.css`
- `ext/src/entrypoints/confirm/`: `index.html`, `main.ts`, `style.css`
- `ext/src/assets/ui.css`, `ext/src/assets/brand.ts` (new: the `BrandMark` SVG builder, §2.6.4)
- `ext/src/lib/ui-format.ts`, `ext/src/lib/ui-format.test.ts`, `ext/src/lib/media-panel.ts`,
  `ext/src/lib/media-panel.test.ts`

Deliverables: §4.11 and §4.12 (Studio tokens identical to the app's, solid surfaces, tabs, video cards with
labelled Download, two-line titles, the custom quality select, B's per-site switch card with today's
semantics (the "send downloads" switch is the inverse of the stored disable rule), the global capture switch
in the footer, honest hand-off pills without live progress, the not-running state without a start button,
the brand mark in the popup and options, and the options and confirm pages on the new tokens). The in-page
overlay, frozen contracts (`offer-types.ts`, `messages.ts`, `options-form.ts`) and all background/content
code are frozen.

Tests: select-menu keyboard (happy-dom), status chip texts, the inverse site switch keeping the stored rule,
and the "No cookies sent" chip only with sign-in off.

Mockups: `popup-page.png`, `popup-page-dark.png`, `popup-downloads.png`, `popup-offline.png`;
`B-fluent/popup-page.png`.

### 7.7 Frozen files (no lane edits them)

- `ui/frontend/src/main.ts`
- `ui/frontend/src/lib/`: `categories.ts`, `confirm.ts`, `query.ts`, `selection.ts`, `text.ts`,
  `startup.ts`, `quality.ts` (synced with Go by `internal/media/quality_sync_test.go`), each with its test
- `ui/frontend/src/lib/stores/tools.svelte.ts` (+test)
- `ui/frontend/vite.config.ts`, `vitest.config.ts`, `svelte.config.js`, `package.json`
- Everything in `ext/` that §7.6 does not list (including `ext/public/icon/*`)
- `internal/`, `cmd/`, `proto/`, `installer/`, `packaging/`, `scripts/`, `bin/`

---

## 8. Verification

- Each lane renders its screens from the mock (`pnpm build` + a static server, or `pnpm dev`) in headless
  Chrome (`--headless=new --remote-debugging-pipe`, `Page.captureScreenshot`) and **looks** at them:
  - **Main window** at 1280×800, light and dark, Comfortable and Compact; selected and idle; palette open;
    empty (`?mock=0`).
  - **Add video** at 1280×800 (dialog).
  - **Settings**: every page.
  - **Progress window** at 520×300 and 520×560, and the **complete dialog**.
  - **Popup** at 380 px via `Extensions.loadUnpacked` (`--enable-unsafe-extension-debugging`): connected,
    offline, dark.
  Compare with the mockup named in §4.1, and with §1.2's fixes.
- **Contrast**: new colours outside the tables in §2.1 need a measured ratio in the lane report.
- **Windows-only checks** for the owner after `scripts/build-windows.ps1`. The lanes must not launch the
  Wails window while the owner's Downex runs: the single-instance lock would hand the arguments to it.
  - Frameless main window: resize from all edges; snap by drag and Win+arrows; double-click to maximise
    (and no double toggle); minimise, maximise and close buttons with keyboard focus; Alt+F4.
  - The Windows 11 shadow and rounded corners.
  - Progress windows still framed.
  - The free-space item for `D:` and for a network folder.

Final review (2026-10-28), fixed:

- Row bars of paused/failed downloads were a dotted line (§3.13 rule 4); dark tags under 4.5:1 (§3.7);
  tiles cropped to their drawn area (`viewBox 2 2 28 28`, Comfortable rows 26 px, a three-bar audio glyph at
  20 px and below, the same in the extension); the Add video poster is a pale video tint with a play disc;
  a stream's connection boxes show speeds that add up to Together; no empty chart for stopped downloads.
- Escape on an open Select closes only the list (Select listens on its trigger, not through Svelte's
  delegated root listener); the frameless title bar stays usable above a modal (outside the inert page,
  z-index 45, the scrim starts below it, window controls Tab-reachable as a Tab-only TRAP_EXEMPT region);
  the post-queue countdown (z-index 50) and the notices with actions stay above the scrim; Wails' resize
  edges are off while maximised.
- Focus and contrast: §2.8 (fields, listbox options, scroll region), §3.6 (switch), §3.4 (segmented),
  forced colours on selected rows and danger items, the popup's Check again keeps the focus.
- Lows: palette ranks available commands first; "Keep running" everywhere; yt-dlp's "medium" audio note
  reads "Audio"; one percentage on the setup card; a 24 px Commands target; Dialog close tooltip; the dark
  logo tile's hairline ring; the 56 px sidebar rail below 1000 px; 24-hour time and date fields
  (`DateTimeField.svelte`) instead of the browser's pickers; the confirm page lists file rows.

Open items for the owner, which this document does not decide:

- Size units are decimal (KB, MB, GB) since 2026-10-31 (§ Numbers and units above), as the mockups showed.
- The Windows 11 snap-layout flyout on Maximise is not available with Wails v2 (it does not answer
  WM_NCHITTEST). On a restored frameless window the outer 6 px of the page are Wails' resize edges.
- The command palette is light-dismiss and covers the title bar: the first click on a window control
  closes it.
- The schedule's date is a typed `yyyy-mm-dd` field with arrow-key steps; a calendar popover is not built.
- Store links for "Get the browser extension" wait for the store listings (M7 backlog).
