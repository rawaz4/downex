#!/usr/bin/env node
// Lays out the demo UI (the simulated daemon, VITE_DOWNEX_API=mock) in headless Chrome or Edge and
// checks the sizing rules of docs/DESIGN-SYSTEM.md §3.9 ("Dialog sizing") and §4.8:
//   1. Settings General, Appearance and Updates do not scroll in a 1200×760 window;
//   2. Queues never scrolls inside another scrolling area (one scroll region), at every size;
//   3. every dialog fits in a 900×600 window (nothing outside it, no sideways scrolling), also with
//      larger text (root font 20 px);
//   3a. no dialog scrolls by a few pixels there (a scrollbar for less than 24 px of content is a
//      sizing mistake, not a long page);
//   4. the download complete view, and the collapsed progress view (Status, Speed limit, a web page's video), show no
//      scrollbar at 100, 125 and 150 % display scaling, in the window the page asks the host for
//      (the host's rounding of window sizes is emulated: ui/progress.go, Wails' SetSize/Size);
//   5. Add → Video or stream with its qualities found does not scroll at 1200×760 or 1920×1080;
//   6. the progress window's tabs: switching between Status and Speed limit asks the host for no
//      new window size (nothing jumps), and every tab has the same padding on both sides (the
//      hover and focus marks are symmetric);
//   7. the fit rule: every dialog in every content state it has (FIT: Confirm for a file, a page's
//      video with 1 to 8 qualities, one or more audio tracks, long and right-to-left titles, a long
//      address, streams, its notes; Add; Download again; Restart; Remove; Open; Move to queue;
//      Queues; every Settings page; the palette; the progress window's views) shows no scrollbar
//      at 1200×760 and the owner's 1190×752, with 16 and 20 px text, light and dark; nor in the
//      small windows, 1200×760 at 125 % (960×608 CSS px) and 900×600, unless the state is in ALLOW
//      (its content cannot fit there: each entry says why, with the numbers); a state that scrolls
//      (allowed, or content without a bound: a long list) scrolls in one region, in a dialog grown
//      to the window's height, with its footer in view (1920×1080 too: a fixed-height dialog grows
//      into it). Each state reports every scrolling element (scrollHeight − clientHeight) and the
//      block size of its sections at 1190×752 (--json FILE). In every state and window, also:
//      text cut off (an ellipsis or a line clamp) has its whole text in the tooltip the pointer
//      gets there; no field label breaks onto two lines; a Confirm or Add folder field shows its
//      whole folder; a video's card is above its qualities, and no quality column is empty; and,
//      in the states marked `selects`, each Select's list opens 2 to 8 px from its trigger, inside
//      the window (a dialog's blurred backdrop holds fixed boxes: review 2026-10-29).
// jsdom has no layout, so these checks live here rather than in the unit tests.
//
// Usage, from ui/frontend (needs Node 24 and Chrome or Edge; no network):
//   node scripts/measure-dialogs.mjs                       run the checks; exit code 1 on a failure
//   node scripts/measure-dialogs.mjs --shots DIR --prefix after
//                                                          also save screenshots (light and dark)
// Options: --root DIR      another copy of ui/frontend to serve (its own src/, this node_modules)
//          --chrome PATH   the browser (default: CHROME_PATH, then the usual Chrome/Edge places)
//          --schemes light,dark   colour schemes for the screenshots (default both)
//          --sizes 900x600,1200x760,1920x1080   window sizes of the main-window screenshots
//          --only a,b      only these scenarios (names below)
//          --no-check      screenshots only
//          --fit-only      only check 7 (the fit rule); --no-fit: checks 1 to 6 only
//          --fail-shots DIR   a screenshot (light and dark) of every state that breaks the fit rule
//          --json FILE     every measurement of check 7 with its sections' block sizes
//          --breakdown     print the sections of every state that breaks the rule at 1190×752
//          --state-shots DIR   a screenshot of every state measured by check 7 (light and dark),
//                          named <state>-<size>-f<font>-<scheme>.png, as --fail-shots names them
//          --shots-like DIR    with --state-shots: only the names that DIR has (an earlier run's
//                          --fail-shots folder: the same states after a change)
// It starts its own Vite server and browser (a throw-away profile) and stops both.

import { spawn } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));

function args() {
  const out = {
    root: resolve(here, '..'),
    shots: '',
    prefix: 'shot',
    schemes: ['light', 'dark'],
    sizes: null,
    only: [],
    check: true,
    chrome: '',
    legacy: true,
    fit: true,
    failShots: '',
    json: '',
    breakdown: false,
    stateShots: '',
    shotsLike: '',
  };
  const argv = process.argv.slice(2);
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    const next = () => {
      const v = argv[++i];
      if (v === undefined) throw new Error(`${a} needs a value`);
      return v;
    };
    if (a === '--root') out.root = resolve(next());
    else if (a === '--shots') out.shots = resolve(next());
    else if (a === '--prefix') out.prefix = next();
    else if (a === '--schemes') out.schemes = next().split(',').filter(Boolean);
    else if (a === '--sizes') out.sizes = next().split(',').filter(Boolean);
    else if (a === '--only') out.only = next().split(',').filter(Boolean);
    else if (a === '--chrome') out.chrome = next();
    else if (a === '--no-check') out.check = false;
    else if (a === '--fit-only') out.legacy = false;
    else if (a === '--no-fit') out.fit = false;
    else if (a === '--fail-shots') out.failShots = resolve(next());
    else if (a === '--json') out.json = resolve(next());
    else if (a === '--breakdown') out.breakdown = true;
    else if (a === '--state-shots') out.stateShots = resolve(next());
    else if (a === '--shots-like') out.shotsLike = resolve(next());
    else throw new Error(`unknown option ${a}`);
  }
  return out;
}

const opt = args();
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function findChrome() {
  const candidates = [
    opt.chrome,
    process.env.CHROME_PATH,
    'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',
    'C:\\Program Files (x86)\\Google\\Chrome\\Application\\chrome.exe',
    process.env.LOCALAPPDATA && join(process.env.LOCALAPPDATA, 'Google\\Chrome\\Application\\chrome.exe'),
    'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe',
    'C:\\Program Files\\Microsoft\\Edge\\Application\\msedge.exe',
    '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
    '/usr/bin/google-chrome',
    '/usr/bin/chromium',
    '/usr/bin/chromium-browser',
  ];
  const found = candidates.find((p) => p && existsSync(p));
  if (!found) throw new Error('no Chrome or Edge found: pass --chrome PATH or set CHROME_PATH');
  return found;
}

// ---- the demo server --------------------------------------------------------------------------

async function startVite(root) {
  process.env.VITE_DOWNEX_API = 'mock';
  const { createServer } = await import('vite');
  const cacheDir = mkdtempSync(join(tmpdir(), 'downex-measure-vite-'));
  const server = await createServer({
    root,
    configFile: join(root, 'vite.config.ts'),
    cacheDir,
    logLevel: 'error',
    clearScreen: false,
    // loopback only; --root may be a copy whose node_modules links to this one
    server: { port: 0, strictPort: false, host: '127.0.0.1', fs: { allow: [root, resolve(here, '..')] } },
  });
  server.httpServer?.once('close', () => rmSync(cacheDir, { recursive: true, force: true }));
  await server.listen();
  const url = server.resolvedUrls?.local?.[0];
  if (!url) throw new Error('vite did not report its address');
  return { server, url };
}

// ---- the browser (Chrome DevTools Protocol over the built-in WebSocket) ------------------------

async function startChrome() {
  const profile = mkdtempSync(join(tmpdir(), 'downex-measure-'));
  const child = spawn(
    findChrome(),
    [
      '--headless=new',
      '--remote-debugging-port=0',
      `--user-data-dir=${profile}`,
      '--no-first-run',
      '--no-default-browser-check',
      '--disable-extensions',
      '--disable-sync',
      '--disable-background-networking',
      '--disable-component-update',
      '--force-color-profile=srgb',
      'about:blank',
    ],
    { stdio: ['ignore', 'ignore', 'pipe'] },
  );
  const endpoint = await new Promise((res, rej) => {
    let buf = '';
    const timer = setTimeout(() => rej(new Error('the browser did not start')), 20_000);
    child.stderr.on('data', (d) => {
      buf += d.toString();
      const m = /DevTools listening on (ws:\/\/\S+)/.exec(buf);
      if (m) {
        clearTimeout(timer);
        res(m[1]);
      }
    });
    child.on('exit', (code) => rej(new Error(`the browser exited (${code})`)));
  });
  const ws = new WebSocket(endpoint);
  await new Promise((r, j) => {
    ws.addEventListener('open', r, { once: true });
    ws.addEventListener('error', j, { once: true });
  });
  let seq = 0;
  const pending = new Map();
  ws.addEventListener('message', (ev) => {
    const m = JSON.parse(ev.data);
    if (m.id && pending.has(m.id)) {
      const { res, rej } = pending.get(m.id);
      pending.delete(m.id);
      if (m.error) rej(new Error(JSON.stringify(m.error)));
      else res(m.result);
    }
  });
  const send = (method, params = {}, sessionId) => {
    const id = ++seq;
    ws.send(JSON.stringify({ id, method, params, sessionId }));
    return new Promise((res, rej) => pending.set(id, { res, rej }));
  };
  const { targetId } = await send('Target.createTarget', { url: 'about:blank' });
  const { sessionId } = await send('Target.attachToTarget', { targetId, flatten: true });
  const S = (m, p) => send(m, p, sessionId);
  await S('Page.enable');
  await S('Runtime.enable');
  // The simulated window records each size the page asks for (MockWindowState.sizes.push({width,
  // height})); the page cannot resize a browser tab, so this copies those requests where the
  // script can read them and play the host's part.
  await S('Page.addScriptToEvaluateOnNewDocument', {
    source: `(() => {
      const seen = (window.__downexSizes = []);
      const push = Array.prototype.push;
      Array.prototype.push = function (...items) {
        const v = items[0];
        if (this !== seen && items.length === 1 && v && typeof v === 'object' && !Array.isArray(v) &&
            Object.keys(v).length === 2 && typeof v.width === 'number' && typeof v.height === 'number') {
          push.call(seen, { width: v.width, height: v.height });
        }
        return push.apply(this, items);
      };
    })();`,
  });
  const stop = async () => {
    try {
      await send('Browser.close');
    } catch {
      child.kill();
    }
    await new Promise((r) => (child.exitCode !== null ? r() : child.once('exit', r)));
    for (let i = 0; i < 10; i++) {
      try {
        rmSync(profile, { recursive: true, force: true });
        break;
      } catch {
        await sleep(300);
      }
    }
  };
  return { S, stop };
}

