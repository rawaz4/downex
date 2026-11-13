<script lang="ts">
  // Settings → Network (DESIGN-SYSTEM.md §4.9): the proxy (its mode as a segmented control,
  // then the mode's fields), host rules and site logins. The Settings dialog owns the form and
  // the login editor; the layout classes (s-*) are the dialog's.
  import { tick } from 'svelte';
  import type { ProxyMode } from '../api';
  import { RATE_UNITS } from '../format';
  import { escapeBidi } from '../text';
  import {
    applyLoginEditor,
    loginEditorOf,
    newLoginEditor,
    REQUEST_CAP_RANGE_MB,
    type HostRuleDraft,
    type NetworkForm,
    type SettingsFormErrors,
    type SiteLoginDraft,
    type SiteLoginEditor,
    type SiteLoginEditorErrors,
  } from '../validate';
  import Icon from './Icon.svelte';
  import SegmentedControl from './SegmentedControl.svelte';
  import Select from './Select.svelte';

  type Form = { -readonly [K in keyof NetworkForm]: NetworkForm[K] };

  interface Props {
    form: Form;
    errors: SettingsFormErrors;
    /** A proxy password is stored (as loaded). */
    hasProxyPassword: boolean;
    /** A new proxy password, stored with Api.setSecret when the settings are saved. */
    proxyPassword: string;
    /** Remove the stored proxy password when the settings are saved. */
    clearProxyPassword: boolean;
    /** Gives a new editor row its key. */
    newKey: () => number;
    disabled?: boolean;
    /**
     * The site login editor, open or not (null). The Settings dialog owns it: it outlives
     * this page, and Save applies what it holds.
     */
    editor: SiteLoginEditor | null;
    editorError: SiteLoginEditorErrors;
  }

  let {
    form = $bindable(),
    errors,
    hasProxyPassword,
    proxyPassword = $bindable(),
    clearProxyPassword = $bindable(),
    newKey,
    disabled = false,
    editor = $bindable(),
    editorError = $bindable(),
  }: Props = $props();

  const uid = $props.id();

  type Mode = Exclude<ProxyMode, 'unspecified'>;
  const MODES: readonly { value: Mode; label: string; title: string; hint: string }[] = [
    {
      value: 'system',
      label: 'System',
      title: 'Use the system proxy settings',
      hint: 'Uses the system proxy settings, including automatic detection and configuration scripts.',
    },
    { value: 'none', label: 'No proxy', title: 'No proxy', hint: 'Connects directly, without a proxy.' },
    { value: 'manual', label: 'Manual', title: 'Manual proxy', hint: 'Uses the HTTP, HTTPS or SOCKS proxies you enter.' },
    {
      value: 'pac',
      label: 'PAC script',
      title: 'Automatic configuration script',
      hint: 'Asks a proxy auto-configuration (PAC) script, which Downex evaluates, for each address.',
    },
  ];
  const UNITS = RATE_UNITS.map((u) => ({ value: u, label: u }));

  /** The mode shown as chosen ('unspecified' means system). */
  const shownMode = $derived<Mode>(form.proxyMode === 'unspecified' ? 'system' : form.proxyMode);
  const modeHint = $derived(MODES.find((m) => m.value === shownMode)?.hint ?? '');

  async function openEditor(key: number | null) {
    const l = key === null ? undefined : form.siteLogins.find((x) => x.key === key);
    editor = l ? loginEditorOf(l) : newLoginEditor();
    editorError = {};
    await tick();
    document.getElementById(`${uid}-login-pattern`)?.focus();
  }

  async function applyEditor() {
    const e = editor;
    if (!e) return;
    const applied = applyLoginEditor(form.siteLogins, e, newKey);
    if (!applied.ok) {
      editorError = applied.errors;
      await tick();
      document.querySelector<HTMLElement>(`#${uid}-login-editor [aria-invalid="true"]`)?.focus();
      return;
    }
    form.siteLogins = applied.siteLogins;
    const focusKey = e.key;
    editor = null;
    editorError = {};
    await tick();
    document.getElementById(focusKey === null ? `${uid}-login-add` : `${uid}-login-${focusKey}-edit`)?.focus();
  }

  async function cancelEditor() {
    const key = editor?.key ?? null;
    editor = null;
    editorError = {};
    await tick();
    document.getElementById(key === null ? `${uid}-login-add` : `${uid}-login-${key}-edit`)?.focus();
  }

  async function removeLogin(l: SiteLoginDraft) {
    const i = form.siteLogins.indexOf(l);
    form.siteLogins = form.siteLogins.filter((x) => x !== l);
    if (editor?.key === l.key) {
      editor = null;
      editorError = {};
    }
    await tick();
    const next = form.siteLogins[i] ?? form.siteLogins[i - 1];
    document.getElementById(next ? `${uid}-login-${next.key}-edit` : `${uid}-login-add`)?.focus();
  }

  function passwordState(l: SiteLoginDraft): string {
    if (l.password) return 'New password (saved with the settings)';
    if (l.clearPassword) return 'Removed when saved';
    return l.hasPassword ? 'Saved' : 'None';
  }

  async function addRule() {
    const key = newKey();
    form.hostRules = [
      ...form.hostRules,
      { key, pattern: '', connections: '', speedValue: '', speedUnit: 'MB/s', maxRequest: '', maxRequestBytes: 0 },
    ];
    await tick();
    document.getElementById(`${uid}-rule-${key}-pattern`)?.focus();
  }

  function setRule(i: number, patch: Partial<HostRuleDraft>) {
    form.hostRules = form.hostRules.map((r, j) => (j === i ? { ...r, ...patch } : r));
  }

  async function removeRule(i: number) {
    form.hostRules = form.hostRules.filter((_, j) => j !== i);
    await tick();
    const next = form.hostRules[i] ?? form.hostRules[i - 1];
    document.getElementById(next ? `${uid}-rule-${next.key}-pattern` : `${uid}-rule-add`)?.focus();
  }

  function onEditorKeydown(e: KeyboardEvent) {
    // Enter applies the login, not the whole settings form.
    if (e.key === 'Enter') {
      e.preventDefault();
      void applyEditor();
    }
  }

  function err(key: keyof SettingsFormErrors): string | undefined {
    return errors[key] ? `${uid}-${key}-error` : undefined;
  }

  const savedPassword = $derived(
    editor !== null && editor.key !== null && form.siteLogins.find((x) => x.key === editor?.key)?.hasPassword === true,
  );
