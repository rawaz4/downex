<script lang="ts">
  // Settings → Video sites (SITE-MEDIA-SPEC §5.4, DESIGN-SYSTEM.md §4.9): whether videos of web
  // pages are downloaded with yt-dlp, its updates and release channel, the state of yt-dlp and
  // Deno with their install, and the licences. The layout classes (s-*) are the dialog's.
  import { onMount } from 'svelte';
  import type { ToolInfo, ToolsChannel } from '../api';
  import { formatDateTime } from '../format';
  import { escapeBidi } from '../text';
  import { installFraction, installText, toolName, type ToolsStore } from '../stores/tools.svelte';
  import type { VideoForm } from '../validate';
  import Icon from './Icon.svelte';
  import ProgressBar from './ProgressBar.svelte';
  import Select from './Select.svelte';
  import Switch from './Switch.svelte';

  type Form = { -readonly [K in keyof VideoForm]: VideoForm[K] };

  interface Props {
    form: Form;
    /** Video pages are on in the saved settings: installing uses the saved settings. */
    savedSiteMedia: boolean;
    /** The tools' state and their install (without it, the page shows the settings only). */
    tools?: Pick<ToolsStore, 'status' | 'supported' | 'loadError' | 'busy' | 'installError' | 'installing' | 'load' | 'install'>;
    /** Opens a licence in the system's browser. */
    openExternal?: (url: string) => Promise<void>;
    disabled?: boolean;
  }

  let { form = $bindable(), savedSiteMedia, tools, openExternal, disabled = false }: Props = $props();

  const uid = $props.id();

  const CHANNELS: readonly { value: ToolsChannel; label: string }[] = [
    { value: 'nightly', label: 'Nightly (recommended by yt-dlp)' },
    { value: 'stable', label: 'Stable' },
  ];

  /** The licences of §4.8 (fixed addresses, opened in the system's browser). */
  const LICENCES: readonly { label: string; url: string }[] = [
    { label: 'yt-dlp licence (Unlicense)', url: 'https://github.com/yt-dlp/yt-dlp/blob/master/LICENSE' },
    {
      label: 'Licences of the yt-dlp executables (GPLv3+)',
      url: 'https://github.com/yt-dlp/yt-dlp/blob/master/THIRD_PARTY_LICENSES.txt',
    },
    { label: 'Deno licence (MIT)', url: 'https://github.com/denoland/deno/blob/main/LICENSE.md' },
  ];

  const status = $derived(tools?.status ?? null);
  const ready = $derived(status?.ready ?? false);
  /** Install or update: the saved settings must have video pages on. */
  const canInstall = $derived(tools !== undefined && tools.supported && tools.busy === null && savedSiteMedia && !disabled);
  const fraction = $derived(installFraction(status));

  onMount(() => {
    void tools?.load();
  });

  function stateText(t: ToolInfo): string {
    switch (t.state) {
      case 'ready':
        return 'Ready';
      case 'installing':
        return t.version ? 'Updating…' : 'Downloading…';
      case 'missing':
        return 'Not installed';
      case 'failed':
        return t.version ? 'Update failed' : 'Could not be set up';
      case 'disabled':
        return form.siteMedia ? 'Not available on this system' : 'Off';
      case 'unspecified':
        return 'Unknown';
    }
  }

  /** The pill tone of a tool's state. */
  function stateTone(t: ToolInfo): 'ok' | 'info' | 'fail' | 'done' {
    switch (t.state) {
      case 'ready':
        return 'ok';
      case 'installing':
        return 'info';
      case 'failed':
        return 'fail';
      default:
        return 'done';
    }
  }

  /** What Downex checked of the copy in use (ToolInfo.verified). */
  function verifiedText(t: ToolInfo): string {
    if (!t.managed) return t.path ? 'Your own copy: Downex does not check it' : '';
    switch (t.verified) {
      case 'openpgp+sha256':
        return 'Signature (OpenPGP) and checksum checked';
      case 'sha256+authenticode':
        return 'Checksum and publisher signature checked';
      case 'sha256':
        return 'Checksum checked';
      default:
        return t.verified ? escapeBidi(t.verified) : '';
    }
  }

  function copyText(t: ToolInfo): string {
    if (!t.path) return '';
    return t.managed ? 'Downex copy' : `Found on PATH: ${escapeBidi(t.path)}`;
  }

  async function install(update: boolean) {
    if (!tools || !canInstall) return;
    await tools.install(update);
  }

  async function open(url: string) {
    try {
      await openExternal?.(url);
    } catch {
      // nothing to do: the address is shown next to the link
    }
  }
