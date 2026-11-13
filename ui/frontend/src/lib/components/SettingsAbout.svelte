<script lang="ts">
  // Settings → About (DESIGN-SYSTEM.md §4.9.1): what this copy of Downex is (the window, downexd,
  // the build), yt-dlp and Deno, whether updates can work, Downex's folders, the open-source
  // notices, and diagnostics to copy into a problem report. Nothing here changes a setting. The
  // folders are opened by the host from a fixed list (it never takes a path from the page); the
  // IPC endpoint is never shown or copied. The layout classes (s-*) are the dialog's.
  import { onMount, tick } from 'svelte';
  import { toApiError, webviewVersion, type AppFolder, type BuildInfo, type Status, type ToolInfo } from '../api';
  import { buildLines, introBlocks, osLabel, parseNotices, scrubDiagnostics, updatesState, versionsDiffer, type Notices } from '../about';
  import { dayTimeText } from '../display';
  import { escapeBidi } from '../text';
  import { toolName, type ToolsStore } from '../stores/tools.svelte';
  import BrandMark from './BrandMark.svelte';
  import Icon from './Icon.svelte';

  interface Props {
    /** The window's build (Startup); absent from an older host. */
    build?: BuildInfo;
    /** downexd's status: its version, folders and whether this build can update. */
    status?: () => Promise<Status>;
    /** yt-dlp and Deno (their state and the check for updates). */
    tools?: Pick<ToolsStore, 'status' | 'supported' | 'busy' | 'installError' | 'load' | 'install'>;
    /** The saved settings this page reads. */
    updateFeedUrl?: string;
    autoCheckUpdates?: boolean;
    defaultDir?: string;
    siteMedia?: boolean;
    openAppFolder?: (folder: AppFolder) => Promise<void>;
    copyText?: (text: string) => Promise<void>;
    diagnostics?: (opts: { readonly webview?: string }) => Promise<string>;
    thirdPartyNotices?: () => Promise<string>;
    /** The user agent the WebView2 version is read from (default: this page's). */
    userAgent?: string;
    /** Shows another page of the Settings window. */
    onpage?: (page: 'updates' | 'video') => void;
  }

  let {
    build,
    status,
    tools,
    updateFeedUrl = '',
    autoCheckUpdates = true,
    defaultDir = '',
    siteMedia = true,
    openAppFolder,
    copyText,
    diagnostics,
    thirdPartyNotices,
    userAgent = typeof navigator === 'undefined' ? '' : navigator.userAgent,
    onpage,
  }: Props = $props();

  const uid = $props.id();

  // ---- downexd ----
  /** null while asking; 'down' when it could not be asked. */
  let daemon = $state.raw<Status | 'down' | null>(null);
  const known = $derived(daemon !== null && daemon !== 'down' ? daemon : null);

  onMount(() => {
    void tools?.load();
    if (!status) {
      daemon = 'down';
      return;
    }
    status().then(
      (s) => (daemon = s),
      () => (daemon = 'down'),
    );
  });

  const webview = $derived(webviewVersion(userAgent));
  const version = $derived(build?.version || known?.version || '');
  const windowFacts = $derived(
    [build?.version ?? '', build ? osLabel(build) : '', webview ? `WebView2 ${webview}` : ''].filter((s) => s !== '').join(' · '),
  );
  const daemonFacts = $derived.by(() => {
    if (daemon === null) return 'Asking…';
    if (daemon === 'down') return 'Not reachable';
    const parts = [daemon.version];
    if (daemon.startedAt > 0) parts.push(`running since ${dayTimeText(daemon.startedAt)}`);
    if (daemon.pid > 0) parts.push(`process ${daemon.pid}`);
    return parts.join(' · ');
  });
  const built = $derived(build ? buildLines(build) : []);
  const mismatch = $derived(known !== null && versionsDiffer(build?.version ?? '', known.version));
  // while downexd is being asked, the saved address decides
  const updates = $derived(updatesState(daemon === null ? {} : known, updateFeedUrl, autoCheckUpdates));

  // ---- what the page says it did (one polite live region) ----
  let said = $state('');
  let saidTimer: ReturnType<typeof setTimeout> | undefined;
  function say(text: string): void {
    said = text;
    clearTimeout(saidTimer);
    saidTimer = setTimeout(() => (said = ''), 4000);
  }

  // ---- diagnostics ----
  let copying = $state(false);
  let copyError = $state<string | null>(null);

  async function copyDiagnostics(): Promise<void> {
    if (!diagnostics || !copyText || copying) return;
    copying = true;
    copyError = null;
    try {
      const text = await diagnostics(webview ? { webview } : {});
      await copyText(scrubDiagnostics(text, [known?.endpoint ?? '', updateFeedUrl]));
      say('Diagnostics copied. They name no addresses, files or logins.');
    } catch (err) {
      copyError = `Could not copy the diagnostics: ${toApiError(err).message}`;
    } finally {
      copying = false;
    }
  }

  // ---- video tools ----
  const toolList = $derived(tools?.status?.tools ?? []);
  const canCheckTools = $derived(tools !== undefined && tools.supported && tools.busy === null && siteMedia && toolList.length > 0);

  function toolState(t: ToolInfo): { tone: 'ok' | 'info' | 'fail' | 'done'; text: string } {
    switch (t.state) {
      case 'ready':
        return { tone: 'ok', text: 'Ready' };
      case 'installing':
        return { tone: 'info', text: t.version ? 'Updating…' : 'Downloading…' };
      case 'failed':
        return { tone: 'fail', text: t.version ? 'Update failed' : 'Not set up' };
      case 'missing':
        return { tone: 'done', text: 'Not installed' };
      case 'disabled':
        return { tone: 'done', text: 'Off' };
      default:
        return { tone: 'done', text: 'Unknown' };
    }
  }

  // ---- folders ----
  const folders = $derived.by(() => {
    const s = known;
    const rows: { kind: AppFolder; label: string; hint: string; path: string }[] = [
      { kind: 'data', label: 'Data', hint: 'The list of downloads and the settings', path: s?.dataDir ?? '' },
      { kind: 'logs', label: 'Logs', hint: 'What downexd did, for problem reports', path: s?.logDir ?? '' },
      { kind: 'tools', label: 'Tools', hint: 'Downex’s copies of yt-dlp and Deno', path: s?.toolsDir ?? '' },
      { kind: 'downloads', label: 'Downloads', hint: 'The default download folder', path: defaultDir },
    ];
    return rows;
  });
  let folderError = $state<string | null>(null);

  async function openFolder(kind: AppFolder, label: string): Promise<void> {
    if (!openAppFolder) return;
    folderError = null;
    try {
      await openAppFolder(kind);
    } catch (err) {
      const e = toApiError(err);
      folderError = e.code === 'failed_precondition' ? `The ${label} folder does not exist yet.` : `Could not open the ${label} folder: ${e.message}`;
    }
  }

  async function copyPath(path: string, label: string): Promise<void> {
    if (!copyText || path === '') return;
    try {
      await copyText(path);
      say(`The path of the ${label} folder was copied.`);
    } catch (err) {
      folderError = `Could not copy: ${toApiError(err).message}`;
    }
  }

  // ---- open-source notices (a view of their own in the pane) ----
  let view = $state<'about' | 'notices'>('about');
  let notices = $state.raw<Notices | null>(null);
  let noticesText = '';
  let noticesBusy = $state(false);
  let noticesError = $state<string | null>(null);
  let root: HTMLElement | undefined = $state();

  /** The pane scrolls: a new view starts at its top. */
  function toTop(): void {
    const pane = root?.closest<HTMLElement>('[role="tabpanel"]');
    if (pane) pane.scrollTop = 0;
  }

  async function showNotices(): Promise<void> {
    if (!thirdPartyNotices || noticesBusy) return;
    noticesError = null;
    if (!notices) {
      noticesBusy = true;
      try {
        noticesText = await thirdPartyNotices();
        notices = parseNotices(noticesText);
      } catch (err) {
        noticesError = `Could not read the notices: ${toApiError(err).message}`;
        return;
      } finally {
        noticesBusy = false;
      }
    }
    view = 'notices';
    await tick();
    toTop();
    document.getElementById(`${uid}-notices-title`)?.focus();
  }

  async function backToAbout(): Promise<void> {
    view = 'about';
    await tick();
    toTop();
    document.getElementById(`${uid}-view-notices`)?.focus();
  }

  async function copyNotices(): Promise<void> {
    if (!copyText || noticesText === '') return;
    try {
      await copyText(noticesText);
      say('The notices were copied.');
    } catch (err) {
      noticesError = `Could not copy: ${toApiError(err).message}`;
    }
  }
