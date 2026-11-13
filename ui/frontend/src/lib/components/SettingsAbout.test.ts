import { fireEvent, render, screen, within } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import type { BuildInfo } from '../api';
import { ToolsStore } from '../stores/tools.svelte';
import { fakeApi, makeTools } from '../testing';
import SettingsAbout from './SettingsAbout.svelte';
import SettingsDialog from './SettingsDialog.svelte';

const BUILD: BuildInfo = {
  version: '1.0.0-test',
  os: 'windows',
  arch: 'amd64',
  goVersion: 'go1.27.0',
  wailsVersion: 'v2.17.0',
  commit: '',
  commitTime: 0,
  modified: false,
};

const EDGE = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36 Edg/141.0.3537.71';

function setup(extra: Record<string, unknown> = {}) {
  const api = fakeApi([]);
  api.tools = makeTools();
  const tools = new ToolsStore(api);
  const copied: string[] = [];
  const onpage = vi.fn();
  const props = {
    build: BUILD,
    status: () => api.status(),
    tools,
    updateFeedUrl: '',
    autoCheckUpdates: true,
    defaultDir: '/home/you/Downloads',
    siteMedia: true,
    openAppFolder: (f: Parameters<typeof api.openAppFolder>[0]) => api.openAppFolder(f),
    diagnostics: (o: { webview?: string }) => api.diagnostics(o),
    thirdPartyNotices: () => api.thirdPartyNotices(),
    copyText: async (t: string) => void copied.push(t),
    userAgent: EDGE,
    onpage,
    ...extra,
  };
  const utils = render(SettingsAbout, { props });
  return { ...utils, api, tools, copied, onpage };
}

/** The value shown beside a label of the "This copy" card. */
const fact = (label: string) => {
  const dt = screen.getAllByText(label).find((el) => el.tagName === 'DT');
  return dt?.nextElementSibling?.textContent?.replace(/\s+/g, ' ').trim() ?? null;
};