// ---- page helpers -------------------------------------------------------------------------------

let S;
let BASE;

async function evaluate(expr) {
  const r = await S('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true });
  if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 600));
  return r.result.value;
}

const state = { w: 0, h: 0, dpr: 1, scheme: 'light', font: 16 };

async function viewport(w, h, dpr = 1) {
  state.w = w;
  state.h = h;
  state.dpr = dpr;
  await S('Emulation.setDeviceMetricsOverride', { width: w, height: h, deviceScaleFactor: dpr, mobile: false });
}

async function scheme(name) {
  state.scheme = name;
  await S('Emulation.setEmulatedMedia', {
    features: [
      { name: 'prefers-color-scheme', value: name },
      { name: 'prefers-reduced-motion', value: 'reduce' },
    ],
  });
}

async function load(query = '', { details = false } = {}) {
  await S('Page.navigate', { url: BASE + query });
  for (let i = 0; i < 120; i++) {
    await sleep(100);
    const ok = await evaluate(`!!document.querySelector('[role=row][data-id], .pw')`).catch(() => false);
    if (ok) break;
  }
  const stored = await evaluate(`(() => { try { return localStorage.getItem('downex.progress.details') === '1'; } catch { return false; } })()`);
  if (stored !== details) {
    await evaluate(`localStorage.setItem('downex.progress.details', '${details ? '1' : '0'}')`);
    return load(query, { details });
  }
  if (state.font !== 16) await evaluate(`document.documentElement.style.fontSize = '${state.font}px'`);
  await sleep(500);
  if (!query) {
    // a notice dialog the simulated daemon may open at start
    for (let i = 0; i < 3; i++) {
      const had = await evaluate(
        `(() => { const b = document.querySelector('[role=dialog] button[aria-label=Close],[role=alertdialog] button[aria-label=Close]'); if (b) { b.click(); return true; } return false; })()`,
      );
      if (!had) break;
      await sleep(400);
    }
  }
}

async function shot(name, clip) {
  if (!opt.shots) return;
  mkdirSync(opt.shots, { recursive: true });
  const params = { format: 'png' };
  if (clip) params.clip = { ...clip, scale: clip.scale ?? 1 };
  const r = await S('Page.captureScreenshot', params);
  writeFileSync(join(opt.shots, `${opt.prefix}-${name}.png`), Buffer.from(r.data, 'base64'));
}

const key = (k, mods = {}) =>
  `document.activeElement?.blur?.(); window.dispatchEvent(new KeyboardEvent('keydown', { key: '${k}', bubbles: true, cancelable: true, ctrlKey: ${!!mods.ctrl}, shiftKey: ${!!mods.shift} }))`;
const rowByName = (n) => `[...document.querySelectorAll('[role=row][data-id]')].find((r) => r.textContent.includes('${n}'))`;
// as a mouse click: the clicked control takes the focus without a focus ring
const click = (sel, text) =>
  `(() => { const el = [...document.querySelectorAll('${sel}')].find((e) => e.textContent.trim().startsWith('${text}')); if (!el) return 'missing ${text}'; el.focus?.({ focusVisible: false }); el.click(); return true; })()`;
const selectRow = (n) =>
  `(() => { const r = ${rowByName(n)}; if (!r) return 'missing row'; const c = r.querySelector('[role=gridcell]') || r; c.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 0 })); c.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, button: 0 })); c.click(); return true; })()`;
const ctxRow = (n) =>
  `(() => { const r = ${rowByName(n)}; if (!r) return 'missing row'; const b = r.getBoundingClientRect(); r.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: b.left + 40, clientY: b.top + 10 })); return true; })()`;
const search = (text) =>
  `(() => { const i = document.querySelector('input[type=search]'); if (!i) return 'missing search'; i.value = '${text}'; i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`;
const dblRow = (n) =>
  `(() => { const r = ${rowByName(n)}; if (!r) return 'missing row'; (r.querySelector('[role=gridcell]') || r).dispatchEvent(new MouseEvent('dblclick', { bubbles: true })); return true; })()`;

/** The main window's dialogs: how each is opened. */
const DIALOGS = {
  add: [key('n', { ctrl: true })],
  'add-stream': [key('n', { ctrl: true }), click('label', 'Video or stream')],
  palette: [key('k', { ctrl: true })],
  'settings-general': [key(',', { ctrl: true })],
  'settings-downloads': [key(',', { ctrl: true }), click('[role=tab]', 'Downloads')],
  'settings-network': [key(',', { ctrl: true }), click('[role=tab]', 'Network')],
  'settings-integration': [key(',', { ctrl: true }), click('[role=tab]', 'Integration')],
  'settings-video': [key(',', { ctrl: true }), click('[role=tab]', 'Video sites')],
  'settings-appearance': [key(',', { ctrl: true }), click('[role=tab]', 'Appearance')],
  'settings-updates': [key(',', { ctrl: true }), click('[role=tab]', 'Updates')],
  'settings-about': [key(',', { ctrl: true }), click('[role=tab]', 'About')],
  remove: [selectRow('podcast-episode-41.mp3'), key('Delete')],
  restart: [ctxRow('photo-archive-277.7z'), click('[role=menuitem]', 'Restart')],
  // the list is virtual: find the row first
  confirm: [search('budget-plan'), dblRow('budget-plan.xlsx')],
  // a completed file: Keep both or Replace
  'download-again': [search('novel-draft'), ctxRow('novel-draft.epub'), click('[role=menuitem]', 'Download again')],
  'move-queue': [ctxRow('setup-x64-312.exe'), click('[role=menuitem]', 'Move to queue')],
  queues: [`(() => { const b = document.querySelector('button[aria-label="More actions"]'); if (!b) return 'missing menu'; b.click(); return true; })()`, click('[role=menuitem]', 'Manage queues')],
};

/** Progress windows (?progress=<id> of the Studio scene). */
const PROGRESS = {
  'pw-status': { id: 1, fit: true },
  'pw-limits': { id: 1, fit: true, steps: [click('[role=tab]', 'Speed limit')] },
  'pw-site': { id: 2, fit: true },
  'pw-expanded': { id: 1, details: true },
  'pw-paused': { id: 5 },
  'pw-failed': { id: 8 },
  'pw-complete': { id: 11, fit: true },
  'pw-complete-media': { id: 9, fit: true },
};

const wanted = (name) => opt.only.length === 0 || opt.only.includes(name);

/** A dialog that scrolls by less than this many pixels is a sizing mistake (check 3a). */
const NEAR_MISS = 24;

const MEASURE = `(() => {
  const visible = (el) => { const b = el.getBoundingClientRect(); return b.width > 0 && b.height > 0; };
  const dialogs = [...document.querySelectorAll('[role=dialog],[role=alertdialog]')].filter(visible);
  const root = dialogs.pop() || document.querySelector('.pw');
  if (!root) return { none: true };
  const label = (el) => el.tagName.toLowerCase() + (typeof el.className === 'string' && el.className.trim() ? '.' + el.className.trim().split(/\\s+/).filter((c) => !c.startsWith('svelte-')).slice(0, 2).join('.') : '');
  const scrolls = (el, axis) => {
    const cs = getComputedStyle(el);
    if (!/(auto|scroll)/.test(axis === 'y' ? cs.overflowY : cs.overflowX)) return 0;
    const prop = axis === 'y' ? 'scrollTop' : 'scrollLeft';
    const before = el[prop];
    el[prop] = 1e6;
    const by = el[prop];
    el[prop] = before;
    return by;
  };
  const all = [root, ...root.querySelectorAll('*')].filter(visible);
  const v = all.map((el) => ({ el, by: scrolls(el, 'y') })).filter((s) => s.by > 0);
  const h = all.map((el) => ({ el, by: scrolls(el, 'x') })).filter((s) => s.by > 0);
  const nested = v.filter((a) => v.some((b) => b.el !== a.el && b.el.contains(a.el)));
  const r = root.getBoundingClientRect();
  return {
    w: Math.round(r.width), h: Math.round(r.height), top: Math.round(r.top), bottom: Math.round(r.bottom),
    fits: r.top >= -0.5 && r.left >= -0.5 && r.bottom <= innerHeight + 0.5 && r.right <= innerWidth + 0.5,
    vscroll: v.map((s) => ({ el: label(s.el), by: s.by })),
    hscroll: h.map((s) => ({ el: label(s.el), by: s.by })),
    nested: nested.map((s) => label(s.el)),
  };
})()`;

// ---- the host's part for a progress window: it sizes the window the page asks for ---------------

/** The frame the page assumes when the host cannot tell it (lib/progress.ts PROGRESS_WINDOW.frame). */
const FRAME = 40;

/**
 * Plays the host: takes the last height the page asked for and gives the page what Windows would
 * at this scaling (Wails' SetSize truncates to device pixels; the frame is whole device pixels).
 */
async function playHost(width) {
  let last = -1;
  for (let round = 0; round < 4; round++) {
    const sizes = await evaluate('window.__downexSizes.slice()');
    const asked = sizes.at(-1);
    if (!asked || asked.height === last) break;
    last = asked.height;
    const devHeight = Math.floor(asked.height * state.dpr);
    const page = Math.floor((devHeight - Math.round(FRAME * state.dpr)) / state.dpr);
    await viewport(width, page, state.dpr);
    await sleep(450);
  }
  return last;
}

