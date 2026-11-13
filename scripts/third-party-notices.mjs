// Writes ui/THIRD-PARTY-NOTICES.txt: the licence texts of everything Downex's programs are
// built from that others wrote. The desktop window embeds the file (ui/about.go, Settings →
// About → Open-source notices).
//
//   node scripts/third-party-notices.mjs [--check]
//
// Run it from anywhere inside the repository after a dependency changes (go.mod, or
// ui/frontend's packages); --check only reports whether the file is up to date (exit 1
// when it is not). It needs Go and the frontend's installed packages (pnpm install in
// ui/frontend); it reads the module cache and node_modules and never downloads anything
// except, for a module missing from the cache, through "go mod download".
//
// What it collects:
// - Go: every module linked into downex, downexd, downex-nmhost and the windows, for Windows
//   (amd64 and arm64), macOS (with cgo: the menu-bar agent and the window) and Linux, from
//   "go list -deps", plus the Go standard library and runtime. The windows are the native one
//   (ui/native: windows/amd64, which the arm64 Setup ships too, and macOS with cgo) and the
//   earlier Wails window (ui, with the desktop,production tags), which -WebViewUI and -w still
//   build;
// - npm: every package whose code ends up in the Wails window's page, from a Vite build of
//   ui/frontend that is not written to disk (the modules in its chunks), not the
//   development tools;
// - each one's licence files (LICENSE, LICENCE, COPYING, NOTICE, PATENTS, UNLICENSE and
//   their variants) as shipped in the module or package. A Go module without one stops the
//   script; an npm package without one is listed with the licence its package.json names.
// yt-dlp and Deno are not part of Downex: they are listed as programs downloaded separately.

import { execFileSync } from 'node:child_process';
import { existsSync, readFileSync, readdirSync, writeFileSync } from 'node:fs';
import { dirname, join, relative, sep } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const frontend = join(root, 'ui', 'frontend');
const outFile = join(root, 'ui', 'THIRD-PARTY-NOTICES.txt');
const check = process.argv.includes('--check');

const licenceName = /^(licen[cs]e|copying|notice|patents|unlicense)([.\-_].*)?$/i;

function licenceFiles(dir) {
  let names;
  try {
    names = readdirSync(dir, { withFileTypes: true });
  } catch {
    return [];
  }
  return names
    .filter((e) => e.isFile() && licenceName.test(e.name))
    .map((e) => e.name)
    .sort((a, b) => a.localeCompare(b, 'en'))
    .map((name) => ({ name, text: readFileSync(join(dir, name), 'utf8').replace(/\r\n/g, '\n').trimEnd() }));
}

function go(args, env = {}) {
  return execFileSync('go', args, { cwd: root, env: { ...process.env, ...env }, encoding: 'utf8', maxBuffer: 64 << 20 });
}

// ---- Go modules -----------------------------------------------------------------------

const cmds = ['./cmd/downex', './cmd/downexd', './cmd/downex-nmhost'];
const native = './ui/native';
const targets = [
  { GOOS: 'windows', GOARCH: 'amd64', CGO_ENABLED: '0', tags: '', pkgs: [...cmds, native] },
  { GOOS: 'windows', GOARCH: 'amd64', CGO_ENABLED: '0', tags: 'desktop,production', pkgs: ['./ui'] },
  { GOOS: 'windows', GOARCH: 'arm64', CGO_ENABLED: '0', tags: '', pkgs: cmds },
  { GOOS: 'darwin', GOARCH: 'arm64', CGO_ENABLED: '1', tags: '', pkgs: [...cmds, native] },
  { GOOS: 'darwin', GOARCH: 'arm64', CGO_ENABLED: '1', tags: 'desktop,production', pkgs: ['./ui'] },
  { GOOS: 'darwin', GOARCH: 'amd64', CGO_ENABLED: '1', tags: '', pkgs: [...cmds, native] },
  { GOOS: 'darwin', GOARCH: 'amd64', CGO_ENABLED: '1', tags: 'desktop,production', pkgs: ['./ui'] },
  { GOOS: 'linux', GOARCH: 'amd64', CGO_ENABLED: '0', tags: '', pkgs: cmds },
];

const modules = new Map(); // path → {path, version, dir}
for (const t of targets) {
  const format = '{{with .Module}}{{if not .Main}}{{.Path}}\t{{.Version}}\t{{.Dir}}\t{{with .Replace}}{{.Path}}\t{{.Version}}\t{{.Dir}}{{end}}{{end}}{{end}}';
  const args = ['list', '-e', '-deps', '-f', format];
  if (t.tags) args.push('-tags', t.tags);
  const out = go([...args, ...t.pkgs], { GOOS: t.GOOS, GOARCH: t.GOARCH, CGO_ENABLED: t.CGO_ENABLED, GOFLAGS: '-mod=readonly' });
  for (const line of out.split(/\r?\n/)) {
    if (!line.trim()) continue;
    const [path, version, dir, rpath, rversion, rdir] = line.split('\t');
    const key = path;
    if (modules.has(key)) continue;
    modules.set(key, rpath ? { path, version: rversion || version, dir: rdir || dir, replacedBy: rpath } : { path, version, dir });
  }
}

