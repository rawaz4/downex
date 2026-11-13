<script lang="ts">
  // The Settings window (DESIGN-SYSTEM.md §3.20, §4.9): a dialog of 55 × 46 rem (less where the
  // window has less room) that does not change size between its pages, a side navigation (a
  // vertical tablist; a row of tabs when the dialog is narrow) and a content pane that scrolls. Save writes the settings downexd keeps; Appearance (theme, density) belongs to this
  // computer and applies at once. The pages share the layout classes (s-*) defined here.
  import { onMount, tick, type ComponentProps } from 'svelte';
  import {
    DEFAULT_SETTINGS,
    toApiError,
    type Capabilities,
    type SecretKind,
    type Settings,
    type SiteLogin,
    type UpdateInfo,
  } from '../api';
  import Dialog from './Dialog.svelte';
  import Icon, { type IconName } from './Icon.svelte';
  import SettingsAbout from './SettingsAbout.svelte';
  import SettingsAppearance from './SettingsAppearance.svelte';
  import SettingsDownloads from './SettingsDownloads.svelte';
  import SettingsIntegration from './SettingsIntegration.svelte';
  import SettingsNetwork from './SettingsNetwork.svelte';
  import SettingsUpdates from './SettingsUpdates.svelte';
  import SettingsVideo from './SettingsVideo.svelte';
  import Switch from './Switch.svelte';
  import { splitRate, type RateUnit } from '../format';
  import { escapeBidi } from '../text';
  import {
    applyLoginEditor,
    categoryDrafts,
    errorsOnPage,
    firstErrorPage,
    integrationForm,
    loginEditorChanged,
    networkForm,
    newLoginEditor,
    updatesForm,
    validateSettingsForm,
    videoForm,
    type CategoryDraft,
    type IntegrationForm,
    type NetworkForm,
    type SettingsField,
    type SettingsFormErrors,
    type SettingsPage,
    type SiteLoginDraft,
    type SiteLoginEditor,
    type SiteLoginEditorErrors,
    type UpdatesForm,
    type VideoForm,
  } from '../validate';

  interface Props {
    load: () => Promise<Settings>;
    save: (settings: Settings) => Promise<Settings>;
    pickFolder: (initialDir: string) => Promise<string | null>;
    onclose: () => void;
    onsaved?: (settings: Settings) => void;
    fallbackFocus?: () => HTMLElement | null | undefined;
    /** The page shown first (default: General; Downloads with `focusField`). */
    initialPage?: SettingsPage;
    /** Opens with this field focused (the status bar's "Custom…" limit: Downloads). */
    focusField?: SettingsField;
    /** Opens Network with the editor of a new site login for this address pattern (auth_required). */
    addLogin?: string;
    /** Stores and removes passwords (M6-SPEC §2); without them, passwords cannot be entered. */
    setSecret?: (kind: SecretKind, id: string, password: string) => Promise<void>;
    clearSecret?: (kind: SecretKind, id: string) => Promise<void>;
    checkForUpdate?: () => Promise<UpdateInfo>;
    installUpdate?: () => Promise<UpdateInfo>;
    pickFile?: (opts: { title: string; initialDir: string }) => Promise<string | null>;
    capabilities?: Pick<Capabilities, 'hibernate' | 'wakeTimers'>;
    /** yt-dlp and Deno (Video sites): their state and install. */
    tools?: ComponentProps<typeof SettingsVideo>['tools'];
    /** Opens a fixed https address (a licence) in the system's browser. */
    openExternal?: (url: string) => Promise<void>;
    /** The theme and density stores Appearance changes (tests give their own). */
    appearance?: ComponentProps<typeof SettingsAppearance>;
    /** About: the build, downexd's status, the folders, diagnostics and notices (the page reads the saved settings itself). */
    about?: Omit<ComponentProps<typeof SettingsAbout>, 'updateFeedUrl' | 'autoCheckUpdates' | 'defaultDir' | 'siteMedia' | 'onpage'>;
  }

  let {
    load,
    save,
    pickFolder,
    onclose,
    onsaved,
    fallbackFocus,
    initialPage,
    focusField,
    addLogin,
    setSecret,
    clearSecret,
    checkForUpdate,
    installUpdate,
    pickFile,
    capabilities = { hibernate: true, wakeTimers: false },
    tools,
    openExternal,
    appearance = {},
    about = {},
  }: Props = $props();

  const uid = $props.id();

  const PAGES: readonly { id: SettingsPage; label: string; icon: IconName }[] = [
    { id: 'general', label: 'General', icon: 'gear' },
    { id: 'downloads', label: 'Downloads', icon: 'arrow-down' },
    { id: 'network', label: 'Network', icon: 'globe' },
    { id: 'integration', label: 'Integration', icon: 'plug' },
    { id: 'video', label: 'Video sites', icon: 'stream' },
    { id: 'appearance', label: 'Appearance', icon: 'density' },
    { id: 'updates', label: 'Updates', icon: 'refresh' },
    { id: 'about', label: 'About', icon: 'info' },
  ];
  // svelte-ignore state_referenced_locally
  let page = $state<SettingsPage>(
    addLogin !== undefined ? 'network' : (initialPage ?? (focusField === 'globalSpeedLimit' ? 'downloads' : 'general')),
  );
  const platform = $derived(capabilities.wakeTimers ? 'windows' : !capabilities.hibernate ? 'mac' : 'other');

  type Mutable<T> = { -readonly [K in keyof T]: T[K] };
  let network = $state<Mutable<NetworkForm>>(networkForm(DEFAULT_SETTINGS));
  let integration = $state<Mutable<IntegrationForm>>(integrationForm(DEFAULT_SETTINGS));
  let updates = $state<Mutable<UpdatesForm>>(updatesForm(DEFAULT_SETTINGS));
  let video = $state<Mutable<VideoForm>>(videoForm(DEFAULT_SETTINGS));
  let proxyPassword = $state('');
  let clearProxyPassword = $state(false);
  /** Site logins with a password when loaded: removing one also removes its password. */
  let loadedLogins = $state.raw<readonly SiteLogin[]>([]);
  /** Network's site login editor (kept across pages; Save applies it). */
  let loginEditor = $state<SiteLoginEditor | null>(null);
  let loginEditorError = $state<SiteLoginEditorErrors>({});
  /** Save found the login editor holding a login that cannot be added (until it closes). */
  let loginEditorBlocked = $state(false);
  $effect(() => {
    if (loginEditor === null) loginEditorBlocked = false;
  });

  type Toggle =
    | 'useCategoryFolders'
    | 'confirmBrowserDownloads'
    | 'notifyOnComplete'
    | 'notifyOnFailure'
    | 'launchAtLogin'
    | 'showTrayIcon'
    | 'showProgressWindow'
    | 'showCompleteDialog';

  interface ToggleRow {
    readonly key: Toggle;
    readonly label: string;
    readonly hint?: string;
  }

  const STARTUP_TOGGLES: readonly ToggleRow[] = [
    { key: 'launchAtLogin', label: 'Start Downex in the background when I sign in', hint: 'Downex then runs in the background, so scheduled queues start on time.' },
    { key: 'showTrayIcon', label: 'Show the Downex icon in the notification area' },
  ];
  const NOTIFY_TOGGLES: readonly ToggleRow[] = [
    { key: 'notifyOnComplete', label: 'Notify me when a download completes' },
    { key: 'notifyOnFailure', label: 'Notify me when a download fails' },
  ];
  const PROGRESS_TOGGLES: readonly ToggleRow[] = [
    { key: 'showProgressWindow', label: 'Show a progress window when I start a download' },
    { key: 'showCompleteDialog', label: 'Show the download complete dialog for downloads I started' },
  ];

  let loading = $state(true);
  let loadError = $state<string | null>(null);
  let saveError = $state<string | null>(null);
  let busy = $state(false);
  let attempted = $state(false);
  /** The settings as loaded: fields this dialog does not show are written back from it. */
  let base = $state.raw<Settings>(DEFAULT_SETTINGS);
  let maxConcurrent = $state('3');
  let speedValue = $state('');
  let speedUnit = $state<RateUnit>('MB/s');
  let defaultDir = $state('');
  let defaultConnections = $state('8');
  let categories = $state<CategoryDraft[]>([]);
  let toggles = $state<Record<Toggle, boolean>>({
    useCategoryFolders: true,
    confirmBrowserDownloads: true,
    notifyOnComplete: true,
    notifyOnFailure: true,
    launchAtLogin: false,
    showTrayIcon: true,
    showProgressWindow: true,
    showCompleteDialog: true,
  });
  let nextKey = 1;
  /** The content pane (the tab panel), which scrolls. */
  let body: HTMLDivElement | undefined = $state();

  const result = $derived(
    validateSettingsForm(
      {
        maxConcurrent,
        speedValue,
        speedUnit,
        speedLimitBytes: base.globalSpeedLimit,
        defaultDir,
        defaultConnections,
        categories,
        ...toggles,
        network,
        integration,
        updates,
        video,
      },
      base,
    ),
  );
  const errors: SettingsFormErrors = $derived(attempted && !result.ok ? result.errors : {});
  /** Pages whose fields need attention (marked in the side navigation). */
  const pagesWithErrors = $derived(
    new Set(
      PAGES.map((p) => p.id).filter(
        (p) => Object.keys(errorsOnPage(errors, p)).length > 0 || (p === 'network' && loginEditorBlocked && loginEditor !== null),
      ),
    ),
  );

  function newKey(): number {
    return nextKey++;
  }

  async function showPage(id: SettingsPage, focusTab = false) {
    if (page !== id) {
      page = id;
      if (body) body.scrollTop = 0;
    }
    if (!focusTab) return;
    await tick();
    document.getElementById(`${uid}-tab-${id}`)?.focus();
  }

  /** The side navigation is a vertical tablist: Up/Down (and Left/Right), Home and End. */
  function onTabKeydown(e: KeyboardEvent) {
    const i = PAGES.findIndex((p) => p.id === page);
    let next = -1;
    if (e.key === 'ArrowDown' || e.key === 'ArrowRight') next = (i + 1) % PAGES.length;
    else if (e.key === 'ArrowUp' || e.key === 'ArrowLeft') next = (i - 1 + PAGES.length) % PAGES.length;
    else if (e.key === 'Home') next = 0;
    else if (e.key === 'End') next = PAGES.length - 1;
    if (next < 0) return;
    e.preventDefault();
    const target = PAGES[next];
    if (target) void showPage(target.id, true);
  }

  /** Takes loaded settings into every page's form. */
  function fill(s: Settings) {
    base = s;
    maxConcurrent = String(s.maxConcurrentDownloads);
    const rate = splitRate(s.globalSpeedLimit);
    speedValue = rate.value;
    speedUnit = rate.unit;
    defaultDir = s.defaultDir;
    defaultConnections = String(s.defaultMaxConnections);
    categories = categoryDrafts(s.categories, nextKey);
    nextKey += s.categories.length;
    toggles = {
      useCategoryFolders: s.useCategoryFolders,
      confirmBrowserDownloads: s.confirmBrowserDownloads,
      notifyOnComplete: s.notifyOnComplete,
      notifyOnFailure: s.notifyOnFailure,
      launchAtLogin: s.launchAtLogin,
      showTrayIcon: s.showTrayIcon,
      showProgressWindow: s.showProgressWindow,
      showCompleteDialog: s.showCompleteDialog,
    };
    network = networkForm(s, nextKey);
    nextKey += s.hostRules.length + s.siteLogins.length;
    integration = integrationForm(s);
    updates = updatesForm(s);
    video = videoForm(s);
    proxyPassword = '';
    clearProxyPassword = false;
    loadedLogins = s.siteLogins;
  }

  onMount(async () => {
    try {
      fill(await load());
    } catch (err) {
      loadError = toApiError(err).message;
    } finally {
      loading = false;
    }
    // A server asked for credentials (auth_required): the editor of a new login for it,
    // opened once (it then lives on until applied or cancelled, across pages).
    if (addLogin !== undefined) loginEditor = newLoginEditor(addLogin);
    // The fields were disabled while loading, so focus started on the close button: it goes
    // to the field asked for, else to the chosen page in the side navigation.
    await tick();
    let target: HTMLElement | null | undefined;
    if (addLogin !== undefined) target = body?.querySelector<HTMLElement>('[data-login-field="user"]');
    else if (focusField) target = body?.querySelector<HTMLElement>(`[data-field="${focusField}"]:not([disabled])`);
    target ??= document.getElementById(`${uid}-tab-${page}`);
    target?.focus();
    if (focusField && target instanceof HTMLInputElement) target.select();
  });

  /**
   * Save with the login editor open: a login entered there is added to the list (its
   * password is stored with the others); one that cannot be added stops the save. An
   * editor holding nothing new is closed. Resolves to whether saving may go on.
   */
  async function takeLoginEditor(): Promise<boolean> {
    const e = loginEditor;
    if (!e) return true;
    if (!loginEditorChanged(e, network.siteLogins)) {
      loginEditor = null;
      loginEditorError = {};
      return true;
    }
    const applied = applyLoginEditor(network.siteLogins, e, newKey);
    if (applied.ok) {
      network.siteLogins = applied.siteLogins;
      loginEditor = null;
      loginEditorError = {};
      loginEditorBlocked = false;
      return true;
    }
    loginEditorError = applied.errors;
    loginEditorBlocked = true;
    await showPage('network');
    await tick();
    body?.querySelector<HTMLElement>('[data-login-editor] [aria-invalid="true"]')?.focus();
    return false;
  }

  async function onsubmit() {
    if (loading || loadError) return;
    attempted = true;
    saveError = null;
    if (!(await takeLoginEditor())) return;
    if (!result.ok) {
      // Stay on this page when it has an error, else show the first page that has one.
      const target = firstErrorPage(errorsOnPage(result.errors, page)) ?? firstErrorPage(result.errors);
      if (target) await showPage(target);
      await tick();
      body?.querySelector<HTMLElement>('[aria-invalid="true"], [data-invalid="true"]')?.focus();
      return;
    }
    busy = true;
    const logins = network.siteLogins;
    try {
      const saved = await save(result.settings);
      const failed = await storeSecrets(saved, logins);
      const fresh = failed.length > 0 || hasSecretChanges(logins) ? await load().catch(() => saved) : saved;
      if (failed.length > 0) {
        // The settings are saved: show what they are now, and what is missing.
        fill(fresh);
        attempted = false;
        saveError = `Settings saved, but ${failed.join('; ')}. Enter the password again.`;
        onsaved?.(fresh);
        return;
      }
      onsaved?.(fresh);
      onclose();
    } catch (err) {
      saveError = `Could not save: ${toApiError(err).message}`;
    } finally {
      busy = false;
    }
  }

  function hasSecretChanges(logins: readonly SiteLoginDraft[]): boolean {
    return (
      proxyPassword !== '' ||
      clearProxyPassword ||
      logins.some((l) => l.password !== '' || l.clearPassword) ||
      loadedLogins.some((l) => l.hasPassword && !logins.some((d) => d.id === l.id))
    );
  }

  /**
   * After the settings are saved: stores new passwords, removes cleared ones, and those of
   * removed logins. A new login gets its id from downexd: it is found in the saved settings
   * by its address and user name. Resolves to what could not be stored.
   */
  async function storeSecrets(saved: Settings, logins: readonly SiteLoginDraft[]): Promise<string[]> {
    const failed: string[] = [];
    const attempt = async (what: string, fn: () => Promise<void>) => {
      try {
        await fn();
      } catch (err) {
        failed.push(`${what} could not be stored (${toApiError(err).message})`);
      }
    };
    if (proxyPassword !== '' && setSecret) {
      const pw = proxyPassword;
      await attempt('the proxy password', () => setSecret('proxy', '', pw));
    } else if (clearProxyPassword && clearSecret) {
      await attempt('the removal of the proxy password', () => clearSecret('proxy', ''));
    }
    const known = new Set(loadedLogins.map((l) => l.id));
    const claimed = new Set<string>();
    for (const l of logins) {
      if (l.password === '' && !l.clearPassword) continue;
      let id = l.id;
      if (id === '') {
        const match = saved.siteLogins.find(
          (s) => s.id !== '' && !known.has(s.id) && !claimed.has(s.id) && s.urlPattern === l.urlPattern.trim() && s.username === l.username.trim(),
        );
        id = match?.id ?? '';
      }
      const what = `the password for ${escapeBidi(l.urlPattern.trim())}`;
      if (id === '') {
        if (l.password) failed.push(`${what} could not be stored (downexd did not return the new login)`);
        continue;
      }
      claimed.add(id);
      if (l.password !== '' && setSecret) {
        const pw = l.password;
        await attempt(what, () => setSecret('site_login', id, pw));
      } else if (l.clearPassword && clearSecret) {
        await attempt(`the removal of ${what}`, () => clearSecret('site_login', id));
      }
    }
    // Passwords of removed logins are removed too (downexd may have done it already).
    if (clearSecret) {
      for (const l of loadedLogins) {
        if (l.hasPassword && !saved.siteLogins.some((s) => s.id === l.id)) {
          await clearSecret('site_login', l.id).catch(() => undefined);
        }
      }
    }
    return failed;
  }