// ---- run -------------------------------------------------------------------------------------

const failures = [];
const results = [];

function fail(what) {
  failures.push(what);
  console.log(`  FAIL ${what}`);
}

async function openDialog(name) {
  await load('');
  const log = [];
  for (const step of DIALOGS[name]) {
    log.push(await evaluate(step).catch((e) => `error ${e.message.slice(0, 80)}`));
    await sleep(400);
  }
  return log;
}

async function measureDialog(name, tag) {
  const log = await openDialog(name);
  const m = await evaluate(MEASURE);
  results.push({ name, size: tag, ...m, log });
  return m;
}

async function checks() {
  console.log('checks');
  state.font = 16;
  await scheme('light');
  // 1. Settings General, Appearance and Updates without a scrollbar at the default window size.
  await viewport(1200, 760);
  for (const name of ['settings-general', 'settings-appearance', 'settings-updates']) {
    if (!wanted(name)) continue;
    const m = await measureDialog(name, '1200x760');
    if (m.none) fail(`${name} at 1200x760: no dialog`);
    else if (m.vscroll.length > 0) fail(`${name} at 1200x760 scrolls: ${JSON.stringify(m.vscroll)}`);
    else console.log(`  ok   ${name} at 1200x760 does not scroll (${m.w}x${m.h})`);
  }
  // 2. Queues: one scroll region at every size.
  for (const [w, h] of [
    [900, 600],
    [1200, 760],
    [1920, 1080],
  ]) {
    if (!wanted('queues')) break;
    await viewport(w, h);
    const m = await measureDialog('queues', `${w}x${h}`);
    if (m.none) fail(`queues at ${w}x${h}: no dialog`);
    else if (m.nested.length > 0) fail(`queues at ${w}x${h} scrolls inside a scrolling area: ${JSON.stringify(m.nested)}`);
    else console.log(`  ok   queues at ${w}x${h}: no nested scrolling (scrolling: ${JSON.stringify(m.vscroll)})`);
  }
  // 3. Every dialog fits a 900x600 window, also with larger text.
  for (const font of [16, 20]) {
    state.font = font;
    await viewport(900, 600);
    for (const name of Object.keys(DIALOGS)) {
      if (!wanted(name)) continue;
      const m = await measureDialog(name, `900x600 font ${font}`);
      const missed = results.at(-1).log.find((l) => typeof l === 'string');
      if (name === 'settings-about' && missed) continue; // a copy without the About page
      if (m.none || missed) fail(`${name} at 900x600 (font ${font}): not opened (${missed ?? 'no dialog'})`);
      else if (!m.fits) fail(`${name} at 900x600 (font ${font}) does not fit: top ${m.top}, bottom ${m.bottom}, ${m.w}x${m.h}`);
      else if (m.hscroll.length > 0) fail(`${name} at 900x600 (font ${font}) scrolls sideways: ${JSON.stringify(m.hscroll)}`);
      else if (m.vscroll.some((s) => s.by < NEAR_MISS))
        fail(`${name} at 900x600 (font ${font}) scrolls by a few pixels: ${JSON.stringify(m.vscroll.filter((s) => s.by < NEAR_MISS))}`);
      else console.log(`  ok   ${name} fits 900x600 (font ${font}): ${m.w}x${m.h}`);
    }
  }
  state.font = 16;
  // 4. The complete view and a video page's collapsed progress view at 100, 125 and 150 %.
  for (const [name, p] of Object.entries(PROGRESS)) {
    if (!p.fit || !wanted(name)) continue;
    for (const dpr of [1, 1.25, 1.5]) {
      await viewport(520, 300 - FRAME, dpr);
      await load(`?progress=${p.id}`);
      await playHost(520);
      for (const step of p.steps ?? []) {
        await evaluate(step);
        await sleep(400);
      }
      const asked = await playHost(520);
      const m = await evaluate(MEASURE);
      const sizes = await evaluate('window.__downexSizes.map((s) => s.height)');
      results.push({ name, size: `520@${dpr}`, asked, sizes, page: state.h, ...m });
      if (m.vscroll.length > 0) fail(`${name} at ${dpr * 100}% scrolls in a ${state.h}px page (asked ${asked}): ${JSON.stringify(m.vscroll)}`);
      else console.log(`  ok   ${name} at ${dpr * 100}%: no scrollbar (window ${asked}px, page ${state.h}px)`);
    }
  }
  // 5. Add → Video or stream, its qualities found, without a scrollbar.
  for (const [w, h] of [
    [1200, 760],
    [1920, 1080],
  ]) {
    if (!wanted('add-video-found')) break;
    await viewport(w, h);
    const found = await openVideoFound();
    const m = await evaluate(MEASURE);
    results.push({ name: 'add-video-found', size: `${w}x${h}`, found, ...m });
    if (!found) fail(`add-video-found at ${w}x${h}: no qualities found`);
    else if (m.vscroll.length > 0) fail(`add-video-found at ${w}x${h} scrolls: ${JSON.stringify(m.vscroll)} (${m.w}x${m.h})`);
    else console.log(`  ok   add-video-found at ${w}x${h} does not scroll (${m.w}x${m.h})`);
  }
  // 6. The progress window's tabs.
  for (const [dpr, font] of [
    [1, 16],
    [1.25, 16],
    [1, 20],
  ]) {
    if (!wanted('pw-tabs')) break;
    state.font = font;
    await viewport(520, 300 - FRAME, dpr);
    await load('?progress=1');
    await playHost(520);
    const before = await evaluate('window.__downexSizes.map((s) => s.height)');
    for (const tab of ['Speed limit', 'Status', 'Speed limit']) {
      await evaluate(click('[role=tab]', tab));
      await sleep(500);
      await playHost(520);
    }
    const after = await evaluate('window.__downexSizes.map((s) => s.height)');
    const pads = await evaluate(
      `[...document.querySelectorAll('.pw [role=tab]')].map((t) => { const cs = getComputedStyle(t); return [t.textContent.trim(), cs.paddingInlineStart, cs.paddingInlineEnd]; })`,
    );
    results.push({ name: 'pw-tabs', size: `520@${dpr} font ${font}`, before, after, pads });
    const tag = `pw-tabs at ${dpr * 100}% (font ${font})`;
    // asking again for the same size moves nothing; another size does
    if (after.slice(before.length).some((h) => h !== before.at(-1)))
      fail(`${tag}: switching tabs asks for new window sizes: ${JSON.stringify(before)} → ${JSON.stringify(after)}`);
    else console.log(`  ok   ${tag}: switching tabs keeps the window (${before.at(-1)}px)`);
    const lopsided = pads.filter(([, a, b]) => a !== b);
    if (pads.length < 2) fail(`${tag}: no tabs found`);
    else if (lopsided.length > 0) fail(`${tag}: tabs with uneven padding: ${JSON.stringify(lopsided)}`);
    else console.log(`  ok   ${tag}: every tab is padded alike (${pads[0][1]})`);
  }
  state.font = 16;
  await viewport(520, 300 - FRAME, 1);
}

/** Opens Add → Video or stream, types a video page's address and finds its qualities. */
async function openVideoFound() {
  await openDialog('add-stream');
  await evaluate(
    `(() => { const i = [...document.querySelectorAll('[role=dialog] input')].find((x) => (x.placeholder || '').startsWith('https://www.youtube.com')); if (!i) return 'missing input'; i.focus(); return true; })()`,
  );
  await S('Input.insertText', { text: 'https://www.youtube.com/watch?v=aqz-KE-bpKQ' });
  await sleep(200);
  await evaluate(click('[role=dialog] button', 'Find qualities'));
  for (let i = 0; i < 40; i++) {
    await sleep(500);
    if (await evaluate(`!!document.querySelector('[role=dialog] .found-mark')`)) {
      await sleep(800);
      return true;
    }
  }
  return false;
}

async function screenshots() {
  console.log(`screenshots → ${opt.shots}`);
  const sizes = (opt.sizes ?? ['900x600', '1200x760', '1920x1080']).map((s) => s.split('x').map(Number));
  const MAIN_SHOTS = ['add', 'add-stream', 'settings-general', 'settings-appearance', 'settings-network', 'settings-about', 'queues', 'palette', 'confirm', 'download-again'];
  for (const sch of opt.schemes) {
    await scheme(sch);
    state.font = 16;
    for (const [w, h] of sizes) {
      await viewport(w, h);
      // the title bar and its theme button
      if (wanted('titlebar')) {
        await load('');
        await shot(`${sch}-titlebar-${w}x${h}`, { x: 0, y: 0, width: w, height: 44, scale: 2 });
      }
      for (const name of MAIN_SHOTS) {
        if (!wanted(name)) continue;
        const log = await openDialog(name);
        if (log.some((l) => typeof l === 'string')) {
          console.log(`  skip ${name} at ${w}x${h}: ${JSON.stringify(log)}`);
          continue;
        }
        await shot(`${sch}-${name}-${w}x${h}`);
        if (name === 'settings-appearance' && w === 1200) {
          const box = await evaluate(
            `(() => { const f = [...document.querySelectorAll('fieldset')].find((x) => x.textContent.includes('System')); if (!f) return null; const r = f.closest('.s-row')?.getBoundingClientRect() ?? f.getBoundingClientRect(); return { x: r.left, y: r.top, width: r.width, height: r.height }; })()`,
          );
          if (box) await shot(`${sch}-theme-control-zoom`, { ...box, scale: 2 });
        }
      }
    }
    // larger text
    if (wanted('settings-general')) {
      state.font = 20;
      await viewport(1200, 760);
      await openDialog('settings-general');
      await shot(`${sch}-settings-general-1200x760-font20`);
      await viewport(900, 600);
      await openDialog('settings-network');
      await shot(`${sch}-settings-network-900x600-font20`);
      state.font = 16;
    }
    // progress windows
    for (const [name, p] of Object.entries(PROGRESS)) {
      if (!wanted(name)) continue;
      await viewport(520, (p.details ? 560 : 300) - FRAME, 1);
      await load(`?progress=${p.id}`, { details: !!p.details });
      if (p.fit) await playHost(520);
      for (const step of p.steps ?? []) {
        await evaluate(step);
        await sleep(400);
      }
      if (p.fit) await playHost(520);
      await shot(`${sch}-${name}`);
    }
    await load('?progress=1', { details: false });
  }
}

