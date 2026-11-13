// Settings → About (DESIGN-SYSTEM.md §4.9.1): the texts of what this copy of Downex is, whether
// updates can work, the diagnostics' last guard and the open-source notices. Pure functions; the
// page (SettingsAbout.svelte) shows them.

import type { BuildInfo, Status } from './api';

/** The system as people know it: "Windows x64", "macOS (Apple silicon)", "Linux x64". */
export function osLabel(build: Pick<BuildInfo, 'os' | 'arch'>): string {
  const { os, arch } = build;
  switch (os) {
    case 'windows':
      return arch === 'arm64' ? 'Windows on Arm' : arch === 'amd64' ? 'Windows x64' : `Windows ${arch}`;
    case 'darwin':
      return arch === 'arm64' ? 'macOS (Apple silicon)' : arch === 'amd64' ? 'macOS (Intel)' : `macOS ${arch}`;
    case 'linux':
      return arch === 'amd64' ? 'Linux x64' : arch === 'arm64' ? 'Linux ARM64' : `Linux ${arch}`;
    default:
      return `${os}/${arch}`;
  }
}

/** What the window is built with: Go, Wails, and the commit when the build has one. */
export function buildLines(build: BuildInfo): string[] {
  const lines: string[] = [];
  if (build.goVersion) lines.push(`Go ${build.goVersion.replace(/^go/, '')}`);
  if (build.wailsVersion) lines.push(`Wails ${build.wailsVersion.replace(/^v/, '')}`);
  if (build.commit) lines.push(`Commit ${build.commit.slice(0, 12)}${build.modified ? ' (with uncommitted changes)' : ''}`);
  return lines;
}

/** The window and downexd are different versions (an update that is not finished, or a stray downexd). */
export function versionsDiffer(windowVersion: string, daemonVersion: string): boolean {
  const norm = (v: string) => v.trim().replace(/^v/, '');
  if (norm(windowVersion) === '' || norm(daemonVersion) === '') return false;
  return norm(windowVersion) !== norm(daemonVersion);
}

/**
 * Whether Downex can update itself: not in a build without a signing key (downexd's
 * updatesSupported), not without an update address (the saved settings), else how it checks.
 * `status` null: downexd could not be asked.
 */
export function updatesState(
  status: Pick<Status, 'updatesSupported'> | null,
  feedUrl: string,
  autoCheck: boolean,
): { on: boolean; text: string } {
  if (status === null) return { on: false, text: 'Not known while downexd is not reachable' };
  if (status.updatesSupported === false) return { on: false, text: 'Off in this build (no signing key)' };
  if (feedUrl.trim() === '') return { on: false, text: 'Off (no update address)' };
  return { on: true, text: autoCheck ? 'On · checked every day' : 'On · checked when you ask' };
}

/**
 * The diagnostics as copied: the host already leaves out the endpoint, addresses, names, logins and
 * the proxy; what the page knows must stay private (the IPC endpoint, the update address) is
 * hidden here once more, should a host ever let it through.
 */
export function scrubDiagnostics(text: string, privateValues: readonly string[]): string {
  let out = text;
  for (const v of privateValues) {
    if (v.trim().length < 4) continue;
    out = out.split(v).join('(hidden)');
  }
  return out;
}

export interface NoticeSection {
  /** "github.com/wailsapp/wails/v2 v2.17.0". */
  readonly title: string;
  readonly body: string;
}

/** THIRD-PARTY-NOTICES.txt: its introduction, then one section per component. */
export interface Notices {
  readonly intro: string;
  readonly sections: readonly NoticeSection[];
}

const RULE = /^={10,}\s*$/;

/** A block of the notices' introduction as the page shows it. */
export interface NoticeBlock {
  readonly kind: 'heading' | 'para' | 'item';
  readonly text: string;
}

/**
 * The introduction reflowed for the page (the file is wrapped for a text editor): paragraphs, a
 * line underlined with "-" or "=" as a heading, "- " lines as list items (their indented
 * continuation joined). The file's own title ("THIRD-PARTY NOTICES") is left out: the view has one.
 */
export function introBlocks(intro: string): NoticeBlock[] {
  const lines = intro.replace(/\r\n?/g, '\n').split('\n');
  if (/^third-party notices$/i.test((lines[0] ?? '').trim())) lines.shift();
  const blocks: { kind: NoticeBlock['kind']; text: string }[] = [];
  let open: { kind: NoticeBlock['kind']; text: string } | null = null;
  for (const raw of lines) {
    const line = raw.trim();
    if (line === '') {
      open = null;
      continue;
    }
    if (/^[-=]{3,}$/.test(line)) {
      // a setext underline: the paragraph above it is a heading
      if (open && open.kind === 'para') open.kind = 'heading';
      open = null;
      continue;
    }
    if (line.startsWith('- ')) {
      open = { kind: 'item', text: line.slice(2).trim() };
      blocks.push(open);
      continue;
    }
    if (open) open.text += ` ${line}`;
    else {
      open = { kind: 'para', text: line };
      blocks.push(open);
    }
  }
  return blocks;
}

/**
 * Splits the notices file (scripts/third-party-notices.mjs) at its section headings: a title
 * between two rules of "=". A file without them is all introduction.
 */
export function parseNotices(text: string): Notices {
  const lines = text.replace(/\r\n?/g, '\n').split('\n');
  const intro: string[] = [];
  const sections: { title: string; body: string[] }[] = [];
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i] ?? '';
    if (RULE.test(line) && i + 2 < lines.length && RULE.test(lines[i + 2] ?? '') && (lines[i + 1] ?? '').trim() !== '') {
      sections.push({ title: (lines[i + 1] ?? '').trim(), body: [] });
      i += 2;
      continue;
    }
    const current = sections.at(-1);
    if (current) current.body.push(line);
    else intro.push(line);
  }
  return {
    intro: intro.join('\n').trim(),
    sections: sections.map((s) => ({ title: s.title, body: s.body.join('\n').trim() })),
  };
}