</script>

{#snippet toggleRows(rows: readonly ToggleRow[])}
  {#each rows as t (t.key)}
    <div class="s-row s-switch">
      <Switch bind:checked={toggles[t.key]} label={t.label} hint={t.hint} disabled={loading} />
    </div>
  {/each}
{/snippet}

<Dialog title="Settings" {onclose} {onsubmit} {busy} {fallbackFocus} width="70rem" fixedHeight="46rem" flush>
  <div class="settings-shell">
    <div
      class="nav"
      role="tablist"
      aria-label="Settings pages"
      aria-orientation="vertical"
      tabindex="-1"
      onkeydown={onTabKeydown}
    >
      {#each PAGES as p (p.id)}
        <button
          type="button"
          role="tab"
          id="{uid}-tab-{p.id}"
          class="tab"
          class:invalid={pagesWithErrors.has(p.id)}
          aria-selected={page === p.id}
          aria-controls="{uid}-panel"
          tabindex={page === p.id ? 0 : -1}
          onclick={() => void showPage(p.id)}
        >
          <span class="tab-icon"><Icon name={p.icon} size={16} /></span>
          <span class="tab-label">{p.label}</span>{#if pagesWithErrors.has(p.id)}<span class="sr-only"> (needs attention)</span><span
              class="mark"
              aria-hidden="true">!</span
            >{/if}
        </button>
      {/each}
    </div>

    <div class="pane" bind:this={body} aria-busy={loading} role="tabpanel" id="{uid}-panel" aria-labelledby="{uid}-tab-{page}">
      {#if loadError}
        <p class="note fail load-error" role="alert">Could not load settings: <bdi>{escapeBidi(loadError)}</bdi></p>
      {/if}

      {#key page}
        <div class="page" class:cols={page === 'integration'} class:vgrid={page === 'video'}>
          {#if page === 'downloads'}
            <SettingsDownloads
              bind:maxConcurrent
              bind:defaultConnections
              bind:speedValue
              bind:speedUnit
              bind:defaultDir
              bind:categories
              bind:useCategoryFolders={toggles.useCategoryFolders}
              {errors}
              {pickFolder}
              {newKey}
              disabled={loading}
            />
          {:else if page === 'network'}
            <SettingsNetwork
              bind:form={network}
              bind:proxyPassword
              bind:clearProxyPassword
              bind:editor={loginEditor}
              bind:editorError={loginEditorError}
              {errors}
              hasProxyPassword={base.proxy.hasPassword}
              {newKey}
              disabled={loading}
            />
          {:else if page === 'integration'}
            <SettingsIntegration
              bind:form={integration}
              bind:confirmBrowserDownloads={toggles.confirmBrowserDownloads}
              {errors}
              {pickFile}
              disabled={loading}
              {platform}
            />
          {:else if page === 'video'}
            <SettingsVideo bind:form={video} savedSiteMedia={base.siteMedia} {tools} {openExternal} disabled={loading} />
          {:else if page === 'appearance'}
            <SettingsAppearance {...appearance} />
          {:else if page === 'updates'}
            <SettingsUpdates
              bind:form={updates}
              {errors}
              savedFeedUrl={base.updateFeedUrl}
              {checkForUpdate}
              {installUpdate}
              disabled={loading}
            />
          {:else if page === 'about'}
            <SettingsAbout
              {tools}
              {...about}
              updateFeedUrl={base.updateFeedUrl}
              autoCheckUpdates={base.autoCheckUpdates}
              defaultDir={base.defaultDir}
              siteMedia={base.siteMedia}
              onpage={(p) => void showPage(p)}
            />
          {:else}
            <section class="s-section" aria-labelledby="{uid}-startup">
              <div class="s-head"><h3 class="s-title" id="{uid}-startup">Start-up</h3></div>
              <div class="s-card">{@render toggleRows(STARTUP_TOGGLES)}</div>
            </section>
            <section class="s-section" aria-labelledby="{uid}-notify">
              <div class="s-head"><h3 class="s-title" id="{uid}-notify">Notifications</h3></div>
              <div class="s-card">{@render toggleRows(NOTIFY_TOGGLES)}</div>
            </section>
            <section class="s-section" aria-labelledby="{uid}-progress">
              <div class="s-head">
                <h3 class="s-title" id="{uid}-progress">Progress windows</h3>
                <p class="s-lead">
                  A download’s progress window can always be opened from the list (double-click it, or choose Show progress).
                </p>
              </div>
              <div class="s-card">{@render toggleRows(PROGRESS_TOGGLES)}</div>
            </section>
          {/if}
        </div>
      {/key}
    </div>
  </div>

  {#snippet footerStart()}
    {#if loginEditorBlocked && loginEditor}
      <p class="foot-msg" role="alert"><Icon name="alert" size={14} />Add or cancel the site login you are editing (Network), then save.</p>
    {:else if attempted && !result.ok}
      <p class="foot-msg" role="alert"><Icon name="alert" size={14} />Some settings need attention.</p>
    {/if}
    {#if saveError}
      <p class="foot-msg" role="alert"><Icon name="alert" size={14} /><span>{saveError}</span></p>
    {/if}
  {/snippet}

  {#snippet footer()}
    <button type="button" class="btn" onclick={onclose} disabled={busy}>Cancel</button>
    <button type="submit" class="btn primary" disabled={busy || loading || loadError !== null}>
      {busy ? 'Saving…' : 'Save'}
    </button>
  {/snippet}
</Dialog>

<style>
  /* The dialog's body holds only this shell: its one row fills the fixed height, so that the
     side navigation runs from the header to the footer and only the pane scrolls. */
  :global(.body.flush:has(> .settings-shell)) {
    grid-template-rows: minmax(0, 1fr);
    overflow: hidden;
  }
  .settings-shell {
    display: grid;
    grid-template-columns: 12.5rem minmax(0, 1fr);
    min-block-size: 0;
    border-block-start: 1px solid var(--line);
  }

  /* ---- side navigation: a vertical tablist ---- */
  .nav {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-block-size: 0;
    overflow-y: auto;
    padding: 10px 8px;
    border-inline-end: 1px solid var(--line);
    background: var(--surface-2);
  }
  .nav:focus {
    outline: none;
  }
  .tab {
    position: relative;
    display: flex;
    flex: none;
    align-items: center;
    gap: 10px;
    block-size: 32px;
    padding-block: 0;
    padding-inline: 10px 8px;
    border: 0;
    border-radius: 7px;
    background: transparent;
    color: var(--ink-2);
    font-size: var(--type-body);
    font-weight: 500;
    text-align: start;
    cursor: pointer;
    transition:
      background-color var(--dur-2) ease,
      color var(--dur-2) ease;
  }
  .tab-icon {
    display: grid;
    flex: none;
    color: var(--muted);
  }
  .tab-label {
    flex: 1 1 auto;
    min-inline-size: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .tab:hover:not([aria-selected='true']) {
    background: var(--hover);
    color: var(--ink);
  }
  .tab[aria-selected='true'] {
    background: var(--sel);
    box-shadow: inset 0 0 0 1px var(--sel-line);
    color: var(--ink);
    font-weight: 600;
  }
  .tab[aria-selected='true'] .tab-icon {
    color: var(--accent);
  }
  .tab:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }
  .tab.invalid,
  .tab.invalid .tab-icon {
    color: var(--danger);
  }
  .mark {
    display: grid;
    flex: none;
    place-items: center;
    inline-size: 16px;
    block-size: 16px;
    border-radius: 50%;
    background: var(--danger);
    color: var(--tone-on);
    font-size: 0.6875rem;
    font-weight: 650;
    line-height: 1;
  }

  /* ---- the content pane ---- */
  /* The pages adapt to the pane's width (@container settings-pane (…)), not the window's. */
  .pane {
    container: settings-pane / inline-size;
    min-block-size: 0;
    overflow-y: auto;
    scrollbar-gutter: stable;
    padding-block: 22px 28px;
    padding-inline: 24px 20px;
  }
  .pane:focus {
    outline: none;
  }
  .load-error {
    margin-block-end: 16px;
  }
  .page {
    display: grid;
    gap: 24px;
    animation: page-in var(--dur-3) var(--ease-out);
  }
  /* A wide pane (the dialog is up to 70rem): the page's sections in two balanced columns, read
     (and tabbed) down the first, then the second; one column when the pane is narrower. */
  .page.cols {
    display: block;
    columns: 2 24rem;
    column-gap: 28px;
  }
  .page.cols > :global(*) {
    break-inside: avoid;
    margin-block-end: 24px;
  }
  /* Video sites: its settings and its licences on the start side, the tools (the longest part)
     beside them; Tab takes the settings, the tools, then the licences. */
  @container settings-pane (min-width: 50rem) {
    .page.vgrid {
      grid-template-columns: repeat(2, minmax(0, 1fr));
      grid-template-rows: auto minmax(0, 1fr);
      align-items: start;
      column-gap: 28px;
    }
    .page.vgrid > :global(:nth-child(1)) {
      grid-area: 1 / 1;
    }
    .page.vgrid > :global(:nth-child(2)) {
      grid-area: 1 / 2 / span 2;
    }
    .page.vgrid > :global(:nth-child(3)) {
      grid-area: 2 / 1;
    }
  }
  @keyframes page-in {
    from {
      opacity: 0;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .page {
      animation: none;
    }
    .tab {
      transition: none;
    }
  }

  /* ---- the pages' shared layout (s-*) ---- */
  .pane :global(:where(.s-section)) {
    display: grid;
    gap: 12px;
    min-inline-size: 0;
  }
  .pane :global(:where(.s-head)) {
    display: grid;
    gap: 3px;
  }
  .pane :global(:where(.s-title)) {
    font: 650 var(--type-section) / 1.3 var(--font-display);
    letter-spacing: -0.01em;
    color: var(--ink);
  }
  .pane :global(:where(.s-lead)) {
    color: var(--muted);
    font-size: var(--type-cell);
    line-height: 1.45;
  }
  .pane :global(:where(.s-card)) {
    display: grid;
    min-inline-size: 0;
    border: 1px solid var(--line);
    border-radius: var(--r-lg);
    background: var(--surface);
  }
  .pane :global(:where(.s-card[hidden])) {
    display: none;
  }
  .pane :global(:where(.s-row)) {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: center;
    gap: 6px 20px;
    min-block-size: 52px;
    padding-block: 9px;
    padding-inline: 14px;
  }
  .pane :global(:where(.s-row + .s-row)) {
    border-block-start: 1px solid var(--line-soft);
  }
  .pane :global(:where(.s-row.stack)) {
    grid-template-columns: minmax(0, 1fr);
    gap: 10px;
    padding-block: 12px;
  }
  .pane :global(:where(.s-row.s-switch)) {
    display: block;
    padding-block: 2px;
  }
  .pane :global(:where(.s-text)) {
    display: grid;
    gap: 2px;
    min-inline-size: 0;
  }
  .pane :global(:where(.s-label)) {
    color: var(--ink);
    font-size: var(--type-body);
    font-weight: 500;
  }
  .pane :global(:where(.s-hint)) {
    color: var(--faint);
    font-size: var(--type-small);
  }
  .pane :global(:where(.s-control)) {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: 8px;
    min-inline-size: 0;
  }
  .pane :global(:where(.s-control.wide)) {
    justify-content: stretch;
  }
  .pane :global(:where(.s-field-icon)) {
    position: relative;
    display: block;
    flex: 1 1 auto;
    min-inline-size: 0;
  }
  .pane :global(:where(.s-field-icon > .icon)) {
    position: absolute;
    inset-block: 0;
    inset-inline-start: 10px;
    margin-block: auto;
    color: var(--faint);
    pointer-events: none;
  }
  .pane :global(:where(.s-field-icon > .input)) {
    padding-inline-start: 34px;
  }
  .pane :global(:where(.s-error)) {
    grid-column: 1 / -1;
    margin-block-start: -2px;
  }
  .pane :global(:where(.s-empty)) {
    display: block;
    min-block-size: 0;
    color: var(--muted);
    font-size: var(--type-cell);
  }
  .pane :global(:where(.s-error-row)) {
    display: block;
    min-block-size: 0;
  }
  .pane :global(:where(.s-after)) {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px 12px;
  }
  .pane :global(:where(.s-note)) {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    color: var(--muted);
    font-size: var(--type-cell);
  }
  .pane :global(:where(.s-note .icon)) {
    color: var(--accent);
  }

  /* ---- the footer's messages ---- */
  .foot-msg {
    display: flex;
    align-items: flex-start;
    gap: 7px;
    color: var(--danger);
    font-size: var(--type-cell);
    line-height: 1.35;
  }
  .foot-msg :global(.icon) {
    flex: none;
    margin-block-start: 1px;
  }

  /* A narrow dialog (larger text in a small window): the side navigation becomes a row of tabs
     above the pane, drawn as Tabs are (§3.4.1): on a --line hairline, one 2 px Mint bar under
     the chosen label; the row scrolls sideways when it has to. */
  @container dialog (max-width: 44rem) {
    .settings-shell {
      grid-template-columns: minmax(0, 1fr);
      grid-template-rows: auto minmax(0, 1fr);
    }
    .nav {
      flex-direction: row;
      gap: 2px;
      overflow-x: auto;
      overflow-y: hidden;
      scrollbar-width: none;
      padding-block: 0;
      padding-inline: var(--sp-6);
      border-inline-end: 0;
      background: transparent;
      box-shadow: inset 0 -1px 0 var(--line);
    }
    .tab {
      align-items: stretch;
      block-size: 36px;
      padding-inline: 10px;
      border-radius: var(--r-sm) var(--r-sm) 0 0;
    }
    /* labels only: every page's tab fits the row at larger text sizes */
    .tab-icon {
      display: none;
    }
    .tab-label {
      position: relative;
      display: flex;
      align-items: center;
      overflow: visible;
    }
    .tab .mark {
      align-self: center;
    }
    .tab[aria-selected='true'] {
      background: transparent;
      box-shadow: none;
    }
    .tab[aria-selected='true'] .tab-label::after {
      content: '';
      position: absolute;
      inset-inline: 0;
      inset-block-end: 0;
      block-size: 2px;
      border-radius: 2px 2px 0 0;
      background: var(--accent);
    }
  }
  @media (forced-colors: active) {
    .tab[aria-selected='true'] {
      outline: 2px solid Highlight;
      outline-offset: -2px;
    }
    .tab[aria-selected='true'] .tab-label::after {
      forced-color-adjust: none;
      background: Highlight;
    }
    .pane :global(:where(.s-card)) {
      border-color: CanvasText;
    }
  }
</style>