</script>

<section class="s-section" aria-labelledby="{uid}-video">
  <div class="s-head">
    <h3 class="s-title" id="{uid}-video">Video sites</h3>
    <p class="s-lead" id="{uid}-video-hint">
      Downex saves the video of a web page (YouTube and the other sites yt-dlp supports) as one MP4 file, in the quality
      you choose. yt-dlp reads the page, with Deno for YouTube; Downex downloads both the first time you use it.
    </p>
  </div>
  <div class="s-card">
    <div class="s-row s-switch">
      <Switch bind:checked={form.siteMedia} label="Download videos from web pages with yt-dlp" {disabled} />
    </div>
    <div class="s-row s-switch">
      <Switch
        bind:checked={form.toolsAutoUpdate}
        label="Update automatically"
        hint="Checks once a day; only Downex’s own copies."
        disabled={disabled || !form.siteMedia}
      />
    </div>
    <!-- the label and the select on one line, the hint under both at the card's width (beside
         the select it ran to six lines in a column) -->
    <div class="s-row channel-row">
      <label class="s-label" class:off={disabled || !form.siteMedia} for="{uid}-channel">yt-dlp releases</label>
      <p class="s-hint channel-hint" id="{uid}-channel-hint">
        Nightly releases follow site changes within a day. Saving another channel installs its latest release.
      </p>
      <div class="s-control channel">
        <Select
          id="{uid}-channel"
          bind:value={form.toolsChannel}
          options={CHANNELS}
          size="sm"
          disabled={disabled || !form.siteMedia}
          ariaDescribedby="{uid}-channel-hint"
        />
      </div>
    </div>
  </div>
</section>