</script>

<section class="s-section" aria-labelledby="{uid}-proxy">
  <div class="s-head">
    <h3 class="s-title" id="{uid}-proxy">Proxy</h3>
  </div>
  <div class="s-card">
    <div class="s-row stack">
      <div class="s-text">
        <span class="s-label">How downloads connect</span>
        <p class="s-hint">{modeHint}</p>
      </div>
      <div class="proxy-mode">
        <SegmentedControl
          value={shownMode}
          options={MODES}
          legend="How downloads connect"
          fill
          selectWhenNarrow
          onchange={(v) => (form.proxyMode = v)}
          {disabled}
        />
      </div>
      {#if errors.proxy}
        <p id="{uid}-proxy-error" class="error-text s-error">{errors.proxy}</p>
      {/if}
    </div>

    {#if shownMode === 'manual'}
      <div class="s-row stack">
        <div class="pair">
          <div class="field">
            <label for="{uid}-http">HTTP proxy</label>
            <input
              id="{uid}-http"
              class="input sm mono"
              bind:value={form.httpProxy}
              placeholder="proxy.example.com:8080"
              spellcheck="false"
              autocomplete="off"
              {disabled}
              aria-invalid={errors.httpProxy ? 'true' : undefined}
              aria-describedby={err('httpProxy')}
            />
            {#if errors.httpProxy}
              <p id="{uid}-httpProxy-error" class="error-text">{errors.httpProxy}</p>
            {/if}
          </div>
          <div class="field">
            <label for="{uid}-https">HTTPS proxy</label>
            <input
              id="{uid}-https"
              class="input sm mono"
              bind:value={form.httpsProxy}
              placeholder="proxy.example.com:8080"
              spellcheck="false"
              autocomplete="off"
              {disabled}
              aria-invalid={errors.httpsProxy ? 'true' : undefined}
              aria-describedby={err('httpsProxy')}
            />
            {#if errors.httpsProxy}
              <p id="{uid}-httpsProxy-error" class="error-text">{errors.httpsProxy}</p>
            {/if}
          </div>
        </div>
        <div class="field">
          <div class="label-row">
            <label for="{uid}-socks">SOCKS proxy</label>
            <span id="{uid}-socks-hint" class="hint">When set, it is used instead of the HTTP proxies.</span>
          </div>
          <input
            id="{uid}-socks"
            class="input sm mono"
            bind:value={form.socksProxy}
            placeholder="socks5://127.0.0.1:1080"
            spellcheck="false"
            autocomplete="off"
            {disabled}
            aria-invalid={errors.socksProxy ? 'true' : undefined}
            aria-describedby={err('socksProxy') ?? `${uid}-socks-hint`}
          />
          {#if errors.socksProxy}
            <p id="{uid}-socksProxy-error" class="error-text">{errors.socksProxy}</p>
          {/if}
        </div>
        <div class="field">
          <div class="label-row">
            <label for="{uid}-bypass">Connect directly to</label>
            <span id="{uid}-bypass-hint" class="hint">Separated by semicolons; * matches any part, &lt;local&gt; names without dots.</span>
          </div>
          <input
            id="{uid}-bypass"
            class="input sm mono"
            bind:value={form.bypass}
            placeholder="&lt;local&gt;;*.example.com"
            spellcheck="false"
            autocomplete="off"
            {disabled}
            aria-invalid={errors.bypass ? 'true' : undefined}
            aria-describedby={err('bypass') ?? `${uid}-bypass-hint`}
          />
          {#if errors.bypass}
            <p id="{uid}-bypass-error" class="error-text">{errors.bypass}</p>
          {/if}
        </div>
      </div>
    {:else if shownMode === 'pac'}
      <div class="s-row stack">
        <div class="field">
          <label for="{uid}-pac">Script address</label>
          <input
            id="{uid}-pac"
            class="input sm mono"
            bind:value={form.pacUrl}
            placeholder="https://wpad.example.com/proxy.pac"
            spellcheck="false"
            autocomplete="off"
            {disabled}
            aria-invalid={errors.pacUrl ? 'true' : undefined}
            aria-describedby={err('pacUrl')}
          />
          {#if errors.pacUrl}
            <p id="{uid}-pacUrl-error" class="error-text">{errors.pacUrl}</p>
          {/if}
        </div>
      </div>
    {/if}

    {#if shownMode !== 'none'}
      <div class="s-row stack">
        <div class="pair">
          <div class="field">
            <label for="{uid}-proxy-user">Proxy user name</label>
            <input
              id="{uid}-proxy-user"
              class="input sm"
              bind:value={form.proxyUser}
              autocomplete="off"
              spellcheck="false"
              {disabled}
              aria-invalid={errors.proxyUser ? 'true' : undefined}
              aria-describedby={err('proxyUser')}
            />
            {#if errors.proxyUser}
              <p id="{uid}-proxyUser-error" class="error-text">{errors.proxyUser}</p>
            {/if}
          </div>
          <div class="field">
            <label for="{uid}-proxy-password">Proxy password</label>
            <input
              id="{uid}-proxy-password"
              class="input sm"
              type="password"
              bind:value={proxyPassword}
              placeholder={hasProxyPassword && !clearProxyPassword ? 'Saved (type to replace it)' : 'None'}
              autocomplete="new-password"
              {disabled}
              aria-describedby="{uid}-proxy-password-hint"
            />
          </div>
        </div>
        <div class="password-foot">
          <p id="{uid}-proxy-password-hint" class="hint with-icon">
            <Icon name="shield" size={12} />Kept in the system's credential store, never shown.
          </p>
          {#if hasProxyPassword}
            <label class="check small">
              <input type="checkbox" bind:checked={clearProxyPassword} disabled={disabled || proxyPassword !== ''} />
              Remove the saved password
            </label>
          {/if}
        </div>
      </div>
    {/if}
  </div>
</section>

<section class="s-section" aria-labelledby="{uid}-rules">
  <div class="s-head">
    <h3 class="s-title" id="{uid}-rules">Host rules</h3>
    <p class="s-lead" id="{uid}-rules-hint">Limits for the downloads from some hosts. The most specific pattern wins.</p>
  </div>
  {#if form.hostRules.length > 0}
    <div class="s-card">
      <table class="table rules" aria-labelledby="{uid}-rules" aria-describedby="{uid}-rules-hint {uid}-rules-request-hint">
        <thead>
          <tr>
            <th scope="col">Host</th>
            <th scope="col">Connections</th>
            <th scope="col">Speed limit (shared)</th>
            <th scope="col">Max request</th>
            <th scope="col"><span class="sr-only">Actions</span></th>
          </tr>
        </thead>
        <tbody>
          {#each form.hostRules as r, i (r.key)}
            {@const rowErr = errors.hostRuleRows?.[r.key] ?? {}}
            {@const id = `${uid}-rule-${r.key}`}
            <tr>
              <td>
                <input
                  id="{id}-pattern"
                  class="input sm mono"
                  bind:value={r.pattern}
                  placeholder="*.example.com"
                  aria-label="Host of rule {i + 1}"
                  spellcheck="false"
                  autocomplete="off"
                  {disabled}
                  aria-invalid={rowErr.pattern ? 'true' : undefined}
                  aria-describedby={rowErr.pattern ? `${id}-pattern-error` : undefined}
                />
                {#if rowErr.pattern}
                  <p id="{id}-pattern-error" class="error-text">{rowErr.pattern}</p>
                {/if}
              </td>
              <td>
                <input
                  id="{id}-connections"
                  class="input sm narrow tnum"
                  type="number"
                  min="0"
                  max="32"
                  placeholder="Default"
                  aria-label="Connections of rule {i + 1}"
                  value={r.connections}
                  oninput={(e) => setRule(i, { connections: e.currentTarget.value })}
                  {disabled}
                  aria-invalid={rowErr.connections ? 'true' : undefined}
                  aria-describedby={rowErr.connections ? `${id}-connections-error` : undefined}
                />
                {#if rowErr.connections}
                  <p id="{id}-connections-error" class="error-text">{rowErr.connections}</p>
                {/if}
              </td>
              <td>
                <div class="rate">
                  <input
                    class="input sm tnum"
                    inputmode="decimal"
                    bind:value={r.speedValue}
                    placeholder="No limit"
                    aria-label="Speed limit of rule {i + 1}"
                    autocomplete="off"
                    {disabled}
                    aria-invalid={rowErr.speed ? 'true' : undefined}
                    aria-describedby={rowErr.speed ? `${id}-speed-error` : undefined}
                  />
                  <span class="unit">
                    <Select
                      value={r.speedUnit}
                      options={UNITS}
                      size="sm"
                      ariaLabel="Speed limit unit of rule {i + 1}"
                      onchange={(u) => setRule(i, { speedUnit: u })}
                      {disabled}
                    />
                  </span>
                </div>
                {#if rowErr.speed}
                  <p id="{id}-speed-error" class="error-text">{rowErr.speed}</p>
                {/if}
              </td>
              <td>
                <div class="suffixed">
                  <input
                    id="{id}-request"
                    class="input sm narrow tnum"
                    inputmode="decimal"
                    placeholder="No cap"
                    aria-label="Max request of rule {i + 1} (MB)"
                    value={r.maxRequest}
                    oninput={(e) => setRule(i, { maxRequest: e.currentTarget.value })}
                    autocomplete="off"
                    {disabled}
                    aria-invalid={rowErr.maxRequest ? 'true' : undefined}
                    aria-describedby={rowErr.maxRequest ? `${id}-request-error` : undefined}
                  />
                  <span class="suffix" aria-hidden="true">MB</span>
                </div>
                {#if rowErr.maxRequest}
                  <p id="{id}-request-error" class="error-text">{rowErr.maxRequest}</p>
                {/if}
              </td>
              <td class="actions">
                <button
                  type="button"
                  class="icon-btn sm"
                  onclick={() => void removeRule(i)}
                  {disabled}
                  aria-label="Remove the rule for {r.pattern.trim() || `row ${i + 1}`}"
                  title="Remove rule"
                >
                  <Icon name="remove" size={16} />
                </button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    <p class="s-lead" id="{uid}-rules-request-hint">
      Max request: the longest part ({REQUEST_CAP_RANGE_MB.min}–{REQUEST_CAP_RANGE_MB.max} MB) asked of the host in one request;
      empty = no cap. Without a rule of yours, YouTube’s video servers (*.googlevideo.com) get 10.5 MB and 4 connections.
    </p>
  {/if}
  <div class="s-after">
    <button type="button" class="btn" id="{uid}-rule-add" onclick={() => void addRule()} {disabled}>
      <Icon name="add" size={16} />
      Add host rule
    </button>
  </div>
</section>

<section class="s-section" aria-labelledby="{uid}-logins">
  <div class="s-head">
    <h3 class="s-title" id="{uid}-logins">Site logins</h3>
    <p class="s-lead" id="{uid}-logins-hint">
      User names and passwords for servers that ask for them. Passwords are kept in the system's credential store and are
      never shown.
    </p>
  </div>
  {#if form.siteLogins.length > 0}
    <div class="s-card">
      <table class="table logins" aria-labelledby="{uid}-logins" aria-describedby="{uid}-logins-hint">
        <thead>
          <tr>
            <th scope="col">Address</th>
            <th scope="col">User name</th>
            <th scope="col">Password</th>
            <th scope="col"><span class="sr-only">Actions</span></th>
          </tr>
        </thead>
        <tbody>
          {#each form.siteLogins as l (l.key)}
            {@const rowErr = errors.siteLoginRows?.[l.key] ?? {}}
            <tr class:invalid={rowErr.urlPattern || rowErr.username}>
              <td class="address">
                <bdi class="mono">{escapeBidi(l.urlPattern)}</bdi>
                {#if rowErr.urlPattern}<p class="error-text">{rowErr.urlPattern}</p>{/if}
              </td>
              <td>
                <bdi>{escapeBidi(l.username)}</bdi>
                {#if rowErr.username}<p class="error-text">{rowErr.username}</p>{/if}
              </td>
              <td class="pw" class:fresh={l.password !== ''}>{passwordState(l)}</td>
              <td class="actions">
                <button
                  type="button"
                  class="icon-btn sm"
                  id="{uid}-login-{l.key}-edit"
                  onclick={() => void openEditor(l.key)}
                  {disabled}
                  aria-label="Edit the login for {escapeBidi(l.urlPattern)}"
                  title="Edit"
                  data-invalid={rowErr.urlPattern || rowErr.username ? 'true' : undefined}
                >
                  <Icon name="settings" size={16} />
                </button>
                <button
                  type="button"
                  class="icon-btn sm"
                  onclick={() => void removeLogin(l)}
                  {disabled}
                  aria-label="Remove the login for {escapeBidi(l.urlPattern)}"
                  title="Remove"
                >
                  <Icon name="remove" size={16} />
                </button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}

  {#if editor}
    <div class="editor" id="{uid}-login-editor" role="group" aria-labelledby="{uid}-login-editor-title" data-login-editor>
      <h4 id="{uid}-login-editor-title">{editor.key === null ? 'New site login' : 'Edit site login'}</h4>
      <div class="field">
        <div class="label-row">
          <label for="{uid}-login-pattern">Address</label>
          <span id="{uid}-login-pattern-hint" class="hint">An address with * for any part, or a host such as *.example.com.</span>
        </div>
        <input
          id="{uid}-login-pattern"
          class="input sm mono"
          bind:value={editor.urlPattern}
          placeholder="https://example.com/*"
          spellcheck="false"
          autocomplete="off"
          onkeydown={onEditorKeydown}
          aria-invalid={editorError.urlPattern ? 'true' : undefined}
          aria-describedby={editorError.urlPattern ? `${uid}-login-pattern-error` : `${uid}-login-pattern-hint`}
        />
        {#if editorError.urlPattern}
          <p id="{uid}-login-pattern-error" class="error-text">{editorError.urlPattern}</p>
        {/if}
      </div>
      <div class="pair">
        <div class="field">
          <label for="{uid}-login-user">User name</label>
          <input
            id="{uid}-login-user"
            class="input sm"
            data-login-field="user"
            bind:value={editor.username}
            spellcheck="false"
            autocomplete="off"
            onkeydown={onEditorKeydown}
            aria-invalid={editorError.username ? 'true' : undefined}
            aria-describedby={editorError.username ? `${uid}-login-user-error` : undefined}
          />
          {#if editorError.username}
            <p id="{uid}-login-user-error" class="error-text">{editorError.username}</p>
          {/if}
        </div>
        <div class="field">
          <label for="{uid}-login-password">Password</label>
          <input
            id="{uid}-login-password"
            class="input sm"
            type="password"
            bind:value={editor.password}
            autocomplete="new-password"
            placeholder={savedPassword ? 'Saved (type to replace it)' : ''}
            onkeydown={onEditorKeydown}
          />
          {#if savedPassword}
            <label class="check small">
              <input type="checkbox" bind:checked={editor.clearPassword} disabled={editor.password !== ''} />
              Remove the saved password
            </label>
          {/if}
        </div>
      </div>
      <div class="editor-actions">
        <p class="hint">The login is stored when you save the settings.</p>
        <button type="button" class="btn" onclick={() => void cancelEditor()}>Cancel</button>
        <button type="button" class="btn primary" onclick={() => void applyEditor()}>
          {editor.key === null ? 'Add login' : 'Apply'}
        </button>
      </div>
    </div>
  {:else}
    <div class="s-after">
      <button type="button" class="btn" id="{uid}-login-add" onclick={() => void openEditor(null)} {disabled}>
        <Icon name="add" size={16} />
        Add site login
      </button>
    </div>
  {/if}
</section>

<style>
  .pair {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 12px;
    align-items: start;
  }
  .password-foot {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 6px 16px;
  }
  .with-icon {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
  .check.small {
    font-size: var(--type-cell);
    color: var(--ink-2);
  }
  .table {
    inline-size: 100%;
    border-collapse: separate;
    border-spacing: 0;
  }
  .table th {
    block-size: 30px;
    padding-block: 0;
    padding-inline: 8px;
    border-block-end: 1px solid var(--line);
    background: var(--surface-2);
    color: var(--faint);
    font-size: var(--type-caps);
    font-weight: 600;
    letter-spacing: 0.07em;
    text-align: start;
    text-transform: uppercase;
    white-space: nowrap;
  }
  .table th:first-child {
    padding-inline-start: 14px;
    border-start-start-radius: 9px;
  }
  .table th:last-child {
    border-start-end-radius: 9px;
  }
  .table td {
    vertical-align: top;
    padding-block: 7px;
    padding-inline: 8px;
  }
  .table td:first-child {
    padding-inline-start: 14px;
  }
  .table td:last-child {
    padding-inline-end: 10px;
  }
  .table tbody tr + tr td {
    border-block-start: 1px solid var(--line-soft);
  }
  .table .error-text {
    margin-block-start: 4px;
  }
  .rules td:nth-child(2) {
    inline-size: 6.5rem;
  }
  .rules td:nth-child(3) {
    inline-size: 12.5rem;
  }
  .rules td:nth-child(4) {
    inline-size: 7.5rem;
  }
  .narrow {
    inline-size: 100%;
    min-inline-size: 4.5rem;
  }
  .rate {
    display: flex;
    gap: 6px;
  }
  .rate .input {
    flex: 1 1 auto;
    min-inline-size: 0;
  }
  .unit {
    display: block;
    flex: 0 0 5.75rem;
  }
  .suffixed {
    position: relative;
  }
  .suffixed .input {
    padding-inline-end: 36px;
  }
  .suffix {
    position: absolute;
    inset-block: 0;
    inset-inline-end: 10px;
    display: grid;
    place-items: center;
    color: var(--faint);
    font-size: var(--type-small);
    pointer-events: none;
  }
  .logins td {
    block-size: 40px;
    vertical-align: middle;
    color: var(--ink-2);
  }
  .logins .address {
    max-inline-size: 18rem;
    overflow-wrap: anywhere;
  }
  .logins .mono {
    color: var(--ink);
  }
  .pw {
    color: var(--muted);
    font-size: var(--type-cell);
    white-space: nowrap;
  }
  .pw.fresh {
    color: var(--accent-text);
    white-space: normal;
  }
  .actions {
    inline-size: 1%;
    white-space: nowrap;
    text-align: end;
  }
  .invalid td {
    background: var(--danger-soft);
  }
  .editor {
    display: grid;
    gap: 12px;
    padding: 14px;
    border: 1px solid var(--accent-line);
    border-radius: var(--r-lg);
    background: var(--surface-2);
  }
  h4 {
    margin: 0;
    font: 650 var(--type-lead) / 1.3 var(--font-display);
    color: var(--ink);
  }
  .editor-actions {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: 8px;
  }
  .editor-actions .hint {
    margin-inline-end: auto;
  }
  /* the Settings pane's width (SettingsDialog), not the window's */
  @container settings-pane (max-width: 32rem) {
    .pair {
      grid-template-columns: minmax(0, 1fr);
    }
  }
  /* the proxy mode: under its label, the four segments sharing up to 30rem (a Select when narrow) */
  .proxy-mode {
    --seg-max: 30rem;
  }
</style>