</script>

<div class="about" bind:this={root}>
  {#if view === 'notices' && notices}
    <section class="s-section" aria-labelledby="{uid}-notices-title">
      <div class="nhead">
        <button type="button" class="btn sm ghost back" aria-label="Back to About" onclick={() => void backToAbout()}>
          <span class="flip"><Icon name="chevron-left" size={15} /></span>About
        </button>
        <h3 class="s-title" id="{uid}-notices-title" tabindex="-1">Open-source notices</h3>
        {#if copyText}
          <button type="button" class="btn sm" onclick={() => void copyNotices()}><Icon name="copy" size={14} />Copy all</button>
        {/if}
      </div>
      {#if notices.intro}
        <div class="intro">
          {#each introBlocks(notices.intro) as b, i (i)}
            {#if b.kind === 'heading'}
              <h4 class="intro-head">{b.text}</h4>
            {:else if b.kind === 'item'}
              <p class="intro-item">{b.text}</p>
            {:else}
              <p>{b.text}</p>
            {/if}
          {/each}
        </div>
      {/if}
      {#if notices.sections.length > 0}
        <p class="s-hint">{notices.sections.length} {notices.sections.length === 1 ? 'component' : 'components'}: open one to read its licence.</p>
        <div class="s-card components">
          {#each notices.sections as s, i (i)}
            <details class="comp">
              <summary><span class="chev"><Icon name="chevron-right" size={13} /></span><span class="comp-title">{s.title}</span></summary>
              <pre class="licence">{s.body}</pre>
            </details>
          {/each}
        </div>
      {/if}
      {#if noticesError}
        <p class="note fail" role="alert">{noticesError}</p>
      {/if}
    </section>
  {:else}
    <section class="hero" aria-labelledby="{uid}-name">
      <BrandMark size={44} />
      <div class="hero-text">
        <h3 class="hero-name" id="{uid}-name">Downex</h3>
        <p class="hero-version">{version ? `Version ${version}` : 'Version not known'}</p>
        <p class="s-hint diag-hint" id="{uid}-diag-hint">
          Diagnostics are versions, states and settings for a problem report: no addresses, file names, logins or proxy.
        </p>
      </div>
      {#if diagnostics && copyText}
        <button type="button" class="btn sm" onclick={() => void copyDiagnostics()} disabled={copying} aria-describedby="{uid}-diag-hint">
          {#if copying}<span class="spinner" aria-hidden="true"></span>{:else}<Icon name="copy" size={14} />{/if}Copy diagnostics
        </button>
      {/if}
    </section>
    {#if copyError}
      <p class="note fail" role="alert">{copyError}</p>
    {/if}
    {#if mismatch && known}
      <p class="note warn" role="alert">
        This window is version <bdi>{escapeBidi(build?.version ?? '')}</bdi>, but downexd is version <bdi>{escapeBidi(known.version)}</bdi>.
        Quit Downex from its icon in the notification area and start it again, so that both are the same version.
      </p>
    {/if}

    <!-- the four parts in two columns when the pane is wide (one below 49rem), read and tabbed
         down the first, then the second -->
    <div class="cols">
      <div class="col">
        <section class="s-section" aria-labelledby="{uid}-copy">
          <div class="s-head"><h3 class="s-title" id="{uid}-copy">This copy</h3></div>
          <dl class="s-card facts">
            <div class="fact">
              <dt>Window</dt>
              <dd>{windowFacts || 'Not known (an older window)'}</dd>
            </div>
            <div class="fact">
              <dt>downexd</dt>
              <dd class:off={daemon === 'down'}>{daemonFacts}</dd>
            </div>
            {#if built.length > 0}
              <div class="fact">
                <dt>Built with</dt>
                <dd>{built.join(' · ')}</dd>
              </div>
            {/if}
            <div class="fact">
              <dt>Updates</dt>
              <dd class="with">
                <span class:off={!updates.on}>{updates.text}</span>
                {#if onpage}
                  <!-- short, so that the state and the link keep to one line in a column -->
                  <button type="button" class="btn sm link" aria-label="Update settings" onclick={() => onpage?.('updates')}>Settings</button>
                {/if}
              </dd>
            </div>
          </dl>
        </section>

        <section class="s-section" aria-labelledby="{uid}-tools">
          <!-- Check for tool updates beside the title, its hint under the lead: no row of its own -->
          <div class="s-head">
            <div class="title-row">
              <h3 class="s-title" id="{uid}-tools">Video tools</h3>
              {#if tools?.supported}
                <button type="button" class="btn sm" onclick={() => void tools?.install(true)} disabled={!canCheckTools} aria-describedby="{uid}-tools-hint">
                  {#if tools.busy === 'update'}<span class="spinner" aria-hidden="true"></span>{:else}<Icon name="refresh" size={14} />{/if}
                  {tools.busy === 'update' ? 'Checking…' : 'Check for tool updates'}
                </button>
              {/if}
            </div>
            <p class="s-lead">yt-dlp reads video pages, with Deno for YouTube. They are separate programs that Downex downloads when you first use a video page.</p>
            {#if tools?.supported}
              <p class="s-hint" id="{uid}-tools-hint">
                {#if !siteMedia}
                  Video downloads from web pages are off (Video sites).
                {:else if tools.busy !== null}
                  This can take a few minutes.
                {:else}
                  Only Downex’s own copies are updated, never ones found on PATH.
                {/if}
              </p>
            {/if}
          </div>
          {#if !tools}
            <p class="s-lead">Their state is shown when Downex is connected to downexd.</p>
          {:else if !tools.supported}
            <p class="note info">This downexd cannot download videos from web pages.</p>
          {:else}
            <ul class="s-card tools" aria-label="Video tools">
              {#each toolList as t (t.name)}
                {@const st = toolState(t)}
                <li class="tool">
                  <span class="tool-name">{toolName(t.name)}</span>
                  <span class="tool-version mono">{t.version ? escapeBidi(t.version) : '—'}</span>
                  <span class="tool-where">{t.path ? (t.managed ? 'Downex copy' : 'Found on PATH') : ''}</span>
                  <span class="pill {st.tone}">{st.text}</span>
                </li>
              {/each}
            </ul>
            {#if tools.installError}
              <p class="note fail" role="alert"><bdi>{escapeBidi(tools.installError)}</bdi></p>
            {/if}
          {/if}
        </section>
      </div>

      <div class="col">
        <section class="s-section" aria-labelledby="{uid}-folders">
          <div class="s-head"><h3 class="s-title" id="{uid}-folders">Folders</h3></div>
          <ul class="s-card folders" aria-label="Folders">
            {#each folders as f (f.kind)}
              <li class="folder">
                <span class="folder-text">
                  <span class="folder-label">{f.label}<span class="folder-hint">{' · '}{f.hint}</span></span>
                  {#if f.path}
                    <span class="folder-path mono" dir="auto">{f.path}</span>
                  {:else}
                    <span class="folder-path missing">{daemon === null ? 'Asking downexd…' : 'Not reported by this downexd'}</span>
                  {/if}
                </span>
                <span class="folder-actions">
                  <button type="button" class="btn sm" aria-label="Open the {f.label} folder" disabled={!openAppFolder} onclick={() => void openFolder(f.kind, f.label)}>
                    <Icon name="folder" size={14} />Open
                  </button>
                  <button
                    type="button"
                    class="icon-btn sm"
                    aria-label="Copy the path of the {f.label} folder"
                    title="Copy the path"
                    disabled={!copyText || f.path === ''}
                    onclick={() => void copyPath(f.path, f.label)}
                  >
                    <Icon name="copy" size={14} />
                  </button>
                </span>
              </li>
            {/each}
          </ul>
          {#if folderError}
            <p class="note fail" role="alert">{folderError}</p>
          {/if}
        </section>

        <section class="s-section" aria-labelledby="{uid}-notices">
          <div class="s-head">
            <h3 class="s-title" id="{uid}-notices">Open-source notices</h3>
            <p class="s-lead">
              Downex is built with open-source software; its licences are listed here. yt-dlp and Deno are not part of Downex: their
              licences are on the Video sites page.
            </p>
          </div>
          <div class="s-after">
            <button type="button" class="btn sm" id="{uid}-view-notices" onclick={() => void showNotices()} disabled={!thirdPartyNotices || noticesBusy}>
              {#if noticesBusy}<span class="spinner" aria-hidden="true"></span>{:else}<Icon name="documents" size={14} />{/if}View open-source notices
            </button>
            {#if onpage}
              <button type="button" class="btn sm link" onclick={() => onpage?.('video')}>Video sites</button>
            {/if}
          </div>
          {#if noticesError}
            <p class="note fail" role="alert">{noticesError}</p>
          {/if}
        </section>
      </div>
    </div>
  {/if}

  <!-- What the page did (copied …), in one polite live region that stays in the page; its pill
       keeps to the bottom of the pane while it shows. -->
  <p class="said" role="status">{#if said}<span class="said-pill"><Icon name="check" size={14} />{said}</span>{/if}</p>
</div>

<style>
  /* block flow, 24 px between the parts: the empty live region at the end then costs nothing
     (a grid row of its own would add a gap that a negative margin cannot take back) */
  .about {
    display: flow-root;
    min-inline-size: 0;
  }
  .about > :not(:first-child, .said) {
    margin-block-start: 24px;
  }
  .cols {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    align-items: start;
    gap: 24px 28px;
    min-inline-size: 0;
  }
  .col {
    display: grid;
    gap: 24px;
    min-inline-size: 0;
  }
  @container settings-pane (min-width: 49rem) {
    .cols {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
  }
  /* the live region takes no room (no block size, no margin); its pill floats at the pane's bottom */
  .said {
    position: sticky;
    inset-block-end: 0;
    z-index: 1;
    block-size: 0;
  }
  /* it holds no control: a click on what it covers for its 4 s goes through to it */
  .said-pill {
    pointer-events: none;
    position: absolute;
    inset-block-end: 4px;
    inset-inline-end: 0;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    max-inline-size: 100%;
    padding: 5px 12px;
    border: 1px solid var(--accent-line);
    border-radius: var(--r-pill);
    background: var(--raised);
    box-shadow: var(--shadow-sm);
    color: var(--accent-text);
    font-size: var(--type-small);
    font-weight: 500;
    white-space: nowrap;
  }

  /* ---- the header: the mark, the name and version, Copy diagnostics ---- */
  .hero {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    align-items: center;
    gap: 14px;
    padding: 14px 16px;
    border: 1px solid var(--line);
    border-radius: var(--r-lg);
    background: linear-gradient(var(--accent-soft), var(--accent-soft)) no-repeat 0 0 / 3px 100%, var(--surface);
  }
  .hero-text {
    display: grid;
    gap: 1px;
    min-inline-size: 0;
  }
  .hero-name {
    font: 650 var(--type-page) / 1.2 var(--font-display);
    letter-spacing: -0.015em;
    color: var(--ink);
  }
  .hero-version {
    color: var(--muted);
    font-size: var(--type-cell);
  }
  .diag-hint {
    margin-block-start: 3px;
  }
  .title-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px 12px;
    min-inline-size: 0;
  }
  .title-row > .btn {
    flex: none;
  }

  /* ---- This copy: label / value rows ---- */
  .facts {
    margin: 0;
  }
  .fact {
    display: grid;
    grid-template-columns: 6rem minmax(0, 1fr);
    align-items: baseline;
    gap: 4px 16px;
    min-block-size: 36px;
    padding-block: 8px;
    padding-inline: 14px;
  }
  .fact + .fact {
    border-block-start: 1px solid var(--line-soft);
  }
  dt {
    color: var(--muted);
    font-size: var(--type-cell);
  }
  dd {
    margin: 0;
    min-inline-size: 0;
    color: var(--ink);
    font-size: var(--type-cell);
    overflow-wrap: anywhere;
  }
  dd.with {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    justify-content: space-between;
    gap: 4px 12px;
  }
  .off {
    color: var(--muted);
  }
  .btn.link {
    font-size: var(--type-cell);
  }

  /* ---- video tools: one line each ---- */
  .tools,
  .folders {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .tool {
    display: grid;
    grid-template-columns: 5rem minmax(0, auto) minmax(0, 1fr) auto;
    align-items: center;
    gap: 12px;
    min-block-size: 40px;
    padding-inline: 14px;
  }
  .tool + .tool,
  .folder + .folder {
    border-block-start: 1px solid var(--line-soft);
  }
  .tool-name {
    color: var(--ink);
    font-weight: 600;
  }
  .tool-version {
    color: var(--ink-2);
    font-size: var(--type-cell);
  }
  .tool-where {
    color: var(--faint);
    font-size: var(--type-small);
  }

  /* ---- folders ---- */
  .folder {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: center;
    gap: 12px;
    padding-block: 7px;
    padding-inline: 14px;
  }
  .folder-text {
    display: grid;
    gap: 2px;
    min-inline-size: 0;
  }
  .folder-label {
    color: var(--ink);
    font-weight: 500;
  }
  .folder-hint {
    color: var(--faint);
    font-size: var(--type-small);
    font-weight: 400;
  }
  .folder-path {
    color: var(--ink-2);
    font-size: var(--type-small);
    overflow-wrap: anywhere;
    user-select: text;
  }
  .folder-path.missing {
    color: var(--faint);
    font-style: italic;
  }
  .folder-actions {
    display: flex;
    align-items: center;
    gap: 4px;
  }
  .s-after .spinner,
  .hero .spinner {
    inline-size: 12px;
    block-size: 12px;
  }

  /* ---- the notices view ---- */
  .nhead {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    align-items: center;
    gap: 10px;
  }
  .nhead .s-title:focus {
    outline: none;
  }
  .back {
    gap: 2px;
    padding-inline-start: 4px;
  }
  :global([dir='rtl']) .flip {
    display: inline-grid;
    transform: scaleX(-1);
  }
  .intro {
    display: grid;
    gap: 8px;
    max-inline-size: 46rem;
    color: var(--ink-2);
    font-size: var(--type-cell);
    line-height: 1.5;
  }
  .intro-head {
    margin: 4px 0 0;
    color: var(--ink);
    font: 600 var(--type-body) / 1.3 var(--font-display);
  }
  .intro-item {
    position: relative;
    padding-inline-start: 14px;
    overflow-wrap: anywhere;
  }
  .intro-item::before {
    content: '';
    position: absolute;
    inset-block-start: 0.6em;
    inset-inline-start: 2px;
    inline-size: 5px;
    block-size: 5px;
    border-radius: 50%;
    background: var(--accent);
  }
  .components {
    overflow: hidden;
  }
  .comp + .comp {
    border-block-start: 1px solid var(--line-soft);
  }
  .comp summary {
    display: flex;
    align-items: center;
    gap: 6px;
    min-block-size: 34px;
    padding-inline: 10px 14px;
    color: var(--ink);
    cursor: pointer;
    list-style: none;
  }
  .comp summary::-webkit-details-marker {
    display: none;
  }
  .comp summary:hover {
    background: var(--hover);
  }
  .comp summary:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }
  .chev {
    display: grid;
    flex: none;
    color: var(--muted);
    transition: transform var(--dur-2) var(--ease-out);
  }
  .comp[open] .chev {
    transform: rotate(90deg);
    color: var(--accent);
  }
  :global([dir='rtl']) .comp:not([open]) .chev {
    transform: rotate(180deg);
  }
  .comp-title {
    min-inline-size: 0;
    overflow: hidden;
    font-size: var(--type-cell);
    font-weight: 500;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .licence {
    margin: 0;
    padding: 10px 14px 14px 31px;
    border-block-start: 1px solid var(--line-soft);
    background: var(--surface-2);
    color: var(--ink-2);
    font: 400 var(--type-small) / 1.5 var(--font-mono, ui-monospace, monospace);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    user-select: text;
  }

  /* A narrow pane (larger text in a small window): the rows stack. */
  @container settings-pane (max-width: 34rem) {
    .hero {
      grid-template-columns: auto minmax(0, 1fr);
    }
    .hero > .btn {
      grid-column: 1 / -1;
      justify-self: start;
    }
    .fact {
      grid-template-columns: minmax(0, 1fr);
    }
    .tool {
      grid-template-columns: 4rem minmax(0, 1fr) auto;
    }
    .tool-where {
      display: none;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .chev {
      transition: none;
    }
  }
  @media (forced-colors: active) {
    .hero,
    .said-pill {
      border-color: CanvasText;
    }
  }
</style>