// ---- 7. the fit rule: every dialog state at the usual window sizes ---------------------------

/**
 * The window sizes of the fit rule: CSS px and display scaling. `strict`: nothing may scroll
 * (only content without a bound); the others are the small windows, where a state in ALLOW may
 * scroll. 960×608 is 8 px taller than 900×600: a dialog has 548 and 540 px there (Dialog's 8 px
 * margins under 640 px), against 684 at 1200×760 and 676 in the owner's window.
 */
const FIT_SIZES = [
  { tag: '1200x760', w: 1200, h: 760, dpr: 1, strict: true },
  // the owner's window (2026-10-29)
  { tag: '1190x752', w: 1190, h: 752, dpr: 1, strict: true },
  // a 1200×760 window at 125 % display scaling
  { tag: '960x608@125%', w: 960, h: 608, dpr: 1.25, strict: false },
  { tag: '900x600', w: 900, h: 600, dpr: 1, strict: false },
  // a large window: a dialog of a fixed height (Settings, Queues) grows into the room it has
  { tag: '1920x1080', w: 1920, h: 1080, dpr: 1, strict: true },
];
/** Where a state's sections are measured (the owner's window). */
const BREAKDOWN_SIZE = '1190x752';

/**
 * States whose content cannot fit somewhere: they may scroll there, in one region, in a dialog
 * grown to the window's height, with the footer in view. Keys: 'name@small' (the small windows,
 * 960×608 at 125 % and 900×600, at 16 and 20 px text), 'name@small@20' (the small windows at
 * 20 px text only) and 'name@20' (20 px text in every window: long settings forms only). Each
 * entry says why, with the content's height (1190×752 --breakdown, the room in brackets: a
 * dialog's body has the window's height less the title bar, 2 × 8 px, the header and the footer).
 * Everything else, the owner's video with six qualities included, fits every window.
 */
const ALLOW = new Map([
  // Confirm download: 20 px text in a small window (the room of a 768×486 window at 16 px)
  ['confirm-video-6-queue@small@20', 'the fields column holds audio, name, Category and Queue with its hint, and the folder (4 rows of 64 px + 21): 460 px of body for 415-423'],
  ['confirm-video-8@small@20', '8 qualities are 4 rows of 64 px in the quality column at 20 px text, under the card: 443 px of body for 415-423'],
  ['confirm-video-8-queue@small@20', '8 qualities beside 4 field rows with the Queue hint: 460 px of body for 415-423'],
  ['confirm-video-6-fail@small@20', 'the refusal note (3 lines at 20 px) under the qualities, beside 4 field rows: 442 px of body for 415-423'],
  ['confirm-hls-queue@small@20', 'as confirm-video-6-queue: 4 field rows with the Queue hint, 460 px of body for 415-423'],
  ['confirm-file-longurl-fail@small@20', 'the refusal note over a 3-line signed address, the name, the selects and the folder: 446 px of body for 415-423'],
  ['confirm-video-8-long@small@20', 'as confirm-video-8, under a card with a 2-line title: 468 px of body for 415-423'],
  ['confirm-video-6-long@small@20', 'the card with a 2-line title at 20 px text over 4 field rows: 437 px of body for 415-423'],
  ['confirm-video-6-invalid@small@20', 'the name and folder errors under their fields at 20 px text (4 field rows): 439 px of body for 415-423'],
  // the worst case combines everything: 8 qualities, a 2-line title, the Queue choice and a refusal
  ['confirm-video-8-all@small', 'the card with a 2-line title, 8 qualities (4 rows) and the refusal note beside 4 field rows with the Queue hint: 477 px of body at 16 px for 423-431, 564 at 20 px'],
  // Add
  ['add-files-more@small', 'More options open (File name, Connections, Speed limit, Queue, Add paused) under the URLs and the folder: 488 px of body at 16 px for 435-443, 536 at 20 px'],
  ['add-video-8@small', 'the address field, the card and 8 qualities (4 rows of 52 px in the quality column, its codec line): 445 px of body at 16 px for 443 (960×608) and 435 (900×600), 491 at 20 px'],
  ['add-video-8-more@small', '8 qualities beside More options open: 516 px of body at 16 px for 435-443, 547 at 20 px'],
  ['add-video-6-more@small', 'More options open (File name and Queue with their hints, Add paused) under the audio track and the folder: 516 px of body at 16 px for 435-443, 547-574 at 20 px'],
  ['add-video-6-adding@small@20', '"Getting video info…" over the card at 20 px text: 450 px of body for 443 (960×608), 477 for 435 (900×600)'],
  // Queues: a fixed-size editor (48 rem); its one pane, the editor, scrolls
  ['queues-night@20', 'the schedule (switch, times, days, retry, wake) beside the settings needs a 64.5 rem dialog: at 20 px text the editor is one column of 785 px for 570-578'],
  ['queues-night-once@20', 'as queues-night (the date instead of the days): 786 px for 570-578'],
  ['queues-night@small', 'one column in a 928 or 868 px dialog: 723 px of editor at 16 px for 434-442'],
  ['queues-night-once@small', 'as queues-night: 724 px for 434-442'],
  ['queues-new@small@20', 'one column at 20 px text: the settings and the schedule are 566-587 px for 434-442'],
  // Settings: a fixed-size dialog (46 rem); its one pane scrolls. 16 px text fits the strict windows.
  ['settings-general@small', 'three sections of switches in one column (the pane is under 50 rem): 535 px for 434-442 at 16 px'],
  ['settings-network@small', 'the proxy fields and the empty rules and logins: 523 px for 434-442 at 16 px'],
  // Downloads: the limits three to a row and the folder beside its switch fit 16 px text in the
  // strict windows (561 px for 570-578, with the 5 default categories)
  ['settings-downloads@small', 'the limits two to a row over the 5 default categories: 647 px for 434-442 at 16 px'],
  ['settings-downloads@20', 'at 20 px text the limits and the category table: 688 px for 570-578'],
  ['settings-integration@20', 'two columns need a 50 rem pane; at 20 px text the browser, clipboard and safety sections are one column of 844 px for 570-578'],
  ['settings-integration@small', 'one column: 768 px for 434-442 at 16 px'],
  // Video sites and About are long reading pages (tools, licences, folders, notices): two columns
  // fit them in the strict windows at 16 px text, not at 20 px or in a small window
  ['settings-video@20', 'one column at 20 px text (the pane is under 50 rem): 716 px for 570-578'],
  ['settings-video@small', 'one column: 649-667 px for 434-442 at 16 px'],
  ['settings-video-ready@20', 'one column at 20 px text with both tools installed: 764 px for 570-578'],
  ['settings-video-ready@small', 'one column: 706-724 px for 434-442 at 16 px'],
  ['settings-about@20', 'one column at 20 px text: the header, This copy, Video tools, Folders and the notices are 1055 px for 570-578'],
  ['settings-about@small', 'one column: 952 px for 434-442 at 16 px'],
]);

/** Whether `s` may scroll in this window at this text size (ALLOW; a state without a bound may always). */
function allowed(s, size, font) {
  if (s.unbounded) return true;
  if (ALLOW.has(`${s.name}@${font}`)) return true;
  if (size.strict) return false;
  return ALLOW.has(`${s.name}@small`) || ALLOW.has(`${s.name}@small@${font}`);
}

const typeIn = (sel, text) =>
  `(() => { const el = document.querySelector(${JSON.stringify(sel)}); if (!el) return 'missing ${sel}'; el.focus(); el.value = ${JSON.stringify(text)}; el.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`;
/** A click on a row with Ctrl held (adds it to the selection). */
const ctrlRow = (n) =>
  `(() => { const r = ${rowByName(n)}; if (!r) return 'missing row ${n}'; (r.querySelector('[role=gridcell]') || r).dispatchEvent(new MouseEvent('click', { bubbles: true, ctrlKey: true })); return true; })()`;
const clickSel = (sel) =>
  `(() => { const el = document.querySelector(${JSON.stringify(sel)}); if (!el) return 'missing ${sel}'; el.focus?.({ focusVisible: false }); el.click(); return true; })()`;
const moreActions = `(() => { const b = document.querySelector('button[aria-label="More actions"]'); if (!b) return 'missing menu'; b.click(); return true; })()`;
const openQueues = [moreActions, click('[role=menuitem]', 'Manage queues')];
const queueRow = (n) => click('[role=dialog] .queue', n);
/** In a state's steps: the text size is set here (a step before it finds list rows at 16 px). */
const FONT = 'font';
/** The Confirm dialog's Start download, once its guard delay is over. */
const startDownload = [async () => (await sleep(700), true), click('[role=dialog] button', 'Start download')];
/** Clears the folder and types a name the daemon refuses, then Start: both fields show their error. */
const invalidConfirm = [
  `(() => { const i = [...document.querySelectorAll('[role=dialog] input.mono')].pop(); if (!i) return 'missing folder'; i.value = ''; i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`,
  typeIn('[role=dialog] input.input:not(.mono)', 'report<draft>?.mp4'),
  ...startDownload,
];