<section class="s-section" aria-labelledby="{uid}-tools">
  <div class="s-head">
    <h3 class="s-title" id="{uid}-tools">yt-dlp and Deno</h3>
  </div>
  {#if !tools}
    <p class="s-lead">Their state is shown when Downex is connected to downexd.</p>
  {:else if !tools.supported}
    <p class="note info">This downexd cannot download videos from web pages. Update Downex to download videos.</p>
  {:else}
    {#if status && status.tools.length > 0}
      <ul class="tools">
        {#each status.tools as t (t.name)}
          <li class="tool">
            <div class="tool-head">
              <span class="tool-name">{toolName(t.name)}</span>
              {#if t.version}
                <span class="version">version <bdi>{escapeBidi(t.version)}</bdi></span>
              {/if}
              <span class="pill {stateTone(t)}">{stateText(t)}</span>
            </div>
            <dl class="facts">
              {#if copyText(t)}
                <dt>Copy</dt>
                <dd class="mono" dir="auto">{copyText(t)}</dd>
              {/if}
              {#if verifiedText(t)}
                <dt>Checked</dt>
                <dd>{verifiedText(t)}</dd>
              {/if}
              <dt>Last check</dt>
              <dd>
                {t.checkedAt > 0 ? formatDateTime(t.checkedAt) : 'Never'}{t.latestVersion && t.latestVersion !== t.version
                  ? ` · latest ${escapeBidi(t.latestVersion)}`
                  : ''}
              </dd>
              {#if t.error}
                <dt>Problem</dt>
                <dd class="problem"><bdi>{escapeBidi(t.error)}</bdi></dd>
              {/if}
            </dl>
          </li>
        {/each}
      </ul>
    {:else if tools.loadError}
      <p class="note fail" role="alert">Could not read their state: <bdi>{escapeBidi(tools.loadError)}</bdi></p>
    {:else}
      <p class="s-lead" role="status"><span class="spinner" aria-hidden="true"></span>Reading their state…</p>
    {/if}

    {#if tools.installing}
      <div class="progress" aria-live="polite">
        <ProgressBar {fraction} state="downloading" label="Downloading yt-dlp and Deno" />
        <p class="hint">{installText(status)}</p>
      </div>
    {/if}

    <div class="s-after">
      {#if ready}
        <button type="button" class="btn" onclick={() => void install(true)} disabled={!canInstall} aria-describedby="{uid}-install-hint">
          <Icon name="refresh" size={16} />
          {tools.busy === 'update' ? 'Checking for updates…' : 'Check for updates'}
        </button>
      {:else}
        <button type="button" class="btn primary" onclick={() => void install(false)} disabled={!canInstall} aria-describedby="{uid}-install-hint">
          <Icon name="arrow-down" size={16} />
          {tools.busy === 'install' ? 'Installing…' : 'Install now'}
        </button>
      {/if}
      <p class="s-hint" id="{uid}-install-hint">
        {#if !savedSiteMedia}
          {form.siteMedia ? 'Save the settings first: video pages are off in the saved settings.' : 'Turn on video downloads from web pages first.'}
        {:else if tools.busy !== null}
          This can take a few minutes; the window can be closed meanwhile.
        {:else}
          Only Downex’s own copies are installed or updated, never ones found on PATH.
        {/if}
      </p>
    </div>
    {#if tools.installError}
      <p class="note fail" role="alert"><bdi>{escapeBidi(tools.installError)}</bdi></p>
    {/if}
  {/if}
</section>

<section class="s-section" aria-labelledby="{uid}-licences">
  <div class="s-head">
    <h3 class="s-title" id="{uid}-licences">Licences</h3>
    <p class="s-lead">
      yt-dlp (Unlicense; the Windows/macOS/Linux executables are GPLv3+) is downloaded from github.com/yt-dlp; Deno (MIT)
      from github.com/denoland/deno. Downex does not include either.
    </p>
  </div>
  <ul class="links">
    {#each LICENCES as l (l.url)}
      <li>
        <!-- a link names where it goes in its tooltip (the hosts are in the lead); without a
             browser to open it, the address itself is shown, to be copied -->
        {#if openExternal}
          <button type="button" class="btn link" title={l.url} onclick={() => void open(l.url)}>{l.label}<Icon name="external" size={12} /></button>
        {:else}
          <span class="link-label">{l.label}</span>
          <span class="mono url">{l.url}</span>
        {/if}
      </li>
    {/each}
  </ul>
  <p class="s-lead">Whether a site’s terms allow downloading its videos is your responsibility.</p>
</section>

<style>
  .channel-row {
    grid-template-areas:
      'label control'
      'hint hint';
    row-gap: 4px;
  }
  .channel-row > .s-label {
    grid-area: label;
  }
  .channel-row > .channel-hint {
    grid-area: hint;
  }
  .channel {
    grid-area: control;
    inline-size: 16.5rem;
  }
  .s-label.off {
    color: var(--faint);
  }
  .tools {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(15rem, 1fr));
    gap: 10px;
  }
  .tool {
    display: grid;
    align-content: start;
    gap: 8px;
    padding: 12px 14px;
    border: 1px solid var(--line);
    border-radius: var(--r-lg);
    background: var(--surface);
  }
  .tool-head {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 6px 8px;
  }
  .tool-name {
    font: 650 var(--type-lead) / 1.3 var(--font-display);
    color: var(--ink);
  }
  .version {
    color: var(--muted);
    font-size: var(--type-small);
  }
  .tool-head .pill {
    margin-inline-start: auto;
  }
  .facts {
    display: grid;
    grid-template-columns: max-content minmax(0, 1fr);
    gap: 3px 12px;
    margin: 0;
    font-size: var(--type-small);
  }
  dt {
    color: var(--faint);
  }
  dd {
    margin: 0;
    min-inline-size: 0;
    color: var(--ink-2);
  }
  .problem {
    color: var(--danger);
    overflow-wrap: anywhere;
  }
  .facts .mono {
    overflow-wrap: anywhere;
    user-select: text;
  }
  .progress {
    display: grid;
    gap: 6px;
  }
  .s-lead .spinner {
    margin-inline-end: 8px;
    vertical-align: -2px;
  }
  .links {
    display: grid;
    gap: 8px;
    margin: 0;
    padding: 0;
    list-style: none;
    font-size: var(--type-cell);
  }
  .links li {
    display: grid;
    gap: 1px;
  }
  .links .btn.link {
    justify-self: start;
    gap: 4px;
    font-size: var(--type-cell);
  }
  .link-label {
    color: var(--ink-2);
  }
  .links .url {
    color: var(--faint);
    font-size: 0.6875rem;
    overflow-wrap: anywhere;
  }
</style>