const missing = [];
const goSections = [];
for (const m of [...modules.values()].sort((a, b) => a.path.localeCompare(b.path, 'en'))) {
  let dir = m.dir;
  if (!dir) {
    const info = JSON.parse(go(['mod', 'download', '-json', `${m.path}@${m.version}`]));
    dir = info.Dir;
  }
  const files = licenceFiles(dir);
  if (files.length === 0) {
    missing.push(`${m.path} ${m.version}`);
    continue;
  }
  const title = `Go module ${m.path} ${m.version}${m.replacedBy ? ` (replaced by ${m.replacedBy})` : ''}`;
  goSections.push(section(title, files));
}
if (missing.length > 0) {
  console.error(`No licence file found in these Go modules:\n  ${missing.join('\n  ')}`);
  process.exit(2);
}

const goroot = go(['env', 'GOROOT']).trim();
const goVersion = go(['env', 'GOVERSION']).trim();
const stdlib = section(`The Go Programming Language (standard library and runtime, ${goVersion})`, licenceFiles(goroot));

// ---- npm packages bundled into the page ---------------------------------------------

async function bundledPackages() {
  const viteEntry = join(frontend, 'node_modules', 'vite', 'dist', 'node', 'index.js');
  if (!existsSync(viteEntry)) {
    console.error('ui/frontend has no installed packages: run "pnpm install" there first');
    process.exit(2);
  }
  const { build } = await import(pathToFileURL(viteEntry).href);
  const ids = new Set();
  await build({
    root: frontend,
    configFile: join(frontend, 'vite.config.ts'),
    logLevel: 'silent',
    build: { write: false, emptyOutDir: false, reportCompressedSize: false },
    plugins: [
      {
        name: 'downex-collect-modules',
        generateBundle(_options, bundle) {
          for (const chunk of Object.values(bundle)) {
            if (chunk.type === 'chunk') for (const id of Object.keys(chunk.modules)) ids.add(id);
          }
        },
      },
    ],
  });
  const dirs = new Map(); // package dir → true
  for (const raw of ids) {
    const id = raw.replace(/\0/g, '').split('?')[0];
    if (/^vite\//.test(id)) {
      dirs.set(join(frontend, 'node_modules', 'vite'), true); // Vite's own helpers (module preload)
      continue;
    }
    const norm = id.split('/').join(sep);
    const at = norm.lastIndexOf(`node_modules${sep}`);
    if (at < 0) continue; // the page's own code
    const rest = norm.slice(at + `node_modules${sep}`.length).split(sep);
    const name = rest[0].startsWith('@') ? rest.slice(0, 2) : rest.slice(0, 1);
    dirs.set(norm.slice(0, at) + `node_modules${sep}` + name.join(sep), true);
  }
  const pkgs = [];
  for (const dir of dirs.keys()) {
    const pkg = JSON.parse(readFileSync(join(dir, 'package.json'), 'utf8'));
    pkgs.push({ name: pkg.name, version: pkg.version, license: typeof pkg.license === 'string' ? pkg.license : '', dir });
  }
  return pkgs.sort((a, b) => a.name.localeCompare(b.name, 'en'));
}

const npmSections = [];
for (const p of await bundledPackages()) {
  const files = licenceFiles(p.dir);
  const title = `npm package ${p.name} ${p.version}${p.license ? ` (${p.license})` : ''}`;
  npmSections.push(
    files.length > 0 ? section(title, files) : `${banner(title)}\nThe package ships no licence file; its package.json names the licence ${p.license || '(none)'}.\n`,
  );
}

// ---- the file -------------------------------------------------------------------------

function banner(title) {
  return `${'='.repeat(78)}\n${title}\n${'='.repeat(78)}`;
}

function section(title, files) {
  const body = files.map((f) => (files.length > 1 ? `--- ${f.name} ---\n${f.text}` : f.text)).join('\n\n');
  return `${banner(title)}\n${body}\n`;
}

const header = `THIRD-PARTY NOTICES

Downex is built with software that other people wrote and published under open-source
licences. This file reproduces those licences for every part of it that is included in
Downex's programs (the window, downexd, the downex command and the browser helper):
the Go standard library and runtime, the Go modules they link, and the npm packages
whose code is part of the window's page. Each section names the module or package and
its version.

Programs Downex downloads separately
------------------------------------
Downex does not include these programs. When video pages are used, each Downex copy
downloads them from their publishers' releases, checks them, keeps them in its tools
folder and runs them as separate programs. Their own licences apply to them:

- yt-dlp (https://github.com/yt-dlp/yt-dlp): released into the public domain under
  The Unlicense; its release builds contain further components under their own licences,
  which yt-dlp lists in its THIRD_PARTY_LICENSES.txt.
- Deno (https://github.com/denoland/deno): MIT License; its release builds contain further
  components under their own licences.

This file is generated by scripts/third-party-notices.mjs; do not edit it by hand.
`;

const text = [header, stdlib, ...goSections, ...npmSections].join('\n');
const current = existsSync(outFile) ? readFileSync(outFile, 'utf8') : '';
if (check) {
  if (current !== text) {
    console.error(`${relative(root, outFile)} is out of date: run node scripts/third-party-notices.mjs`);
    process.exit(1);
  }
  console.log(`${relative(root, outFile)} is up to date (${goSections.length} Go modules, ${npmSections.length} npm packages).`);
} else {
  writeFileSync(outFile, text);
  console.log(`wrote ${relative(root, outFile)}: Go ${goVersion}, ${goSections.length} Go modules, ${npmSections.length} npm packages.`);
}