/** Add → Video or stream: types an address, finds its qualities and waits for `until`. */
function findMedia(address, until = '.found-mark') {
  return [
    key('n', { ctrl: true }),
    click('label', 'Video or stream'),
    async () => {
      const r = await evaluate(
        `(() => { const i = [...document.querySelectorAll('[role=dialog] input')].find((x) => (x.placeholder || '').startsWith('https://www.youtube.com')); if (!i) return 'missing input'; i.focus(); return true; })()`,
      );
      if (r !== true) return r;
      await S('Input.insertText', { text: address });
      await sleep(200);
      return evaluate(click('[role=dialog] button', 'Find qualities'));
    },
    async () => {
      for (let i = 0; i < 40; i++) {
        await sleep(400);
        if (await evaluate(`!!document.querySelector('[role=dialog] ${until}')`)) return (await sleep(700), true);
      }
      return `no ${until}`;
    },
  ];
}
const YT = 'https://www.youtube.com/watch?v=Xb7mQ2kLp9w';
const SEVERAL = ['novel-draft.epub', 'podcast-episode-41.mp3', 'drone-footage-coast.mp4', 'viewer-8.x86_64.rpm', 'project-assets-362.zip', 'nature-documentary-part1.mp4'];

/**
 * The states of the fit rule. query: the demo's address (?fixture= sets the state up:
 * MockOptions.fixture in src/lib/mock.ts); confirm: the Confirm dialog opens by itself;
 * steps: what opens the state (a string is evaluated in the page, a function runs here; a
 * string result is a failure); unbounded: content without a bound (a long list), which may
 * scroll at every size, in one region.
 */
const FIT = [
  // Confirm: a file
  { name: 'confirm-file', dialog: 'Confirm', state: 'file budget-plan.xlsx, Queue choice', query: '?fixture=confirm:file', confirm: true },
  { name: 'confirm-file-1q', dialog: 'Confirm', state: 'file, one queue', query: '?fixture=confirm:file,queues:1', confirm: true },
  { name: 'confirm-file-waiting', dialog: 'Confirm', state: 'file, 2 more waiting, Queue choice', query: '?fixture=confirm:file,waiting:3', confirm: true },
  { name: 'confirm-file-invalid', dialog: 'Confirm', state: 'file, name and folder errors, Queue choice', query: '?fixture=confirm:file', confirm: true, steps: invalidConfirm },
  { name: 'confirm-file-fail', dialog: 'Confirm', state: 'file, downexd refused (disk full), Queue choice', query: '?fixture=confirm:file,fail', confirm: true, steps: startDownload },
  // a signed storage link (Drive, S3, a CDN): about 600 characters of address
  { name: 'confirm-file-longurl', dialog: 'Confirm', state: 'file, a signed storage address (600 characters), Queue choice', query: '?fixture=confirm:file,url:long', confirm: true, selects: true },
  {
    name: 'confirm-file-longurl-fail',
    dialog: 'Confirm',
    state: 'file, a signed storage address, refused, Queue choice',
    query: '?fixture=confirm:file,url:long,fail',
    confirm: true,
    steps: startDownload,
  },
  // Confirm: a web page's video
  { name: 'confirm-video-1', dialog: 'Confirm', state: 'YouTube video, 1 quality', query: '?fixture=confirm:site,q:1,queues:1', confirm: true },
  { name: 'confirm-video-3', dialog: 'Confirm', state: 'YouTube video, 3 qualities', query: '?fixture=confirm:site,q:3,queues:1', confirm: true },
  { name: 'confirm-video-6-owner', dialog: 'Confirm', state: "owner's case: 6 qualities 1080p50-144p, audio select, one queue", query: '?fixture=confirm:site,q:6,queues:1', confirm: true, selects: true },
  { name: 'confirm-video-6-queue', dialog: 'Confirm', state: '6 qualities, Queue choice', query: '?fixture=confirm:site,q:6', confirm: true, selects: true },
  { name: 'confirm-video-8', dialog: 'Confirm', state: '8 qualities 4K60 HDR-144p (AV1 note)', query: '?fixture=confirm:site,q:8,queues:1', confirm: true },
  { name: 'confirm-video-8-long', dialog: 'Confirm', state: '8 qualities, a 2-line title', query: '?fixture=confirm:site,q:8,title:long,queues:1', confirm: true },
  { name: 'confirm-video-8-queue', dialog: 'Confirm', state: '8 qualities, Queue choice', query: '?fixture=confirm:site,q:8', confirm: true },
  { name: 'confirm-video-6-audio1', dialog: 'Confirm', state: '6 qualities, one audio track (no choice)', query: '?fixture=confirm:site,q:6,audio:1,queues:1', confirm: true },
  { name: 'confirm-video-6-dub', dialog: 'Confirm', state: '6 qualities, 3 audio languages', query: '?fixture=confirm:site,q:6,audio:3,queues:1', confirm: true, selects: true },
  { name: 'confirm-video-6-long', dialog: 'Confirm', state: '6 qualities, long title (wraps)', query: '?fixture=confirm:site,q:6,title:long,queues:1', confirm: true },
  { name: 'confirm-video-6-ku', dialog: 'Confirm', state: '6 qualities, Kurdish title (RTL)', query: '?fixture=confirm:site,q:6,title:ku,queues:1', confirm: true },
  { name: 'confirm-video-6-ar', dialog: 'Confirm', state: '6 qualities, Arabic title (RTL)', query: '?fixture=confirm:site,q:6,title:ar,queues:1', confirm: true },
  { name: 'confirm-video-6-notitle', dialog: 'Confirm', state: '6 qualities, no title', query: '?fixture=confirm:site,q:6,title:none,queues:1', confirm: true },
  { name: 'confirm-video-6-longurl', dialog: 'Confirm', state: '6 qualities, playlist address with tracking parameters', query: '?fixture=confirm:site,q:6,url:long,queues:1', confirm: true },
  { name: 'confirm-video-6-waiting', dialog: 'Confirm', state: '6 qualities, 1 more waiting', query: '?fixture=confirm:site,q:6,queues:1,waiting:2', confirm: true },
  { name: 'confirm-video-6-fail', dialog: 'Confirm', state: '6 qualities, downexd refused (disk full)', query: '?fixture=confirm:site,q:6,queues:1,fail', confirm: true, steps: startDownload },
  { name: 'confirm-video-6-invalid', dialog: 'Confirm', state: '6 qualities, name and folder errors', query: '?fixture=confirm:site,q:6,queues:1', confirm: true, steps: invalidConfirm },
  {
    name: 'confirm-video-8-all',
    dialog: 'Confirm',
    state: '8 qualities, 3 audio, long title, long address, Queue choice, 1 more waiting, refused',
    query: '?fixture=confirm:site,q:8,audio:3,title:long,url:long,waiting:2,fail',
    confirm: true,
    steps: startDownload,
  },
  // Confirm: streams
  { name: 'confirm-hls', dialog: 'Confirm', state: 'HLS stream, 3 qualities (~ sizes), 3 audio', query: '?fixture=confirm:hls,queues:1', confirm: true },
  { name: 'confirm-hls-queue', dialog: 'Confirm', state: 'HLS stream, Queue choice', query: '?fixture=confirm:hls', confirm: true },
  { name: 'confirm-dash', dialog: 'Confirm', state: 'DASH stream, 3 qualities, 3 audio', query: '?fixture=confirm:dash,queues:1', confirm: true },
  { name: 'confirm-muxed', dialog: 'Confirm', state: 'HLS with muxed sound (TS to MP4)', query: '?fixture=confirm:muxed,queues:1', confirm: true },
  { name: 'confirm-stream-audio', dialog: 'Confirm', state: 'audio-only stream', query: '?fixture=confirm:audio,queues:1', confirm: true },
  // Add
  { name: 'add-files', dialog: 'Add', state: 'Files', steps: [key('n', { ctrl: true })] },
  { name: 'add-files-more', dialog: 'Add', state: 'Files, More options open, Queue choice', steps: [key('n', { ctrl: true }), clickSel('[role=dialog] .more-toggle')], selects: true },
  {
    name: 'add-files-manifest',
    dialog: 'Add',
    state: 'Files, a playlist address (suggestion note)',
    steps: [key('n', { ctrl: true }), typeIn('[role=dialog] textarea', 'https://media.example.org/talks/keynote-2026/master.m3u8')],
  },
  { name: 'add-stream', dialog: 'Add', state: 'Video or stream, empty', steps: [key('n', { ctrl: true }), click('label', 'Video or stream')] },
  { name: 'add-video-installing', dialog: 'Add', state: 'video page, yt-dlp and Deno downloading', query: '?fixture=tools:slow', steps: findMedia(YT, '.waiting') },
  { name: 'add-video-getting', dialog: 'Add', state: 'video page, getting video info', query: '?fixture=tools:ready,resolve:slow', steps: findMedia(YT, '.getting') },
  { name: 'add-video-1', dialog: 'Add', state: 'video found, 1 quality', query: '?fixture=tools:ready', steps: findMedia(`${YT}&qualities=1`) },
  { name: 'add-video-3', dialog: 'Add', state: 'video found, 3 qualities', query: '?fixture=tools:ready', steps: findMedia(`${YT}&qualities=3`) },
  { name: 'add-video-6', dialog: 'Add', state: 'video found, 6 qualities', query: '?fixture=tools:ready', steps: findMedia(`${YT}&qualities=6`) },
  { name: 'add-video-8', dialog: 'Add', state: 'video found, 8 qualities (AV1 note)', query: '?fixture=tools:ready', steps: findMedia(`${YT}&qualities=8`) },
  {
    name: 'add-video-8-more',
    dialog: 'Add',
    state: 'video found, 8 qualities, More options open, Queue choice',
    query: '?fixture=tools:ready',
    steps: [...findMedia(`${YT}&qualities=8`), clickSel('[role=dialog] .more-toggle')],
    selects: true,
  },
  {
    name: 'add-video-6-more',
    dialog: 'Add',
    state: 'video found, 6 qualities, More options open, Queue choice',
    query: '?fixture=tools:ready',
    steps: [...findMedia(`${YT}&qualities=6`), clickSel('[role=dialog] .more-toggle')],
  },
  {
    // Add video, while downexd reads the page again (its 15-minute cache expired): "Getting video info…"
    name: 'add-video-6-adding',
    dialog: 'Add',
    state: 'video found, 6 qualities, being added (getting video info)',
    query: '?fixture=tools:ready,add:slow',
    steps: [
      ...findMedia(`${YT}&qualities=6`),
      click('[role=dialog] button', 'Add video'),
      async () => {
        for (let i = 0; i < 30; i++) {
          await sleep(200);
          if (await evaluate(`!!document.querySelector('[role=dialog] .getting')`)) return true;
        }
        return 'no "Getting video info"';
      },
    ],
  },
  { name: 'add-video-6-long', dialog: 'Add', state: 'video found, 6 qualities, long title', query: '?fixture=tools:ready', steps: findMedia(`${YT}&qualities=6&title=long`) },
  { name: 'add-video-6-ar', dialog: 'Add', state: 'video found, 6 qualities, Arabic title', query: '?fixture=tools:ready', steps: findMedia(`${YT}&qualities=6&title=ar`) },
  { name: 'add-video-6-dub', dialog: 'Add', state: 'video found, 6 qualities, 3 audio languages', query: '?fixture=tools:ready', steps: findMedia(`${YT}&qualities=6&audio=3`) },
  { name: 'add-hls', dialog: 'Add', state: 'HLS stream found', steps: findMedia('https://media.example.org/talks/keynote-2026/master.m3u8') },
  { name: 'add-video-error', dialog: 'Add', state: 'video page refused (private video)', query: '?fixture=tools:ready', steps: findMedia('https://www.youtube.com/watch?v=private0001', '.failures') },
  // Download again, Restart, Remove, Open
  { name: 'again-keep', dialog: 'Download again', state: 'one completed, its file there (keep/replace)', steps: [search('novel-draft'), ctxRow('novel-draft.epub'), click('[role=menuitem]', 'Download again')] },
  {
    name: 'again-missing',
    dialog: 'Download again',
    state: 'one completed, its file gone',
    query: '?fixture=gone:novel-draft.epub',
    steps: [search('novel-draft'), ctxRow('novel-draft.epub'), click('[role=menuitem]', 'Download again')],
  },
  {
    name: 'again-several',
    dialog: 'Download again',
    state: '6 completed (5 names + 1 more), one file gone',
    query: '?fixture=gone:podcast-episode-41.mp3',
    // from the palette: the list's context menu does not open on a row this far down in the demo
    steps: [
      selectRow(SEVERAL[0]),
      ...SEVERAL.slice(1).map(ctrlRow),
      FONT,
      key('k', { ctrl: true }),
      typeIn('[role=dialog] input', 'Download again'),
      click('[role=option]', 'Download again'),
    ],
  },
  {
    name: 'restart-mixed',
    dialog: 'Restart',
    state: 'a failed and a completed download',
    steps: [selectRow('photo-archive-277.7z'), ctrlRow('novel-draft.epub'), FONT, ctxRow('photo-archive-277.7z'), click('[role=menuitem]', 'Restart')],
  },
  { name: 'restart', dialog: 'Restart', state: 'one failed download', steps: [ctxRow('photo-archive-277.7z'), click('[role=menuitem]', 'Restart')] },
  { name: 'remove', dialog: 'Remove', state: 'one download', steps: [selectRow('podcast-episode-41.mp3'), key('Delete')] },
  {
    name: 'remove-several',
    dialog: 'Remove',
    state: '6 downloads (5 names + 1 more)',
    steps: [selectRow(SEVERAL[0]), ...SEVERAL.slice(1).map(ctrlRow), FONT, key('Delete')],
  },
  { name: 'open-program', dialog: 'Open', state: 'a completed program (Run this program?)', steps: [search('editor-5'), dblRow('editor-5.AppImage')] },
  // Queues
  { name: 'move-queue', dialog: 'Move to queue', state: '2 queues', steps: [ctxRow('setup-x64-312.exe'), click('[role=menuitem]', 'Move to queue')] },
  // a list without a bound (14 queues is any number): it fits 1200×760 and 1190×752 (14 rows of 34 px)
  { name: 'move-queue-many', dialog: 'Move to queue', state: '14 queues', query: '?fixture=queues:14', steps: [ctxRow('setup-x64-312.exe'), click('[role=menuitem]', 'Move to queue')], unbounded: true },
  // the main queue's Order lists its 13 waiting downloads in the demo: a list without a bound
  { name: 'queues-main', dialog: 'Queues', state: 'Main queue, 13 queued', steps: openQueues, unbounded: true, selects: true },
  { name: 'queues-night', dialog: 'Queues', state: 'Night: schedule every week, empty', steps: [...openQueues, queueRow('Night')], selects: true },
  { name: 'queues-night-once', dialog: 'Queues', state: 'Night: schedule once', steps: [...openQueues, queueRow('Night'), click('[role=dialog] label', 'Once')] },
  { name: 'queues-new', dialog: 'Queues', state: 'New queue', steps: [...openQueues, click('[role=dialog] button', 'New queue')] },
  { name: 'queues-many-queues', dialog: 'Queues', state: '14 queues in the list (the main queue: 13 queued)', query: '?fixture=queues:14', steps: openQueues, unbounded: true,
    // its 14 queues beside the editor: in a small window both columns scroll, side by side
    panes: true },
  { name: 'queues-night-many', dialog: 'Queues', state: 'Night: schedule and 24 queued', query: '?fixture=night:24', steps: [...openQueues, queueRow('Night')], unbounded: true },
  { name: 'queues-main-many', dialog: 'Queues', state: 'Main queue, 37 queued', query: '?fixture=main:30', steps: openQueues, unbounded: true },
  // Settings
  ...[
    ['general', null],
    ['downloads', 'Downloads'],
    ['network', 'Network'],
    ['integration', 'Integration'],
    ['video', 'Video sites'],
    ['appearance', 'Appearance'],
    ['updates', 'Updates'],
    ['about', 'About'],
  ].map(([page, tab]) => ({
    name: `settings-${page}`,
    dialog: 'Settings',
    state: page,
    steps: [key(',', { ctrl: true }), ...(tab ? [click('[role=tab]', tab)] : [])],
    ...(page === 'downloads' || page === 'video' ? { selects: true } : {}),
  })),
  {
    name: 'settings-network-full',
    dialog: 'Settings',
    state: 'network: manual proxy, 3 host rules, 2 site logins',
    query: '?fixture=settings:full',
    steps: [key(',', { ctrl: true }), click('[role=tab]', 'Network')],
    // the host rules and the site logins: lists without a bound
    unbounded: true,
  },
  {
    name: 'settings-network-editor',
    dialog: 'Settings',
    state: 'network: full, the site login editor open',
    query: '?fixture=settings:full',
    steps: [key(',', { ctrl: true }), click('[role=tab]', 'Network'), click('[role=dialog] button', 'Add site login')],
    unbounded: true,
  },
  {
    name: 'settings-video-ready',
    dialog: 'Settings',
    state: 'video sites, yt-dlp and Deno installed',
    query: '?fixture=tools:ready',
    steps: [key(',', { ctrl: true }), click('[role=tab]', 'Video sites')],
  },
  {
    name: 'settings-about-notices',
    dialog: 'Settings',
    state: 'about: open-source notices',
    steps: [key(',', { ctrl: true }), click('[role=tab]', 'About'), click('[role=dialog] button', 'View open-source notices')],
    unbounded: true,
  },
  // the command palette: every command (a list that scrolls by design)
  { name: 'palette', dialog: 'Palette', state: 'every command', steps: [key('k', { ctrl: true })], unbounded: true },
];

