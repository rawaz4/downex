import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { DEFAULT_SETTINGS, type Settings, type ToolsStatus } from '../api';
import { density } from '../stores/density.svelte';
import { theme } from '../stores/theme.svelte';
import { ToolsStore } from '../stores/tools.svelte';
import { chooseOption, makeTools, optionLabels } from '../testing';
import SettingsDialog from './SettingsDialog.svelte';

const loaded: Settings = {
  ...DEFAULT_SETTINGS,
  maxConcurrentDownloads: 4,
  globalSpeedLimit: 2 * 1000 * 1000,
  defaultDir: '/home/you/Downloads',
  categories: [
    { name: 'Video', extensions: ['mp4', 'mkv'], dir: '' },
    { name: 'Books', extensions: ['epub'], dir: '/data/books' },
  ],
  confirmBrowserDownloads: false,
  launchAtLogin: true,
};

function setup(initial: Settings = loaded, props: Record<string, unknown> = {}) {
  const save = vi.fn(async (s: Settings) => s);
  const onsaved = vi.fn();
  const onclose = vi.fn();
  const pickFolder = vi.fn(async () => '/picked');
  render(SettingsDialog, { props: { load: async () => initial, save, pickFolder, onclose, onsaved, ...props } });
  return { save, onsaved, onclose, pickFolder };
}

const saveButton = () => screen.getByRole('button', { name: 'Save' });
/** The settings' on/off rows are switches (native checkboxes with role="switch"). */
const toggle = (name: RegExp) => screen.getByRole('switch', { name }) as HTMLInputElement;
const tab = (name: string) => screen.getByRole('tab', { name: new RegExp(`^${name}`) });
/** The settings are loaded (General's switches are enabled then). */
const loadedGeneral = () => waitFor(() => expect(toggle(/sign in/).disabled).toBe(false));
/** The settings are loaded, on Downloads. */
const loadedDownloads = () => waitFor(() => expect((screen.getByLabelText('Default folder') as HTMLInputElement).disabled).toBe(false));

