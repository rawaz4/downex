// check-idle-render: a window must not keep working while downloads run.
//
// Loads the demo (the simulated daemon, whose Studio scene has running downloads) in headless
// Chrome, once as the main window and once as a progress window, lets start-up motion finish,
// then watches each for a few seconds:
//   - no animation or transition may run for most of that time (a 360 ms slide restarted by
//     every 250 ms progress report, or an endless hatch, kept the window drawing for the whole
//     download: 2026-10-29/30, downloads ran slower with the window open);
//   - the page may lay itself out at most MAX_LAYOUTS times a second (progress reports come
//     four times a second; a frame tween did it at every frame).
// Exit code 1 lists what kept running. Usage: node scripts/check-idle-render.mjs [--chrome PATH]
import { spawn } from 'node:child_process';
import { existsSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const root = resolve(here, '..');
const WATCH_MS = 5000;
const SAMPLE_MS = 500;
const MAX_SHARE = 0.5; // an animation running in more than half of the samples fails
const MAX_LAYOUTS = 8; // per second
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function findChrome() {
  const at = process.argv.indexOf('--chrome');
  const candidates = [
    at > 0 ? process.argv[at + 1] : undefined,
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

async function startVite() {
  process.env.VITE_DOWNEX_API = 'mock';
  const { createServer } = await import('vite');
  const cacheDir = mkdtempSync(join(tmpdir(), 'downex-idle-vite-'));
  const server = await createServer({
    root,
    configFile: join(root, 'vite.config.ts'),
    cacheDir,
    logLevel: 'error',
    clearScreen: false,
    server: { port: 0, strictPort: false, host: '127.0.0.1' },
  });
  server.httpServer?.once('close', () => rmSync(cacheDir, { recursive: true, force: true }));
  await server.listen();
  const url = server.resolvedUrls?.local?.[0];
  if (!url) throw new Error('vite did not report its address');
  return { server, url };
}

async function startChrome() {
  const profile = mkdtempSync(join(tmpdir(), 'downex-idle-'));
  const child = spawn(
    findChrome(),
    ['--headless=new', '--remote-debugging-port=0', `--user-data-dir=${profile}`, '--no-first-run',
      '--no-default-browser-check', '--disable-extensions', '--disable-sync', '--disable-background-networking',
      '--disable-component-update', 'about:blank'],
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
  await S('Performance.enable');
  await S('Emulation.setDeviceMetricsOverride', { width: 1200, height: 760, deviceScaleFactor: 1, mobile: false });
  // motion allowed: the check is about what runs when the viewer did not ask for less
  await S('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-reduced-motion', value: 'no-preference' }] });
  const stop = async () => {
    try {
      await send('Browser.close');
    } catch {
      child.kill();
    }
    await new Promise((r) => (child.exitCode !== null ? r() : child.once('exit', r)));
    rmSync(profile, { recursive: true, force: true });
  };
  return { S, stop };
}

const RUNNING = `(() => {
  const keys = new Set();
  for (const a of document.getAnimations()) {
    if (a.playState !== 'running') continue;
    const el = a.effect && a.effect.target;
    const what = a.animationName || a.transitionProperty || 'animation';
    const where = el ? el.tagName.toLowerCase() + [...el.classList].filter((c) => !c.startsWith('svelte-')).map((c) => '.' + c).join('') : '?';
    keys.add(what + ' on ' + where);
  }
  return [...keys];
})()`;

async function watch(S, base, view) {
  await S('Page.navigate', { url: base + view.query });
  for (let i = 0; i < 100; i++) {
    await sleep(100);
    const r = await S('Runtime.evaluate', { expression: `!!document.querySelector('[role=row][data-id], .pw')`, returnByValue: true }).catch(() => null);
    if (r?.result?.value) break;
  }
  await sleep(2500); // arrival and opening motion end
  // a notice the simulated daemon may open at start
  await S('Runtime.evaluate', { expression: `document.querySelector('[role=dialog] button[aria-label=Close],[role=alertdialog] button[aria-label=Close]')?.click()` });
  await sleep(1000);
  const metrics = async () => Object.fromEntries((await S('Performance.getMetrics')).metrics.map((m) => [m.name, m.value]));
  const m0 = await metrics();
  const counts = new Map();
  const samples = WATCH_MS / SAMPLE_MS;
  for (let i = 0; i < samples; i++) {
    const r = await S('Runtime.evaluate', { expression: RUNNING, returnByValue: true });
    for (const k of r.result.value ?? []) counts.set(k, (counts.get(k) ?? 0) + 1);
    await sleep(SAMPLE_MS);
  }
  const m1 = await metrics();
  const layouts = (m1.LayoutCount - m0.LayoutCount) / (WATCH_MS / 1000);
  const busy = [...counts].filter(([, n]) => n / samples > MAX_SHARE);
  return { layouts, busy, samples };
}

const VIEWS = [
  { name: 'main window', query: '' },
  { name: 'progress window', query: '?progress=1' },
];

const { server, url } = await startVite();
const { S, stop } = await startChrome();
let failed = false;
try {
  for (const view of VIEWS) {
    const { layouts, busy, samples } = await watch(S, url, view);
    const ok = busy.length === 0 && layouts <= MAX_LAYOUTS;
    failed ||= !ok;
    console.log(`${ok ? 'ok  ' : 'FAIL'} ${view.name}: ${layouts.toFixed(1)} layouts/s (max ${MAX_LAYOUTS})` +
      (busy.length ? `; running in most samples: ${busy.map(([k, n]) => `${k} (${n}/${samples})`).join(', ')}` : ''));
  }
} finally {
  await stop();
  await server.close();
}
process.exit(failed ? 1 : 0);