/** The progress window's views (a window of its own: the host sizes it as the page asks). */
const FIT_PROGRESS = [
  { name: 'pw-status', dialog: 'Progress', state: 'collapsed, Status', id: 1 },
  { name: 'pw-limits', dialog: 'Progress', state: 'collapsed, Speed limit', id: 1, steps: [click('[role=tab]', 'Speed limit')] },
  { name: 'pw-site', dialog: 'Progress', state: 'collapsed, a web page video', id: 2 },
  // expanded: the connections (up to 32) are a list that scrolls in its own region
  { name: 'pw-expanded', dialog: 'Progress', state: 'expanded (details), 16 connections', id: 1, details: true, unbounded: true },
  { name: 'pw-site-expanded', dialog: 'Progress', state: 'expanded, a web page video', id: 2, details: true, unbounded: true },
  { name: 'pw-paused', dialog: 'Progress', state: 'paused', id: 5 },
  { name: 'pw-failed', dialog: 'Progress', state: 'failed (HTTP 503)', id: 8 },
  { name: 'pw-complete', dialog: 'Progress', state: 'complete', id: 11 },
  { name: 'pw-complete-media', dialog: 'Progress', state: 'complete, a stream', id: 9 },
];

/** What the fit rule needs to know of the open dialog (or progress window). */
const FIT_MEASURE = `(() => {
  const visible = (el) => { const b = el.getBoundingClientRect(); return b.width > 0 && b.height > 0; };
  const dialogs = [...document.querySelectorAll('[role=dialog],[role=alertdialog]')].filter(visible);
  const root = dialogs.pop() || document.querySelector('.pw');
  if (!root) return { none: true };
  const label = (el) => el.tagName.toLowerCase() + (typeof el.className === 'string' && el.className.trim() ? '.' + el.className.trim().split(/\\s+/).filter((c) => !c.startsWith('svelte-')).slice(0, 2).join('.') : '');
  // a text field scrolls its own text: not a scroll region of the layout
  const field = (el) => el.matches('textarea, input, select');
  const all = [root, ...root.querySelectorAll('*')].filter((el) => visible(el) && !field(el));
  const over = (el, axis) => {
    const cs = getComputedStyle(el);
    if (!/(auto|scroll)/.test(axis === 'y' ? cs.overflowY : cs.overflowX)) return 0;
    return axis === 'y' ? el.scrollHeight - el.clientHeight : el.scrollWidth - el.clientWidth;
  };
  const v = all.map((el) => ({ el, by: over(el, 'y') })).filter((s) => s.by > 0);
  const h = all.map((el) => ({ el, by: over(el, 'x') })).filter((s) => s.by > 0);
  const nested = v.filter((a) => v.some((b) => b.el !== a.el && b.el.contains(a.el)));
  // content cut off by a box that hides its overflow (the dialog itself: its footer pushed out)
  const clipped = [root, ...root.querySelectorAll(':scope > form')]
    .filter((el) => /(hidden|clip)/.test(getComputedStyle(el).overflowY) && el.scrollHeight - el.clientHeight > 1)
    .map((el) => ({ el: label(el), by: el.scrollHeight - el.clientHeight }));
  const r = root.getBoundingClientRect();
  const pw = root.classList.contains('pw');
  const maxH = pw ? innerHeight : parseFloat(getComputedStyle(root).maxHeight) || innerHeight;
  const foot = pw ? [...root.querySelectorAll('.actions')].filter(visible).pop() : root.querySelector('.foot');
  let footer = 'none';
  if (foot) {
    const f = foot.getBoundingClientRect();
    const inside = f.top >= r.top - 0.5 && f.bottom <= r.bottom + 0.5 && f.top >= -0.5 && f.bottom <= innerHeight + 0.5;
    const scroller = v.find((s) => s.el.contains(foot));
    footer = !inside ? 'hidden' : scroller ? 'scrolls with ' + label(scroller.el) : 'visible';
  }
  const norm = (t) => (t || '').replace(/\\s+/g, ' ').trim();
  // text cut off (an ellipsis, or a clamp to some lines) must be in the tooltip the pointer gets
  // there: the title of the element or of its nearest ancestor that has one
  const untipped = [];
  for (const el of all) {
    const cs = getComputedStyle(el);
    const cut =
      (cs.textOverflow === 'ellipsis' && el.scrollWidth > el.clientWidth + 1) ||
      (cs.webkitLineClamp && cs.webkitLineClamp !== 'none' && el.scrollHeight > el.clientHeight + 1);
    if (!cut) continue;
    const text = norm(el.textContent);
    if (!text) continue;
    const tip = norm(el.closest('[title]')?.getAttribute('title'));
    if (!tip.includes(text)) untipped.push({ el: label(el), text: text.slice(0, 60), tip: tip.slice(0, 60) });
  }
  // a field's label on two lines ("File / name", "Save / to")
  const wrapped = [...root.querySelectorAll('.label-row > label, .label-row > .label, .field > label[for]')]
    .filter(visible)
    .filter((el) => {
      const lh = parseFloat(getComputedStyle(el).lineHeight) || parseFloat(getComputedStyle(el).fontSize) * 1.3;
      return el.getBoundingClientRect().height > lh * 1.5;
    })
    .map((el) => norm(el.textContent).slice(0, 30));
  // a folder that its field cannot show whole
  const folders = [...root.querySelectorAll('.folder input')]
    .filter((el) => visible(el) && el.value)
    .filter((el) => el.scrollWidth > el.clientWidth + 1)
    .map((el) => ({ value: el.value, client: el.clientWidth, needs: el.scrollWidth }));
  // a video's card over its qualities, and no quality column without qualities
  const order = [];
  const card = root.querySelector('.card-area');
  const quality = root.querySelector('.quality-area');
  if (quality && (quality.childElementCount === 0 || quality.getBoundingClientRect().height < 1)) order.push('an empty quality column');
  if (card && quality && visible(card) && visible(quality) && card.getBoundingClientRect().bottom > quality.getBoundingClientRect().top + 1)
    order.push(\`the card (\${Math.round(card.getBoundingClientRect().top)}) is not above the qualities (\${Math.round(quality.getBoundingClientRect().top)})\`);
  return {
    w: Math.round(r.width), h: Math.round(r.height), top: Math.round(r.top), bottom: Math.round(r.bottom), maxH: Math.round(maxH),
    fits: r.top >= -0.5 && r.left >= -0.5 && r.bottom <= innerHeight + 0.5 && r.right <= innerWidth + 0.5,
    vscroll: v.map((s) => ({ el: label(s.el), by: s.by, client: s.el.clientHeight, content: s.el.scrollHeight })),
    hscroll: h.map((s) => ({ el: label(s.el), by: s.by })),
    nested: nested.map((s) => label(s.el)),
    clipped,
    footer,
    untipped,
    wrapped,
    folders,
    order,
    title: (root.querySelector('h2, h1')?.textContent || '').trim().slice(0, 60),
  };
})()`;

