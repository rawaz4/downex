// chromium-layout: lays out the cases of layout-cases.json in headless Edge (the WebView2
// runtime's Chromium build) and writes every box's rectangle to layout-chromium.json, which
// TestLayoutMatchesChromium compares the kit's layout with.
//
// Usage (Windows, with Edge installed): node chromium-layout.mjs [--edge PATH]
// The browser runs headless with a fresh profile in a temporary folder that is removed after.
import { spawn } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const at = process.argv.indexOf('--edge');
const EDGE = at > 0 ? process.argv[at + 1] : 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe';
const cases = JSON.parse(readFileSync(join(here, 'layout-cases.json'), 'utf8'));

const px = (v) => (typeof v === 'number' ? `${v}px` : v);
const kebab = (k) => k.replace(/[A-Z]/g, (c) => '-' + c.toLowerCase());

function css(s = {}) {
  const out = [];
  for (const [k, v] of Object.entries(s)) {
    switch (k) {
      case 'margin':
      case 'padding':
        out.push(`${k}: ${v.map(px).join(' ')}`);
        break;
      case 'border':
        out.push(`border-style: solid; border-color: transparent; border-width: ${v.map(px).join(' ')}`);
        break;
      case 'flex':
      case 'display':
      case 'overflow':
        out.push(`${k}: ${v}`);
        break;
      default:
        out.push(`${kebab(k)}: ${px(v)}`);
    }
  }
  return out.join('; ');
}

function html(n) {
  if (n.words) {
    return `<div style="display: flex; flex-wrap: wrap">${n.words
      .map((w) => `<div style="flex: none; width: ${w}px; height: ${n.h}px"></div>`)
      .join('')}</div>`;
  }
  return `<div style="${css(n.s)}">${(n.k ?? []).map(html).join('')}</div>`;
}

const page = `<!doctype html><html><head><meta charset="utf-8"><style>
*, *::before, *::after { box-sizing: border-box; }
html { font-size: 100%; }
body { margin: 0; }
.case { position: relative; margin-bottom: 40px; }
</style></head><body>${cases
  .map((c, i) => `<div class="case" id="c${i}" style="width: ${c.w}px; height: ${c.h}px">${html(c.tree)}</div>`)
  .join('')}</body></html>`;

const measure = `(() => cases.map((c, i) => {
  const wrap = document.getElementById('c' + i);
  const o = wrap.getBoundingClientRect();
  const out = [];
  const walk = (el, n) => {
    const r = el.getBoundingClientRect();
    out.push([r.left - o.left, r.top - o.top, r.width, r.height]);
    if (n.words) {
      for (const k of el.children) {
        const q = k.getBoundingClientRect();
        out.push([q.left - o.left, q.top - o.top, q.width, q.height]);
      }
      return;
    }
    [...el.children].forEach((k, j) => walk(k, n.k[j]));
  };
  walk(wrap.firstElementChild, c.tree);
  return { name: c.name, rects: out };
}))()`;

const profile = mkdtempSync(join(tmpdir(), 'downex-kit-layout-'));
const child = spawn(EDGE, ['--headless=new', '--remote-debugging-port=0', `--user-data-dir=${profile}`, '--no-first-run',
  '--no-default-browser-check', '--disable-extensions', '--disable-sync', '--disable-background-networking',
  '--disable-component-update', '--force-color-profile=srgb', 'about:blank'], { stdio: ['ignore', 'ignore', 'pipe'] });
try {
  const endpoint = await new Promise((res, rej) => {
    let buf = '';
    const timer = setTimeout(() => rej(new Error('the browser did not start')), 20000);
    child.stderr.on('data', (d) => {
      buf += d.toString();
      const m = /DevTools listening on (ws:\/\/\S+)/.exec(buf);
      if (m) {
        clearTimeout(timer);
        res(m[1]);
      }
    });
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
  await S('Emulation.setDeviceMetricsOverride', { width: 1200, height: 800, deviceScaleFactor: 1, mobile: false });
  const { frameTree } = await S('Page.getFrameTree');
  await S('Page.setDocumentContent', { frameId: frameTree.frame.id, html: page });
  await S('Runtime.evaluate', { expression: `window.cases = ${JSON.stringify(cases)}` });
  const r = await S('Runtime.evaluate', { expression: measure, returnByValue: true });
  if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails));
  const { result: ver } = await S('Runtime.evaluate', { expression: 'navigator.userAgent', returnByValue: true });
  writeFileSync(
    join(here, 'layout-chromium.json'),
    JSON.stringify({ engine: ver.value, generated: new Date().toISOString(), cases: r.result.value }, null, 1) + '\n',
  );
  console.log(`wrote ${r.result.value.length} cases`);
  try {
    await send('Browser.close');
  } catch {
    child.kill();
  }
} finally {
  await new Promise((r) => (child.exitCode !== null ? r() : child.once('exit', r)));
  rmSync(profile, { recursive: true, force: true });
}