describe('Settings → About', () => {
  it('says which Downex this is: the window, downexd and the build (never the IPC endpoint)', async () => {
    const { container } = setup();
    expect(screen.getByRole('heading', { name: 'Downex' })).toBeTruthy();
    expect(screen.getByText('Version 1.0.0-test')).toBeTruthy();
    expect(fact('Window')).toBe('1.0.0-test · Windows x64 · WebView2 141.0.3537.71');
    expect(fact('Built with')).toBe('Go 1.27.0 · Wails 2.17.0');
    await vi.waitFor(() => expect(fact('downexd')).toMatch(/^1\.0\.0-test · running since .+ · process 1$/));
    expect(screen.queryByRole('alert')).toBeNull();
    expect(container.textContent).not.toContain('127.0.0.1:47820');
  });

  it('warns when the window and downexd are different versions', async () => {
    setup({ build: { ...BUILD, version: '1.1.0' } });
    const warning = await screen.findByRole('alert');
    expect(warning.textContent).toContain('This window is version 1.1.0, but downexd is version 1.0.0-test.');
  });

  it('shows the commit only when the build has one', () => {
    setup({ build: { ...BUILD, commit: 'abcdef0123456789', modified: false } });
    expect(fact('Built with')).toBe('Go 1.27.0 · Wails 2.17.0 · Commit abcdef012345');
  });

  it('says why updates are off, and opens the Updates page', async () => {
    const { onpage } = setup({ updateFeedUrl: 'https://u.example/update.json' });
    await vi.waitFor(() => expect(screen.getByText('Off in this build (no signing key)')).toBeTruthy());
    await fireEvent.click(screen.getByRole('button', { name: 'Update settings' }));
    expect(onpage).toHaveBeenCalledWith('updates');
  });

  it('shows yt-dlp and Deno and checks for their updates', async () => {
    const { api } = setup();
    const tools = await screen.findByRole('list', { name: 'Video tools' });
    await vi.waitFor(() => expect(within(tools).getAllByRole('listitem')).toHaveLength(2));
    expect(tools.textContent).toContain('yt-dlp');
    expect(tools.textContent).toContain('2026.10.10.233141');
    expect(tools.textContent).toContain('Deno');
    await fireEvent.click(screen.getByRole('button', { name: 'Check for tool updates' }));
    await vi.waitFor(() => expect(api.args.get('installTools')).toEqual([true]));
  });

  it('opens and copies Downex’s folders; the host resolves the paths', async () => {
    const { api, copied } = setup();
    const folders = screen.getByRole('list', { name: 'Folders' });
    const row = (label: string) => within(folders).getAllByRole('listitem').find((li) => li.textContent?.includes(label))!;
    await vi.waitFor(() => expect(row('Logs').textContent).toContain('/home/you/.local/state/Downex'));
    await fireEvent.click(within(row('Logs')).getByRole('button', { name: 'Open the Logs folder' }));
    await fireEvent.click(within(row('Downloads')).getByRole('button', { name: 'Open the Downloads folder' }));
    expect(api.folders).toEqual(['logs', 'downloads']);
    await fireEvent.click(within(row('Data')).getByRole('button', { name: 'Copy the path of the Data folder' }));
    await vi.waitFor(() => expect(copied).toEqual(['/home/you/.config/Downex']));
  });

  it('separates each folder’s name from its hint with a spaced dot', () => {
    const { container } = setup();
    const labels = [...container.querySelectorAll('.folder-label')].map((el) => el.textContent);
    expect(labels[0]).toBe('Data · The list of downloads and the settings');
    for (const label of labels) expect(label).toMatch(/^\S.* · \S/);
  });

  it('cannot copy a folder an older downexd does not report', async () => {
    const status = await fakeApi([]).status();
    setup({ status: async () => ({ ...status, logDir: '', toolsDir: undefined }) });
    const folders = screen.getByRole('list', { name: 'Folders' });
    const logs = within(folders).getAllByRole('listitem').find((li) => li.textContent?.includes('Logs'))!;
    await vi.waitFor(() => expect(logs.textContent).toContain('Not reported by this downexd'));
    expect(within(logs).getByRole('button', { name: 'Copy the path of the Logs folder' }).hasAttribute('disabled')).toBe(true);
  });

  it('copies diagnostics with the WebView2 version, never the endpoint or the update address', async () => {
    const { api, copied } = setup({ updateFeedUrl: 'https://u.example/update.json' });
    api.diagnosticsText = 'Downex 1.0.0-test\nendpoint 127.0.0.1:47820\nfeed https://u.example/update.json\n';
    await vi.waitFor(() => expect(api.calls).toContain('status'));
    await fireEvent.click(screen.getByRole('button', { name: 'Copy diagnostics' }));
    await vi.waitFor(() => expect(copied).toHaveLength(1));
    expect(api.args.get('diagnostics')).toEqual([{ webview: '141.0.3537.71' }]);
    expect(copied[0]).toContain('Downex 1.0.0-test');
    expect(copied[0]).not.toContain('127.0.0.1:47820');
    expect(copied[0]).not.toContain('u.example');
    expect(screen.getByRole('status').textContent).toContain('Diagnostics copied');
  });

  it('shows the open-source notices, one component at a time, and comes back', async () => {
    const { api } = setup();
    api.notices = ['THIRD-PARTY NOTICES', '', 'Intro.', '', '='.repeat(20), 'github.com/wailsapp/wails/v2 v2.17.0', '='.repeat(20), 'MIT License', ''].join('\n');
    const view = screen.getByRole('button', { name: 'View open-source notices' });
    await fireEvent.click(view);
    const heading = await screen.findByRole('heading', { name: 'Open-source notices' });
    expect(heading).toBeTruthy();
    expect(screen.getByText('github.com/wailsapp/wails/v2 v2.17.0')).toBeTruthy();
    expect(screen.queryByRole('heading', { name: 'Downex' })).toBeNull();
    await fireEvent.click(screen.getByRole('button', { name: 'Back to About' }));
    expect(await screen.findByRole('heading', { name: 'Downex' })).toBeTruthy();
    await vi.waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'View open-source notices' })));
  });
});

describe('Settings → About in the Settings window', () => {
  it('is the last page of the side navigation', async () => {
    const api = fakeApi([]);
    render(SettingsDialog, {
      props: {
        load: () => api.getSettings(),
        save: (s) => api.updateSettings(s),
        pickFolder: async () => null,
        onclose: vi.fn(),
        about: { build: BUILD, status: () => api.status(), userAgent: EDGE },
      },
    });
    const tabs = screen.getAllByRole('tab');
    expect(tabs.at(-1)?.textContent?.trim()).toBe('About');
    await fireEvent.click(tabs.at(-1)!);
    expect(await screen.findByRole('heading', { name: 'Downex' })).toBeTruthy();
    await vi.waitFor(() => expect(fact('downexd')).toMatch(/^1\.0\.0-test/));
  });
});