/**
 * Opens each enabled Select of the dialog and reads where its list is: 2 to 8 px from its trigger
 * (below, or above when there is no room), and inside the window.
 */
const OPEN_SELECTS = async () => {
  const count = await evaluate(
    `[...document.querySelectorAll('[role=dialog] [role=combobox]')].filter((b) => !b.disabled && b.getBoundingClientRect().height > 0).length`,
  );
  const out = [];
  for (let i = 0; i < count; i++) {
    await evaluate(
      `(() => { const b = [...document.querySelectorAll('[role=dialog] [role=combobox]')].filter((x) => !x.disabled && x.getBoundingClientRect().height > 0)[${i}]; b.scrollIntoView({ block: 'nearest' }); })()`,
    );
    // the scroll's event comes a frame later: a list open by then would close on it (as it should)
    await sleep(200);
    await evaluate(
      `(() => { [...document.querySelectorAll('[role=dialog] [role=combobox]')].filter((x) => !x.disabled && x.getBoundingClientRect().height > 0)[${i}].click(); })()`,
    );
    await sleep(350);
    const m = await evaluate(`(() => {
      const b = [...document.querySelectorAll('[role=dialog] [role=combobox]')].filter((x) => !x.disabled && x.getBoundingClientRect().height > 0)[${i}];
      const list = document.getElementById(b.getAttribute('aria-controls'));
      const t = b.getBoundingClientRect();
      const l = list.getBoundingClientRect();
      const name = (b.getAttribute('aria-label') || b.labels?.[0]?.textContent || b.id).trim();
      const up = l.bottom <= t.top + 1;
      const gap = up ? t.top - l.bottom : l.top - t.bottom;
      b.click();
      return { name, open: !list.hidden, up, gap: Math.round(gap), top: Math.round(l.top), bottom: Math.round(l.bottom), inside: l.top >= -0.5 && l.bottom <= innerHeight + 0.5 };
    })()`);
    await sleep(150);
    if (!m.open || m.gap < 2 || m.gap > 8 || !m.inside) out.push(m);
  }
  await evaluate(`document.activeElement?.blur?.()`);
  return out;
};

/**
 * Where a state's height goes: the dialog's boxes down to its sections, each with its block size
 * (and its padding, gap and margins where it has them), so the Fix lane sees what to close up.
 */
const BREAKDOWN = `(() => {
  const visible = (el) => { const b = el.getBoundingClientRect(); return b.width > 0 && b.height > 0; };
  const dialogs = [...document.querySelectorAll('[role=dialog],[role=alertdialog]')].filter(visible);
  const root = dialogs.pop() || document.querySelector('.pw');
  if (!root) return null;
  const label = (el) => el.tagName.toLowerCase() + (typeof el.className === 'string' && el.className.trim() ? '.' + el.className.trim().split(/\\s+/).filter((c) => !c.startsWith('svelte-')).slice(0, 2).join('.') : '');
  const px = (v) => Math.round(parseFloat(v) || 0);
  const OPEN = 'form, .body, .form, .picker, .quality, .pair, .where, .fields, .pane, .page, .settings-shell, .editor, .queues-shell, .pw, .panel, .more, .more-body, .about, .cols, .col, .card, .s-section, .s-card, .choices, .names, .list';
  const node = (el, depth) => {
    const b = el.getBoundingClientRect();
    const cs = getComputedStyle(el);
    const out = { el: label(el), h: Math.round(b.height * 10) / 10 };
    const text = (el.textContent || '').replace(/\\s+/g, ' ').trim();
    if (text) out.text = text.slice(0, 40);
    const pad = [px(cs.paddingTop), px(cs.paddingBottom)];
    if (pad[0] || pad[1]) out.pad = pad;
    if (/(grid|flex)/.test(cs.display) && px(cs.rowGap)) out.gap = px(cs.rowGap);
    const mt = px(cs.marginTop), mb = px(cs.marginBottom);
    if (mt || mb) out.margin = [mt, mb];
    if (/(auto|scroll)/.test(cs.overflowY) && el.scrollHeight > el.clientHeight) out.scroll = { client: el.clientHeight, content: el.scrollHeight };
    const kids = [...el.children].filter(visible);
    if (el.matches('.cards')) out.cards = { count: kids.length, columns: cs.gridTemplateColumns.split(' ').length, card: kids[0] ? Math.round(kids[0].getBoundingClientRect().height) : 0 };
    else if (depth < 9 && kids.length > 0 && (el === root || el.matches(OPEN) || (kids.length === 1 && b.height > 60))) out.kids = kids.slice(0, 16).map((k) => node(k, depth + 1));
    return out;
  };
  return { viewport: innerWidth + 'x' + innerHeight, tree: node(root, 0) };
})()`;