describe('SettingsDialog', () => {
  it('writes every setting back, the ones it does not show included', async () => {
    const newer = { ...loaded, fieldOfANewerDaemon: 7 } as Settings;
    const { save, onsaved, onclose } = setup(newer);
    await loadedGeneral();
    expect(toggle(/sign in/).checked).toBe(true);
    await fireEvent.click(tab('Downloads'));
    expect((screen.getByLabelText('Default folder') as HTMLInputElement).value).toBe('/home/you/Downloads');
    expect(toggle(/category's folder/).checked).toBe(true);
    // Asking before browser downloads moved to Integration.
    await fireEvent.click(tab('Integration'));
    expect(toggle(/Ask before saving downloads from the browser/).checked).toBe(false);
    await fireEvent.click(saveButton());
    await waitFor(() => expect(onclose).toHaveBeenCalled());
    expect(save).toHaveBeenCalledWith(newer);
    expect(onsaved).toHaveBeenCalledWith(newer);
  });

  it('edits categories and switches', async () => {
    const { save, pickFolder } = setup(loaded, { initialPage: 'downloads' });
    await loadedDownloads();
    const name0 = await screen.findByDisplayValue('Video');
    await fireEvent.input(name0, { target: { value: 'Movies' } });
    const list = screen.getByRole('list');
    const rows = within(list).getAllByRole('listitem');
    expect(rows).toHaveLength(2);
    await fireEvent.input(within(rows[0]!).getByLabelText('Extensions'), { target: { value: 'MP4, .webm' } });
    // remove Books, add Comics with a folder from the picker
    await fireEvent.click(screen.getByRole('button', { name: 'Remove Books' }));
    await fireEvent.click(screen.getByRole('button', { name: /Add category/ }));
    const added = within(screen.getByRole('list')).getAllByRole('listitem')[1]!;
    const addedName = within(added).getByLabelText('Name');
    expect(document.activeElement).toBe(addedName);
    await fireEvent.input(addedName, { target: { value: 'Comics' } });
    await fireEvent.input(within(added).getByLabelText('Extensions'), { target: { value: 'cbz cbr' } });
    await fireEvent.click(within(added).getByRole('button', { name: 'Choose the folder of Comics' }));
    await waitFor(() => expect((within(added).getByLabelText('Folder') as HTMLInputElement).value).toBe('/picked'));
    expect(pickFolder).toHaveBeenCalledWith('/home/you/Downloads/Comics');
    await fireEvent.click(toggle(/category's folder/));
    await fireEvent.click(tab('General'));
    await fireEvent.click(toggle(/notification area/));
    await fireEvent.click(tab('Integration'));
    await fireEvent.click(toggle(/Ask before saving/));

    await fireEvent.click(saveButton());
    await waitFor(() => expect(save).toHaveBeenCalled());
    expect(save).toHaveBeenCalledWith({
      ...loaded,
      categories: [
        { name: 'Movies', extensions: ['mp4', 'webm'], dir: '' },
        { name: 'Comics', extensions: ['cbz', 'cbr'], dir: '/picked' },
      ],
      useCategoryFolders: false,
      showTrayIcon: false,
      confirmBrowserDownloads: true,
    });
  });

  it('shows and saves the progress window switches', async () => {
    const { save } = setup({ ...loaded, showProgressWindow: true, showCompleteDialog: false });
    await waitFor(() => expect(toggle(/progress window when I start/).disabled).toBe(false));
    expect(toggle(/progress window when I start/).checked).toBe(true);
    expect(toggle(/download complete dialog/).checked).toBe(false);
    expect(screen.getByRole('heading', { name: 'Progress windows' })).toBeTruthy();
    await fireEvent.click(toggle(/progress window when I start/));
    await fireEvent.click(toggle(/download complete dialog/));
    await fireEvent.click(saveButton());
    await waitFor(() => expect(save).toHaveBeenCalled());
    expect(save).toHaveBeenCalledWith({ ...loaded, showProgressWindow: false, showCompleteDialog: true });
  });

  it('shows what is wrong with a category and does not save', async () => {
    const { save } = setup(loaded, { initialPage: 'downloads' });
    await loadedDownloads();
    const books = await screen.findByDisplayValue('Books');
    await fireEvent.input(books, { target: { value: 'video' } });
    const rows = within(screen.getByRole('list')).getAllByRole('listitem');
    await fireEvent.input(within(rows[1]!).getByLabelText('Extensions'), { target: { value: 'ok no/pe' } });
    await fireEvent.click(saveButton());
    await waitFor(() => expect(books.getAttribute('aria-invalid')).toBe('true'));
    expect(screen.getByText('Another category has this name')).toBeTruthy();
    expect(screen.getByText(/Not an extension: "no\/pe"/)).toBeTruthy();
    expect(document.activeElement).toBe(books);
    expect(save).not.toHaveBeenCalled();
  });
});

const m6: Settings = {
  ...loaded,
  proxy: {
    mode: 'manual',
    httpProxy: 'proxy.lan:8080',
    httpsProxy: '',
    socksProxy: '',
    bypass: '<local>',
    pacUrl: '',
    username: 'me',
    hasPassword: true,
  },
  hostRules: [{ pattern: '*.example.com', maxConnections: 4, speedLimit: 1024 * 1024, maxRequestBytes: 10 * 1024 * 1024 }],
  siteLogins: [
    { id: 'a1', urlPattern: 'https://files.example.com/*', username: 'anna', hasPassword: true },
    { id: 'b2', urlPattern: '*.example.org', username: 'bob', hasPassword: false },
  ],
  clipboardMonitor: true,
  markDownloadedFiles: false,
  avScanEnabled: true,
  avScanCommand: '/usr/bin/clamscan',
  avScanArgs: '--no-summary [File]',
  updateFeedUrl: 'https://updates.example.com/downex.json',
  autoCheckUpdates: false,
};

function setupM6(initial: Settings = m6, props: Record<string, unknown> = {}) {
  let stored = initial;
  let nextId = 1;
  const save = vi.fn(async (s: Settings) => {
    // As downexd: new site logins get an id.
    stored = { ...s, siteLogins: s.siteLogins.map((l) => (l.id ? l : { ...l, id: `new-${nextId++}` })) };
    return stored;
  });
  const load = vi.fn(async () => stored);
  const setSecret = vi.fn(async () => {});
  const clearSecret = vi.fn(async () => {});
  const onsaved = vi.fn();
  const onclose = vi.fn();
  const pickFile = vi.fn(async () => '/opt/scanner/scan');
  render(SettingsDialog, {
    props: { load, save, pickFolder: async () => null, onclose, onsaved, setSecret, clearSecret, pickFile, ...props },
  });
  return { save, load, setSecret, clearSecret, onsaved, onclose, pickFile };
}

describe('SettingsDialog: M6 pages', () => {
  it('writes every M6 setting back unchanged', async () => {
    const { save, setSecret, clearSecret } = setupM6();
    await loadedGeneral();
    // Visiting the pages changes nothing.
    for (const name of ['Downloads', 'Network', 'Integration', 'Video sites', 'Appearance', 'Updates', 'General']) {
      await fireEvent.click(tab(name));
    }
    await fireEvent.click(saveButton());
    await waitFor(() => expect(save).toHaveBeenCalled());
    expect(save).toHaveBeenCalledWith(m6);
    expect(setSecret).not.toHaveBeenCalled();
    expect(clearSecret).not.toHaveBeenCalled();
  });

  it('edits the proxy, and stores its password only through setSecret after saving', async () => {
    const { save, setSecret, onsaved, onclose } = setupM6();
    await loadedGeneral();
    await fireEvent.click(tab('Network'));
    expect(tab('Network').getAttribute('aria-selected')).toBe('true');
    const password = screen.getByLabelText('Proxy password') as HTMLInputElement;
    // A stored password is never shown: the field is empty and says so.
    expect(password.type).toBe('password');
    expect(password.value).toBe('');
    expect(password.placeholder).toBe('Saved (type to replace it)');
    await fireEvent.input(screen.getByLabelText('HTTP proxy'), { target: { value: 'proxy.lan' } });
    await fireEvent.input(screen.getByLabelText('SOCKS proxy'), { target: { value: 'socks5://127.0.0.1:1080' } });
    await fireEvent.input(password, { target: { value: 's3cret' } });
    await fireEvent.click(tab('General'));
    await fireEvent.click(saveButton());
    // The error is on Network: the dialog shows it there.
    await waitFor(() => expect(tab('Network').getAttribute('aria-selected')).toBe('true'));
    expect(screen.getByText(/Enter host:port/)).toBeTruthy();
    expect(document.activeElement).toBe(screen.getByLabelText('HTTP proxy'));
    expect(save).not.toHaveBeenCalled();

    await fireEvent.input(screen.getByLabelText('HTTP proxy'), { target: { value: 'proxy.lan:3128' } });
    await fireEvent.click(saveButton());
    await waitFor(() => expect(onclose).toHaveBeenCalled());
    const sent = save.mock.calls[0]?.[0] as Settings;
    expect(sent.proxy).toEqual({ ...m6.proxy, httpProxy: 'proxy.lan:3128', socksProxy: 'socks5://127.0.0.1:1080' });
    expect(JSON.stringify(sent)).not.toContain('s3cret');
    expect(setSecret).toHaveBeenCalledWith('proxy', '', 's3cret');
    expect(onsaved).toHaveBeenCalled();
  });

  it('adds, edits and removes site logins; new passwords go to the ids downexd gives', async () => {
    const { save, setSecret, clearSecret, load } = setupM6();
    await loadedGeneral();
    await fireEvent.click(tab('Network'));
    const logins = screen.getByRole('table', { name: 'Site logins' });
    expect(within(logins).getAllByRole('row')).toHaveLength(3);
    expect(within(logins).getByText('Saved')).toBeTruthy();
    expect(within(logins).getByText('None')).toBeTruthy();

    await fireEvent.click(screen.getByRole('button', { name: 'Add site login' }));
    expect(document.activeElement).toBe(screen.getByLabelText('Address'));
    await fireEvent.input(screen.getByLabelText('Address'), { target: { value: 'https://private.example.net/*' } });
    await fireEvent.input(screen.getByLabelText('User name'), { target: { value: 'carol' } });
    await fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'pw-carol' } });
    // Enter applies the login, not the whole dialog.
    await fireEvent.keyDown(screen.getByLabelText('Password'), { key: 'Enter' });
    expect(save).not.toHaveBeenCalled();
    expect(within(screen.getByRole('table', { name: 'Site logins' })).getByText('New password (saved with the settings)')).toBeTruthy();

    // bob gets a password; anna is removed (with her stored password).
    await fireEvent.click(screen.getByRole('button', { name: 'Edit the login for *.example.org' }));
    await fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'pw-bob' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Apply' }));
    await fireEvent.click(screen.getByRole('button', { name: 'Remove the login for https://files.example.com/*' }));

    await fireEvent.click(saveButton());
    await waitFor(() => expect(setSecret).toHaveBeenCalledTimes(2));
    const sent = save.mock.calls[0]?.[0] as Settings;
    expect(sent.siteLogins).toEqual([
      { id: 'b2', urlPattern: '*.example.org', username: 'bob', hasPassword: false },
      { id: '', urlPattern: 'https://private.example.net/*', username: 'carol', hasPassword: false },
    ]);
    expect(setSecret).toHaveBeenCalledWith('site_login', 'b2', 'pw-bob');
    expect(setSecret).toHaveBeenCalledWith('site_login', 'new-1', 'pw-carol');
    expect(clearSecret).toHaveBeenCalledWith('site_login', 'a1');
    // The stored state (which passwords exist) is read again after the secrets changed.
    expect(load).toHaveBeenCalledTimes(2);
  });

  it('keeps the dialog open when a password cannot be stored', async () => {
    const { setSecret, onclose } = setupM6();
    setSecret.mockRejectedValueOnce(new Error('internal: the credential store is locked'));
    await loadedGeneral();
    await fireEvent.click(tab('Network'));
    await fireEvent.input(screen.getByLabelText('Proxy password'), { target: { value: 'x' } });
    await fireEvent.click(saveButton());
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('the credential store is locked'));
    expect(screen.getByRole('alert').textContent).toMatch(/^Settings saved, but the proxy password could not be stored/);
    expect(onclose).not.toHaveBeenCalled();
    expect((screen.getByLabelText('Proxy password') as HTMLInputElement).value).toBe('');
  });

  it('opens the editor of a new site login for a server that asked (auth_required)', async () => {
    setupM6(m6, { addLogin: 'https://private.example.net/*' });
    await waitFor(() => expect((screen.getByLabelText('Address') as HTMLInputElement).value).toBe('https://private.example.net/*'));
    expect(tab('Network').getAttribute('aria-selected')).toBe('true');
    expect(document.activeElement).toBe(screen.getByLabelText('User name'));
  });

  it('saves the login still open in its editor with the settings (Save instead of Add login)', async () => {
    const { save, setSecret, onclose } = setupM6(m6, { addLogin: 'https://private.example.net/*' });
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('User name')));
    await fireEvent.input(screen.getByLabelText('User name'), { target: { value: 'carol' } });
    await fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'pw-carol' } });
    await fireEvent.click(saveButton());
    await waitFor(() => expect(onclose).toHaveBeenCalled());
    const sent = save.mock.calls[0]?.[0] as Settings;
    expect(sent.siteLogins).toEqual([...m6.siteLogins, { id: '', urlPattern: 'https://private.example.net/*', username: 'carol', hasPassword: false }]);
    expect(setSecret).toHaveBeenCalledWith('site_login', 'new-1', 'pw-carol');
  });

  it('does not save while the login editor holds an incomplete login', async () => {
    const { save, onclose } = setupM6(m6, { addLogin: 'https://private.example.net/*' });
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('User name')));
    await fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'pw' } });
    await fireEvent.click(tab('General'));
    await fireEvent.click(saveButton());
    await waitFor(() => expect(tab('Network').getAttribute('aria-selected')).toBe('true'));
    expect(screen.getByText('Enter the user name')).toBeTruthy();
    expect(document.activeElement).toBe(screen.getByLabelText('User name'));
    expect(screen.getAllByRole('alert').some((a) => /site login you are editing/.test(a.textContent ?? ''))).toBe(true);
    expect(tab('Network').textContent).toContain('!');
    expect(save).not.toHaveBeenCalled();
    expect(onclose).not.toHaveBeenCalled();
    // Cancelling the editor lets the settings be saved without it.
    await fireEvent.click(within(screen.getByRole('group', { name: 'New site login' })).getByRole('button', { name: 'Cancel' }));
    await fireEvent.click(saveButton());
    await waitFor(() => expect(save).toHaveBeenCalledWith(m6));
  });

  it('keeps the login editor across pages, and opens the one for auth_required only once', async () => {
    const { save } = setupM6(m6, { addLogin: 'https://private.example.net/*' });
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('User name')));
    await fireEvent.input(screen.getByLabelText('User name'), { target: { value: 'dave' } });
    await fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'pw-dave' } });
    await fireEvent.click(tab('General'));
    await fireEvent.click(tab('Network'));
    expect((screen.getByLabelText('Address') as HTMLInputElement).value).toBe('https://private.example.net/*');
    expect((screen.getByLabelText('User name') as HTMLInputElement).value).toBe('dave');
    expect((screen.getByLabelText('Password') as HTMLInputElement).value).toBe('pw-dave');
    // Once it is closed, coming back to the page does not open it again.
    await fireEvent.click(within(screen.getByRole('group', { name: 'New site login' })).getByRole('button', { name: 'Cancel' }));
    await fireEvent.click(tab('General'));
    await fireEvent.click(tab('Network'));
    expect(screen.queryByRole('group', { name: 'New site login' })).toBeNull();
    expect(screen.getByRole('button', { name: 'Add site login' })).toBeTruthy();

    // An editor opened and left untouched is not a change.
    await fireEvent.click(screen.getByRole('button', { name: 'Add site login' }));
    await fireEvent.click(saveButton());
    await waitFor(() => expect(save).toHaveBeenCalledWith(m6));
  });

  it('edits host rules', async () => {
    const { save } = setupM6();
    await loadedGeneral();
    await fireEvent.click(tab('Network'));
    await fireEvent.click(screen.getByRole('button', { name: 'Add host rule' }));
    const host = screen.getByLabelText('Host of rule 2');
    expect(document.activeElement).toBe(host);
    await fireEvent.input(host, { target: { value: 'Mirror.Example.net' } });
    await fireEvent.input(screen.getByLabelText('Connections of rule 2'), { target: { value: '2' } });
    await fireEvent.input(screen.getByLabelText('Speed limit of rule 2'), { target: { value: '300' } });
    await chooseOption(screen.getByLabelText('Speed limit unit of rule 2'), 'KB/s');
    // The longest request (site media) in MB: 1.05–1073 (1 MiB to 1 GiB), empty = no cap; the
    // loaded 10 MiB shows as 10.486 MB and stays exact, as does rule 1's 1 MiB/s limit (1.05 MB/s).
    expect((screen.getByLabelText('Max request of rule 1 (MB)') as HTMLInputElement).value).toBe('10.486');
    expect((screen.getByLabelText('Speed limit of rule 1') as HTMLInputElement).value).toBe('1.05');
    const request = screen.getByLabelText('Max request of rule 2 (MB)');
    expect((request as HTMLInputElement).placeholder).toBe('No cap');
    await fireEvent.input(request, { target: { value: '2048' } });
    await fireEvent.click(saveButton());
    await waitFor(() => expect(request.getAttribute('aria-invalid')).toBe('true'));
    expect(screen.getByText('Enter 1.05 to 1073 MB, or leave empty for no cap')).toBeTruthy();
    expect(tab('Network').textContent).toContain('needs attention');
    expect(save).not.toHaveBeenCalled();
    await fireEvent.input(request, { target: { value: '1.5' } });
    await fireEvent.click(saveButton());
    await waitFor(() => expect(save).toHaveBeenCalled());
    expect((save.mock.calls[0]?.[0] as Settings).hostRules).toEqual([
      { pattern: '*.example.com', maxConnections: 4, speedLimit: 1024 * 1024, maxRequestBytes: 10 * 1024 * 1024 },
      { pattern: 'mirror.example.net', maxConnections: 2, speedLimit: 300 * 1000, maxRequestBytes: 1.5 * 1000 * 1000 },
    ]);
  });

  it('sets up the clipboard, file marking and the scanner (Integration)', async () => {
    const { save, pickFile } = setupM6({ ...m6, avScanEnabled: false, avScanCommand: '', clipboardMonitor: false });
    await loadedGeneral();
    await fireEvent.click(tab('Integration'));
    await fireEvent.click(toggle(/links that I copy/));
    await fireEvent.click(toggle(/coming from the internet/));
    await fireEvent.click(toggle(/virus scanner/));
    await fireEvent.click(saveButton());
    await waitFor(() => expect(screen.getByText('Choose the scanner program')).toBeTruthy());
    expect(save).not.toHaveBeenCalled();
    await fireEvent.click(screen.getByRole('button', { name: /Browse/ }));
    await waitFor(() => expect((screen.getByLabelText('Scanner program') as HTMLInputElement).value).toBe('/opt/scanner/scan'));
    expect(pickFile).toHaveBeenCalledWith({ title: 'Choose the virus scanner', initialDir: '' });
    await fireEvent.click(saveButton());
    await waitFor(() => expect(save).toHaveBeenCalled());
    expect(save.mock.calls[0]?.[0]).toMatchObject({
      clipboardMonitor: true,
      markDownloadedFiles: true,
      avScanEnabled: true,
      avScanCommand: '/opt/scanner/scan',
      avScanArgs: '--no-summary [File]',
    });
  });

  it('checks for updates with the saved address and installs after asking', async () => {
    const info = { currentVersion: '0.3.3', latestVersion: '0.4.0', available: true, notes: 'Queues', publishedAt: 0, checkedAt: 1 };
    const checkForUpdate = vi.fn(async () => info);
    const installUpdate = vi.fn(async () => info);
    setupM6(m6, { checkForUpdate, installUpdate });
    await loadedGeneral();
    await fireEvent.click(tab('Updates'));
    // The address is under Advanced, closed with a summary of it.
    await fireEvent.click(screen.getByRole('button', { name: /^Advanced/ }));
    const url = screen.getByLabelText('Update address') as HTMLInputElement;
    expect(url.value).toBe('https://updates.example.com/downex.json');
    // An edited address must be saved before checking.
    await fireEvent.input(url, { target: { value: 'https://other.example.com/feed.json' } });
    expect((screen.getByRole('button', { name: 'Check now' }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/Save the settings first/)).toBeTruthy();
    await fireEvent.input(url, { target: { value: 'https://updates.example.com/downex.json' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Check now' }));
    await waitFor(() => expect(screen.getByText(/is available \(you have/)).toBeTruthy());
    expect(screen.getByText('Queues')).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Install update…' }));
    expect(installUpdate).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Install and restart' }));
    await fireEvent.click(screen.getByRole('button', { name: 'Install and restart' }));
    await waitFor(() => expect(installUpdate).toHaveBeenCalled());
    expect(screen.getByText(/is installing version/)).toBeTruthy();
  });

  it('accepts only an https update address', async () => {
    const { save } = setupM6();
    await loadedGeneral();
    await fireEvent.click(tab('Updates'));
    await fireEvent.click(screen.getByRole('button', { name: /^Advanced/ }));
    await fireEvent.input(screen.getByLabelText('Update address'), { target: { value: 'http://updates.example.com/feed.json' } });
    await fireEvent.click(tab('General'));
    await fireEvent.click(saveButton());
    await waitFor(() => expect(tab('Updates').getAttribute('aria-selected')).toBe('true'));
    // Advanced opens by itself to show the address that is wrong, and the keyboard is on it.
    expect(screen.getByRole('button', { name: /^Advanced/ }).getAttribute('aria-expanded')).toBe('true');
    expect(document.activeElement).toBe(screen.getByLabelText('Update address'));
    expect(screen.getByText(/must start with https:\/\//)).toBeTruthy();
    expect(tab('Updates').textContent).toContain('needs attention');
    expect(save).not.toHaveBeenCalled();
  });

  it('moves between pages with the arrow keys', async () => {
    setupM6();
    await loadedGeneral();
    tab('General').focus();
    await fireEvent.keyDown(tab('General'), { key: 'ArrowRight' });
    expect(document.activeElement).toBe(tab('Downloads'));
    await fireEvent.keyDown(tab('Downloads'), { key: 'End' });
    expect(document.activeElement).toBe(tab('About'));
    await fireEvent.keyDown(tab('About'), { key: 'ArrowRight' });
    expect(document.activeElement).toBe(tab('General'));
  });
});
describe('SettingsDialog: Video sites (SITE-MEDIA-SPEC §5.4)', () => {
  function toolsOf(status: ToolsStatus | null, fail = false) {
    const api = {
      getTools: vi.fn(async () => {
        if (!status) throw new Error('unimplemented: downex.v1.DaemonService.GetTools is not implemented');
        return status;
      }),
      installTools: vi.fn(async (_update: boolean) => {
        if (fail) throw new Error('failed_precondition: tools_unavailable: yt-dlp and Deno are not set up: deno.exe is not signed by its publisher');
        return makeTools();
      }),
      onEvents: vi.fn(() => () => {}),
    };
    return { api, store: new ToolsStore(api) };
  }

  async function openVideo(initial: Settings, tools?: ToolsStore) {
    const openExternal = vi.fn(async () => {});
    const ui = setupM6(initial, { tools, openExternal, initialPage: 'video' });
    await waitFor(() => expect(toggle(/Download videos from web pages/).disabled).toBe(false));
    return { ...ui, openExternal };
  }

  const missing = makeTools({
    ready: false,
    tools: makeTools().tools.map((t) => ({ ...t, state: 'missing' as const, version: '', path: '', verified: '', checkedAt: 0, latestVersion: '' })),
  });

  it('shows the switches, the channel and each tool, and saves them', async () => {
    const { store } = toolsOf(makeTools({ tools: [makeTools().tools[0]!, { ...makeTools().tools[1]!, managed: false, path: '/usr/bin/deno', verified: '' }] }));
    const { save } = await openVideo({ ...m6, siteMedia: true, toolsAutoUpdate: true, toolsChannel: 'nightly' }, store);
    expect(tab('Video sites').getAttribute('aria-selected')).toBe('true');
    expect(toggle(/Download videos from web pages with yt-dlp/).checked).toBe(true);
    expect(toggle(/Update automatically/).checked).toBe(true);
    const channel = screen.getByLabelText('yt-dlp releases');
    expect(channel.getAttribute('role')).toBe('combobox');
    expect(optionLabels(channel)).toEqual(['Nightly (recommended by yt-dlp)', 'Stable']);
    await waitFor(() => expect(screen.getAllByRole('listitem').length).toBeGreaterThanOrEqual(2));
    const [yt, deno] = screen.getAllByRole('listitem').filter((li) => li.classList.contains('tool'));
    expect(yt!.textContent).toContain('yt-dlp');
    expect(yt!.textContent).toContain('Ready');
    expect(yt!.textContent).toContain('version 2026.10.10.233141');
    expect(yt!.textContent).toContain('Downex copy');
    expect(yt!.textContent).toContain('Signature (OpenPGP) and checksum checked');
    expect(deno!.textContent).toContain('Deno');
    expect(deno!.textContent).toContain('Found on PATH: /usr/bin/deno');
    expect(deno!.textContent).toContain('Your own copy: Downex does not check it');
    expect(screen.getByRole('button', { name: 'Check for updates' })).toBeTruthy();
    expect(screen.getByText(/GPLv3\+\) is downloaded from github.com\/yt-dlp; Deno \(MIT\) from github.com\/denoland\/deno/)).toBeTruthy();
    expect(screen.getByText(/terms allow downloading its videos is your responsibility/)).toBeTruthy();

    await chooseOption(channel, 'stable');
    await fireEvent.click(toggle(/Update automatically/));
    await fireEvent.click(saveButton());
    await waitFor(() => expect(save).toHaveBeenCalled());
    expect(save).toHaveBeenCalledWith({ ...m6, siteMedia: true, toolsAutoUpdate: false, toolsChannel: 'stable' });
  });

  it('installs the tools, showing the progress of the tools events', async () => {
    const { api, store } = toolsOf(missing);
    await openVideo({ ...m6, siteMedia: true }, store);
    await waitFor(() => expect(screen.getAllByText('Not installed')).toHaveLength(2));
    let finish: (s: ToolsStatus) => void = () => {};
    api.installTools.mockImplementationOnce(() => new Promise<ToolsStatus>((r) => (finish = r)));
    await fireEvent.click(screen.getByRole('button', { name: 'Install now' }));
    expect(api.installTools).toHaveBeenCalledWith(false);
    expect(screen.getByRole('button', { name: 'Installing…' }).hasAttribute('disabled')).toBe(true);
    store.apply([
      { kind: 'tools', status: { ...missing, tools: missing.tools.map((t) => ({ ...t, state: 'installing' as const, bytesDone: 30, bytesTotal: 100 })) } },
    ]);
    await waitFor(() => expect(screen.getByText('Downloading yt-dlp and Deno: 30%')).toBeTruthy());
    expect(screen.getByRole('progressbar', { name: 'Downloading yt-dlp and Deno' }).getAttribute('aria-valuenow')).toBe('30');
    finish(makeTools());
    await waitFor(() => expect(screen.getByRole('button', { name: 'Check for updates' })).toBeTruthy());
    expect(screen.queryByRole('progressbar', { name: 'Downloading yt-dlp and Deno' })).toBeNull();
    await fireEvent.click(screen.getByRole('button', { name: 'Check for updates' }));
    expect(api.installTools).toHaveBeenLastCalledWith(true);
  });

  it('says why an install failed, and installs only with video pages on in the saved settings', async () => {
    const { store } = toolsOf(missing, true);
    await openVideo({ ...m6, siteMedia: true }, store);
    await waitFor(() => expect(screen.getByRole('button', { name: 'Install now' }).hasAttribute('disabled')).toBe(false));
    await fireEvent.click(screen.getByRole('button', { name: 'Install now' }));
    await waitFor(() =>
      expect(screen.getByRole('alert').textContent).toContain('Downex could not set up yt-dlp/Deno: deno.exe is not signed by its publisher.'),
    );
    await fireEvent.click(toggle(/Download videos from web pages/));
    expect(toggle(/Update automatically/).disabled).toBe(true);
    expect((screen.getByLabelText('yt-dlp releases') as HTMLButtonElement).disabled).toBe(true);
  });

  it('tells that the saved settings must have video pages on first', async () => {
    const { store } = toolsOf(missing);
    await openVideo({ ...m6, siteMedia: false }, store);
    expect(screen.getByRole('button', { name: 'Install now' }).hasAttribute('disabled')).toBe(true);
    expect(screen.getByText('Turn on video downloads from web pages first.')).toBeTruthy();
    await fireEvent.click(toggle(/Download videos from web pages/));
    expect(screen.getByText(/Save the settings first/)).toBeTruthy();
  });

  it('opens the licences in the browser, and says when downexd has no video pages', async () => {
    const { store } = toolsOf(null);
    const { openExternal } = await openVideo({ ...m6, siteMedia: true }, store);
    await waitFor(() => expect(screen.getByText(/Update Downex to download videos/)).toBeTruthy());
    expect(screen.queryByRole('button', { name: 'Install now' })).toBeNull();
    // where a link goes is its tooltip (the page's lead names the hosts)
    expect(screen.getByRole('button', { name: 'Deno licence (MIT)' }).title).toBe('https://github.com/denoland/deno/blob/main/LICENSE.md');
    await fireEvent.click(screen.getByRole('button', { name: 'Deno licence (MIT)' }));
    expect(openExternal).toHaveBeenCalledWith('https://github.com/denoland/deno/blob/main/LICENSE.md');
    await fireEvent.click(screen.getByRole('button', { name: 'yt-dlp licence (Unlicense)' }));
    expect(openExternal).toHaveBeenLastCalledWith('https://github.com/yt-dlp/yt-dlp/blob/master/LICENSE');
  });
});

describe('SettingsDialog: the window (DESIGN-SYSTEM.md §3.20, §4.9)', () => {
  afterEach(() => {
    theme.set('system');
    density.set('comfortable');
  });

  it('lists eight pages in a vertical tablist that the arrows, Home and End move through', async () => {
    setup();
    await loadedGeneral();
    const list = screen.getByRole('tablist', { name: 'Settings pages' });
    expect(list.getAttribute('aria-orientation')).toBe('vertical');
    const names = within(list).getAllByRole('tab').map((t) => t.textContent?.trim());
    expect(names).toEqual(['General', 'Downloads', 'Network', 'Integration', 'Video sites', 'Appearance', 'Updates', 'About']);
    // The chosen page has the keyboard once the settings are loaded; the arrows reach the others.
    await waitFor(() => expect(document.activeElement).toBe(tab('General')));
    expect(within(list).getAllByRole('tab').filter((t) => t.tabIndex === 0)).toEqual([tab('General')]);
    await fireEvent.keyDown(tab('General'), { key: 'ArrowDown' });
    expect(document.activeElement).toBe(tab('Downloads'));
    expect(tab('Downloads').getAttribute('aria-selected')).toBe('true');
    expect(screen.getByRole('tabpanel').getAttribute('aria-labelledby')).toBe(tab('Downloads').id);
    expect(screen.getByLabelText('Default folder')).toBeTruthy();
    await fireEvent.keyDown(tab('Downloads'), { key: 'ArrowUp' });
    expect(document.activeElement).toBe(tab('General'));
    await fireEvent.keyDown(tab('General'), { key: 'ArrowUp' });
    expect(document.activeElement).toBe(tab('About'));
    await fireEvent.keyDown(tab('About'), { key: 'Home' });
    expect(document.activeElement).toBe(tab('General'));
    await fireEvent.keyDown(tab('General'), { key: 'End' });
    expect(tab('About').getAttribute('aria-selected')).toBe('true');
    // Other keys are left alone.
    await fireEvent.keyDown(tab('About'), { key: 'a' });
    expect(tab('About').getAttribute('aria-selected')).toBe('true');
  });

  it('shows an error on the page of its field, marks that page and saves once it is fixed', async () => {
    const { save } = setup(loaded, { initialPage: 'downloads' });
    await loadedDownloads();
    await fireEvent.input(screen.getByLabelText('Global speed limit'), { target: { value: 'fast' } });
    await fireEvent.click(tab('Appearance'));
    await fireEvent.click(saveButton());
    await waitFor(() => expect(tab('Downloads').getAttribute('aria-selected')).toBe('true'));
    expect(document.activeElement).toBe(screen.getByLabelText('Global speed limit'));
    expect(tab('Downloads').textContent).toContain('needs attention');
    expect(tab('General').textContent).not.toContain('needs attention');
    expect(screen.getByRole('alert').textContent).toBe('Some settings need attention.');
    expect(save).not.toHaveBeenCalled();
    // Fixed, it saves the limit in bytes per second.
    await fireEvent.input(screen.getByLabelText('Global speed limit'), { target: { value: '512' } });
    await chooseOption(screen.getByLabelText('Global speed limit unit'), 'KB/s');
    await fireEvent.click(saveButton());
    await waitFor(() => expect(save).toHaveBeenCalledWith({ ...loaded, globalSpeedLimit: 512 * 1000 }));
  });

  it('opens on Downloads with the global speed limit focused (the Custom… limit of the status bar)', async () => {
    setup(loaded, { focusField: 'globalSpeedLimit' });
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Global speed limit')));
    expect(tab('Downloads').getAttribute('aria-selected')).toBe('true');
    expect((screen.getByLabelText('Global speed limit') as HTMLInputElement).value).toBe('2');
    expect(screen.getByLabelText('Global speed limit unit').getAttribute('data-value')).toBe('MB/s');
  });

  it('opens a page asked for by name: Integration explains the extension and its relay', async () => {
    setup(loaded, { initialPage: 'integration' });
    await waitFor(() => expect(toggle(/Ask before saving/).disabled).toBe(false));
    expect(tab('Integration').getAttribute('aria-selected')).toBe('true');
    expect(screen.getByText('The Downex browser extension')).toBeTruthy();
    expect(screen.getByText('downex-nmhost register')).toBeTruthy();
  });

  it('applies the theme and the density at once, without Save, and keeps them on this computer', async () => {
    const { save, onclose } = setup(loaded, { initialPage: 'appearance' });
    await waitFor(() => expect(saveButton().hasAttribute('disabled')).toBe(false));
    expect(screen.getByText('Saved on this computer right away.')).toBeTruthy();
    const themes = screen.getByRole('group', { name: 'Theme' });
    expect(within(themes).getAllByRole('radio').map((r) => (r as HTMLInputElement).checked)).toEqual([true, false, false]);

    await fireEvent.click(within(themes).getByRole('radio', { name: 'Dark' }));
    expect(document.documentElement.dataset.theme).toBe('dark');
    expect(localStorage.getItem('downex.theme')).toBe('dark');
    expect(theme.choice).toBe('dark');

    const densities = screen.getByRole('group', { name: 'List density' });
    expect((within(densities).getByRole('radio', { name: 'Comfortable' }) as HTMLInputElement).checked).toBe(true);
    expect(screen.getByText(/Two lines per download/)).toBeTruthy();
    await fireEvent.click(within(densities).getByRole('radio', { name: 'Compact' }));
    expect(document.documentElement.dataset.density).toBe('compact');
    expect(localStorage.getItem('downex.density')).toBe('compact');
    expect(screen.getByText(/One line per download/)).toBeTruthy();

    // Cancel keeps them: they were never part of the settings.
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(onclose).toHaveBeenCalled();
    expect(save).not.toHaveBeenCalled();
    expect(density.choice).toBe('compact');
    expect(theme.choice).toBe('dark');
  });

  it('changes the stores it is given, and shows their choice', async () => {
    const themeStore = { choice: 'light' as const, set: vi.fn() };
    const densityStore = { choice: 'compact' as const, set: vi.fn() };
    setup(loaded, { initialPage: 'appearance', appearance: { theme: themeStore, density: densityStore } });
    const themes = await screen.findByRole('group', { name: 'Theme' });
    expect((within(themes).getByRole('radio', { name: 'Light' }) as HTMLInputElement).checked).toBe(true);
    expect((screen.getByRole('radio', { name: 'Compact' }) as HTMLInputElement).checked).toBe(true);
    await fireEvent.click(within(themes).getByRole('radio', { name: 'System' }));
    expect(themeStore.set).toHaveBeenCalledWith('system');
    await fireEvent.click(screen.getByRole('radio', { name: 'Comfortable' }));
    expect(densityStore.set).toHaveBeenCalledWith('comfortable');
  });

  it('keeps the update address under Advanced, closed with a summary of what it hides', async () => {
    setupM6({ ...m6, updateFeedUrl: '' }, { initialPage: 'updates' });
    const advanced = await screen.findByRole('button', { name: /^Advanced/ });
    expect(advanced.getAttribute('aria-expanded')).toBe('false');
    expect(advanced.textContent).toContain('No update address: updates are off');
    expect(screen.queryByLabelText('Update address')).toBeNull();
    // Without an address, checking automatically cannot be on, and Check now says where to set one.
    await waitFor(() => expect(screen.getByText('Set an update address under Advanced first.')).toBeTruthy());
    expect(toggle(/Check for updates automatically/).disabled).toBe(true);

    await fireEvent.click(advanced);
    expect(advanced.getAttribute('aria-expanded')).toBe('true');
    const panel = document.getElementById(advanced.getAttribute('aria-controls') ?? '');
    expect(panel?.hidden).toBe(false);
    await fireEvent.input(screen.getByLabelText('Update address'), { target: { value: 'https://updates.example.com/feed.json' } });
    expect(toggle(/Check for updates automatically/).disabled).toBe(false);
    await fireEvent.click(advanced);
    expect(advanced.getAttribute('aria-expanded')).toBe('false');
    expect(advanced.textContent).toContain('Update address: updates.example.com');
  });

  it('closes only the list of an open select on Escape, not the window', async () => {
    const { onclose } = setup(loaded, { initialPage: 'downloads' });
    await loadedDownloads();
    const unit = screen.getByLabelText('Global speed limit unit');
    unit.focus();
    await fireEvent.keyDown(unit, { key: 'ArrowDown' });
    await waitFor(() => expect(unit.getAttribute('aria-expanded')).toBe('true'));
    await fireEvent.keyDown(unit, { key: 'Escape' });
    await waitFor(() => expect(unit.getAttribute('aria-expanded')).toBe('false'));
    expect(onclose).not.toHaveBeenCalled();
    expect(screen.getByRole('dialog', { name: 'Settings' })).toBeTruthy();
    expect(unit.getAttribute('data-value')).toBe('MB/s');
    // Closed, Escape closes the window as before.
    await fireEvent.keyDown(unit, { key: 'Escape' });
    await waitFor(() => expect(onclose).toHaveBeenCalled());
  });

  it('chooses the proxy mode with a segmented control, showing only the fields of that mode', async () => {
    const { save } = setupM6({ ...m6, proxy: { ...m6.proxy, mode: 'unspecified' } }, { initialPage: 'network' });
    const modes = await screen.findByRole('group', { name: 'How downloads connect' });
    await waitFor(() => expect(screen.getByRole('button', { name: 'Add host rule' }).hasAttribute('disabled')).toBe(false));
    // 'unspecified' is shown as (and means) the system's settings.
    expect(within(modes).getAllByRole('radio').map((r) => [(r as HTMLInputElement).value, (r as HTMLInputElement).checked])).toEqual([
      ['system', true],
      ['none', false],
      ['manual', false],
      ['pac', false],
    ]);
    expect(screen.queryByLabelText('HTTP proxy')).toBeNull();
    await fireEvent.click(within(modes).getByRole('radio', { name: 'PAC script' }));
    expect(screen.getByLabelText('Script address')).toBeTruthy();
    await fireEvent.click(within(modes).getByRole('radio', { name: 'No proxy' }));
    expect(screen.queryByLabelText('Proxy user name')).toBeNull();
    await fireEvent.click(within(modes).getByRole('radio', { name: 'Manual' }));
    expect((screen.getByLabelText('HTTP proxy') as HTMLInputElement).value).toBe('proxy.lan:8080');
    await fireEvent.click(saveButton());
    await waitFor(() => expect(save).toHaveBeenCalled());
    expect((save.mock.calls[0]?.[0] as Settings).proxy.mode).toBe('manual');
  });
});

describe('SettingsDialog: sizing (DESIGN-SYSTEM.md §3.9, §3.20)', () => {
  it('is sized in rem (it grows with the text) up to the room the window has, without a fixed pixel height', async () => {
    setup();
    await loadedGeneral();
    const dialog = screen.getByRole('dialog', { name: 'Settings' });
    // 70 × 46 rem (1120 × 736 at 16 px): a wide pane puts a long page in two columns, so that
    // the pages fit the default 1200 × 760 window without scrolling (§3.20)
    expect(dialog.style.inlineSize).toMatch(/^min\(70rem, .*100vw/);
    expect(dialog.style.minBlockSize).toMatch(/^min\(46rem, .*100vh/);
    expect(dialog.style.minBlockSize).toContain('--modal-top');
    expect(dialog.style.blockSize).toBe('');
  });
});