/** The rule's verdict on one measurement: what is wrong ([] = nothing). */
function judge(s, size, font, m) {
  if (m.none) return ['no dialog'];
  const out = [];
  const mayScroll = allowed(s, size, font);
  if (!m.fits) out.push(`outside the window (top ${m.top}, bottom ${m.bottom})`);
  if (m.hscroll.length > 0) out.push(`scrolls sideways ${JSON.stringify(m.hscroll)}`);
  if (m.clipped.length > 0) out.push(`content cut off ${JSON.stringify(m.clipped)}`);
  if (m.footer !== 'visible' && m.footer !== 'none') out.push(`footer ${m.footer}`);
  // two panes side by side (a list beside its editor) may each scroll, never one inside the other
  if (m.vscroll.length > (s.panes ? 2 : 1) || m.nested?.length > 0) out.push(`${m.vscroll.length} scroll regions${m.nested?.length ? ` (nested: ${m.nested.join(', ')})` : ''}`);
  if (m.vscroll.length > 0) {
    if (!mayScroll) out.push(`scrolls by ${Math.max(...m.vscroll.map((v) => v.by))} px`);
    if (m.h < m.maxH - 1) out.push(`scrolls in a dialog ${m.h} px high, not grown to the ${m.maxH} px the window has`);
  }
  for (const u of m.untipped ?? []) out.push(`cut text without its tooltip: ${u.el} "${u.text}" (tooltip "${u.tip}")`);
  for (const w of m.wrapped ?? []) out.push(`label on two lines: "${w}"`);
  for (const f of m.folders ?? []) out.push(`folder cut off: "${f.value}" (${f.client} of ${f.needs} px)`);
  for (const o of m.order ?? []) out.push(o);
  for (const s of m.selects ?? [])
    out.push(`list of "${s.name}" ${!s.open ? 'did not open' : !s.inside ? `leaves the window (${s.top}-${s.bottom})` : `is ${s.gap} px from its trigger`}`);
  return out;
}

const fitRows = [];

function fitLine(s, tag, font, schemes) {
  const m = Object.values(schemes)[0]?.m;
  const bad = Object.entries(schemes).filter(([, r]) => r.problems.length > 0);
  const where = `fit ${s.name} at ${tag} (font ${font})`;
  const scroll = m && !m.none && m.vscroll.length > 0 ? ` | scroll ${m.vscroll.map((v) => `${v.el} +${v.by}px (${v.client}/${v.content})`).join(', ')}` : '';
  if (bad.length === 0) {
    console.log(`  ok   ${where}: ${m.w}x${m.h}${scroll}${scroll ? ' (allowed)' : ''}`);
    return;
  }
  const problems = [...new Set(bad.flatMap(([, r]) => r.problems))];
  fail(`${where} [${bad.map(([sch]) => sch).join('+')}]: ${problems.join('; ')} | dialog ${m?.w}x${m?.h}, max ${m?.maxH}, footer ${m?.footer}${scroll}`);
}

/** The names --shots-like asks for (null: every state). */
let likeNames = null;

async function stateShot(name) {
  if (!opt.stateShots || (likeNames && !likeNames.has(`${name}.png`))) return;
  mkdirSync(opt.stateShots, { recursive: true });
  const r = await S('Page.captureScreenshot', { format: 'png' });
  writeFileSync(join(opt.stateShots, `${name}.png`), Buffer.from(r.data, 'base64'));
}

async function failShot(name) {
  if (!opt.failShots) return;
  mkdirSync(opt.failShots, { recursive: true });
  const r = await S('Page.captureScreenshot', { format: 'png' });
  writeFileSync(join(opt.failShots, `${name}.png`), Buffer.from(r.data, 'base64'));
}

/** Loads the demo for a state and opens it at `font` px text; true, or what went wrong. */
async function openFit(s, font) {
  state.font = 16;
  await S('Page.navigate', { url: BASE + (s.query ?? '') });
  let rows = false;
  for (let i = 0; i < 120 && !rows; i++) {
    await sleep(100);
    rows = await evaluate(`!!document.querySelector('[role=row][data-id]')`).catch(() => false);
  }
  if (!rows) return 'no list';
  const dialogOpen = `!!document.querySelector('[role=dialog],[role=alertdialog]')`;
  // the scene's download awaiting confirmation opens its dialog by itself
  for (let i = 0; i < 40; i++) {
    await sleep(100);
    if (await evaluate(dialogOpen)) break;
  }
  if (s.confirm) {
    const title = await evaluate(`document.querySelector('[role=dialog] h2')?.textContent ?? ''`);
    if (title !== 'Confirm download') return `no Confirm dialog (${title || 'none'})`;
  } else {
    for (let i = 0; i < 3; i++) {
      const had = await evaluate(
        `(() => { const b = document.querySelector('[role=dialog] button[aria-label=Close],[role=alertdialog] button[aria-label=Close]'); if (b) { b.click(); return true; } return false; })()`,
      );
      if (!had) break;
      await sleep(400);
    }
    if (await evaluate(dialogOpen)) return 'a dialog stays open at start';
  }
  const steps = s.steps ?? [];
  const setFont = async () => {
    state.font = font;
    if (font !== 16) await evaluate(`document.documentElement.style.fontSize = '${font}px'`);
    await sleep(200);
  };
  if (!steps.includes(FONT)) await setFont();
  for (const step of steps) {
    if (step === FONT) {
      await setFont();
      continue;
    }
    const r = typeof step === 'function' ? await step() : await evaluate(step).catch((e) => `error ${e.message.slice(0, 80)}`);
    if (typeof r === 'string') return r;
    await sleep(400);
  }
  await sleep(300);
  return (await evaluate(dialogOpen)) ? true : 'no dialog';
}

/** Measures the open state in each colour scheme; a screenshot of each that breaks the rule. */
async function measureFit(s, size, font, shotBase) {
  const schemes = {};
  for (const sch of opt.schemes) {
    await scheme(sch);
    // colours ease into a new scheme: a screenshot waits until they are there
    await sleep(opt.failShots || opt.stateShots ? 450 : 150);
    const m = await evaluate(FIT_MEASURE);
    // where each Select's list opens (the scheme does not move it: light only)
    if (s.selects && sch === opt.schemes[0] && !m.none) m.selects = await OPEN_SELECTS();
    const problems = judge(s, size, font, m);
    schemes[sch] = { m, problems };
    if (problems.length > 0) await failShot(`${shotBase}-${sch}`);
    await stateShot(`${shotBase}-${sch}`);
  }
  await scheme('light');
  return schemes;
}

function printBreakdown(b, indent = '         ') {
  if (!b) return;
  const walk = (n, pad) => {
    const extra = [
      n.pad ? `pad ${n.pad.join('/')}` : '',
      n.gap ? `gap ${n.gap}` : '',
      n.margin ? `margin ${n.margin.join('/')}` : '',
      n.cards ? `${n.cards.count} cards in ${n.cards.columns} columns, ${n.cards.card} px each` : '',
      n.scroll ? `scrolls ${n.scroll.client}/${n.scroll.content}` : '',
    ]
      .filter(Boolean)
      .join(', ');
    console.log(`${indent}${pad}${n.el} ${n.h}${extra ? ` [${extra}]` : ''}${n.text && !n.kids ? ` "${n.text}"` : ''}`);
    for (const k of n.kids ?? []) walk(k, `${pad}  `);
  };
  console.log(`${indent}sections at ${b.viewport}:`);
  walk(b.tree, '');
}

async function fitChecks() {
  console.log('fit rule: no scrollbar at 1200x760, 1190x752, 960x608@125% (900x600 unless allowed); 16 and 20 px text; light and dark');
  for (const s of FIT) {
    if (!wanted(s.name)) continue;
    for (const font of [16, 20]) {
      await scheme('light');
      await viewport(1200, 760, 1);
      const opened = await openFit(s, font);
      if (opened !== true) {
        fail(`fit ${s.name} (font ${font}): not opened: ${opened}`);
        fitRows.push({ name: s.name, dialog: s.dialog, state: s.state, font, opened });
        continue;
      }
      for (const size of FIT_SIZES) {
        await viewport(size.w, size.h, size.dpr);
        await sleep(350);
        const schemes = await measureFit(s, size, font, `${s.name}-${size.tag.replace('%', '')}-f${font}`);
        const row = { name: s.name, dialog: s.dialog, state: s.state, size: size.tag, font, unbounded: !!s.unbounded, schemes };
        if (size.tag === BREAKDOWN_SIZE) row.breakdown = await evaluate(BREAKDOWN);
        fitRows.push(row);
        fitLine(s, size.tag, font, schemes);
        if (opt.breakdown && row.breakdown && Object.values(schemes).some((x) => x.problems.length > 0)) printBreakdown(row.breakdown);
      }
    }
  }
  // the progress window's views, in the window the host gives them at 100 and 125 %
  for (const p of FIT_PROGRESS) {
    if (!wanted(p.name)) continue;
    for (const font of [16, 20]) {
      for (const dpr of [1, 1.25]) {
        state.font = font;
        await scheme('light');
        await viewport(520, (p.details ? 560 : 300) - FRAME, dpr);
        await load(`?progress=${p.id}`, { details: !!p.details });
        await playHost(520);
        for (const step of p.steps ?? []) {
          await evaluate(step);
          await sleep(400);
        }
        await playHost(520);
        await sleep(300);
        const tag = `520x${state.h}@${dpr * 100}%`;
        const schemes = await measureFit(p, { tag, strict: true }, font, `${p.name}-${dpr * 100}-f${font}`);
        const row = { name: p.name, dialog: p.dialog, state: p.state, size: tag, font, schemes };
        if (dpr === 1) row.breakdown = await evaluate(BREAKDOWN);
        fitRows.push(row);
        fitLine(p, tag, font, schemes);
        if (opt.breakdown && row.breakdown && Object.values(schemes).some((x) => x.problems.length > 0)) printBreakdown(row.breakdown);
      }
    }
  }
  state.font = 16;
  await load('?progress=1', { details: false });
}

if (opt.shotsLike) likeNames = new Set(readdirSync(opt.shotsLike).filter((f) => f.endsWith('.png')));
const { server, url } = await startVite(opt.root);
BASE = url;
const browser = await startChrome();
S = browser.S;
try {
  if (opt.check && opt.legacy) await checks();
  if (opt.check && opt.fit) await fitChecks();
  if (opt.shots) await screenshots();
} finally {
  await browser.stop();
  await server.close();
}
if (opt.shots) writeFileSync(join(opt.shots, `${opt.prefix}-measure.json`), JSON.stringify({ failures, results }, null, 1));
if (opt.json) writeFileSync(opt.json, JSON.stringify({ failures, fit: fitRows }, null, 1));
if (opt.check) {
  console.log(failures.length === 0 ? 'all checks passed' : `${failures.length} check(s) failed`);
  process.exitCode = failures.length === 0 ? 0 : 1;
}
