<script lang="ts">
  // Settings → Updates (DESIGN-SYSTEM.md §4.9): checking automatically, Check now / Install,
  // and under a collapsed Advanced disclosure (with a summary of what it hides) the address of
  // the signed update description. The disclosure opens by itself when that address is wrong.
  // The layout classes (s-*) are the dialog's.
  import { tick } from 'svelte';
  import { toApiError, type UpdateInfo } from '../api';
  import { formatDateTime } from '../format';
  import { escapeBidi } from '../text';
  import type { SettingsFormErrors, UpdatesForm } from '../validate';
  import Icon from './Icon.svelte';
  import Switch from './Switch.svelte';

  type Form = { -readonly [K in keyof UpdatesForm]: UpdatesForm[K] };

  interface Props {
    form: Form;
    errors: SettingsFormErrors;
    /** The feed address as saved: Check now uses the saved settings. */
    savedFeedUrl: string;
    checkForUpdate?: () => Promise<UpdateInfo>;
    installUpdate?: () => Promise<UpdateInfo>;
    disabled?: boolean;
  }

  let { form = $bindable(), errors, savedFeedUrl, checkForUpdate, installUpdate, disabled = false }: Props = $props();

  const uid = $props.id();

  let info = $state.raw<UpdateInfo | null>(null);
  let busy = $state<'check' | 'install' | null>(null);
  let failure = $state<string | null>(null);
  let confirming = $state(false);
  let installed = $state.raw<UpdateInfo | null>(null);
  /** The Advanced disclosure was opened (it is also open while its address is wrong). */
  let advancedOpen = $state(false);

  const url = $derived(form.updateFeedUrl.trim());
  /** The address in the field is not the saved one: it must be saved before checking. */
  const unsaved = $derived(url !== savedFeedUrl);
  const canCheck = $derived(checkForUpdate !== undefined && savedFeedUrl !== '' && !unsaved && !disabled && busy === null);
  const advancedShown = $derived(advancedOpen || errors.updateFeedUrl !== undefined);
  /** What the closed disclosure hides, in a few words. */
  const advancedSummary = $derived(url === '' ? 'No update address: updates are off' : `Update address: ${hostOf(url)}`);

  function hostOf(address: string): string {
    try {
      return new URL(address).host || address;
    } catch {
      return address;
    }
  }

  async function check() {
    if (!checkForUpdate || !canCheck) return;
    busy = 'check';
    failure = null;
    confirming = false;
    try {
      info = await checkForUpdate();
    } catch (err) {
      failure = toApiError(err).message;
      info = null;
    } finally {
      busy = null;
    }
  }

  async function askInstall() {
    confirming = true;
    await tick();
    document.getElementById(`${uid}-install-now`)?.focus();
  }

  async function install() {
    if (!installUpdate || busy !== null) return;
    busy = 'install';
    failure = null;
    try {
      installed = await installUpdate();
      confirming = false;
    } catch (err) {
      failure = toApiError(err).message;
    } finally {
      busy = null;
    }
  }

  async function cancelInstall() {
    confirming = false;
    await tick();
    document.getElementById(`${uid}-install`)?.focus();
  }
</script>

<section class="s-section" aria-labelledby="{uid}-feed">
  <div class="s-head">
    <h3 class="s-title" id="{uid}-feed">Updates</h3>
    <p class="s-lead">Updates are installed only when their signature is valid.</p>
  </div>
  <div class="s-card">
    <div class="s-row s-switch">
      <Switch
        bind:checked={form.autoCheckUpdates}
        label="Check for updates automatically"
        hint="Every day; Downex tells you when one is available."
        disabled={disabled || url === ''}
      />
    </div>
    <div class="s-row">
      <div class="s-text">
        <span class="s-label">Check for an update now</span>
        <p class="s-hint" id="{uid}-check-hint">
          {#if savedFeedUrl === '' && url === ''}
            Set an update address under Advanced first.
          {:else if unsaved}
            Save the settings first: checking uses the saved address.
          {:else}
            Uses the saved address.
          {/if}
        </p>
      </div>
      <div class="s-control">
        <button type="button" class="btn" onclick={() => void check()} disabled={!canCheck} aria-describedby="{uid}-check-hint">
          {#if busy === 'check'}<span class="spinner" aria-hidden="true"></span>{:else}<Icon name="refresh" size={16} />{/if}
          {busy === 'check' ? 'Checking…' : 'Check now'}
        </button>
      </div>
    </div>
  </div>

  <div class="result" class:empty={!installed && !info && !failure} aria-live="polite">
    {#if installed}
      <p class="note info">
        Downex is installing version <bdi>{escapeBidi(installed.latestVersion)}</bdi> and closes for it. It starts again when the
        installation is done.
      </p>
    {:else if info}
      {#if info.available}
        <div class="found">
          <p class="found-head">
            <span class="pill ok">Update</span>
            <span>
              Version <strong><bdi>{escapeBidi(info.latestVersion)}</bdi></strong> is available (you have
              <bdi>{escapeBidi(info.currentVersion)}</bdi>){info.publishedAt > 0 ? `, published ${formatDateTime(info.publishedAt)}` : ''}.
            </span>
          </p>
          {#if info.notes}
            <p class="notes"><bdi>{escapeBidi(info.notes)}</bdi></p>
          {/if}
          {#if installUpdate}
            {#if confirming}
              <div class="confirm" role="group" aria-labelledby="{uid}-confirm-text">
                <p id="{uid}-confirm-text">
                  Install version <bdi>{escapeBidi(info.latestVersion)}</bdi> now? Running downloads are paused, and Downex
                  closes while the update is installed.
                </p>
                <div class="buttons">
                  <button type="button" class="btn" onclick={() => void cancelInstall()} disabled={busy !== null}>Not now</button>
                  <button type="button" class="btn primary" id="{uid}-install-now" onclick={() => void install()} disabled={busy !== null}>
                    {busy === 'install' ? 'Downloading the update…' : 'Install and restart'}
                  </button>
                </div>
              </div>
            {:else}
              <div class="buttons">
                <button type="button" class="btn primary" id="{uid}-install" onclick={() => void askInstall()} {disabled}>
                  Install update…
                </button>
              </div>
            {/if}
          {/if}
        </div>
      {:else}
        <p class="uptodate">
          <Icon name="check" size={14} />
          <span>
            Downex is up to date (version <bdi>{escapeBidi(info.currentVersion)}</bdi>{info.checkedAt > 0
              ? `, checked ${formatDateTime(info.checkedAt)}`
              : ''}).
          </span>
        </p>
      {/if}
    {/if}
    {#if failure}
      <p class="note fail" role="alert"><bdi>{escapeBidi(failure)}</bdi></p>
    {/if}
  </div>
</section>

<section class="s-section advanced" aria-labelledby="{uid}-advanced">
  <button
    type="button"
    class="disclosure"
    id="{uid}-advanced"
    aria-expanded={advancedShown}
    aria-controls="{uid}-advanced-panel"
    onclick={() => (advancedOpen = !advancedShown)}
  >
    <span class="chev" class:open={advancedShown}><Icon name="chevron-right" size={14} /></span>
    <span class="disclosure-label">Advanced</span>
    {#if !advancedShown}<span class="summary">{advancedSummary}</span>{/if}
  </button>
  <div class="s-card" id="{uid}-advanced-panel" hidden={!advancedShown}>
    {#if advancedShown}
      <div class="s-row stack">
        <div class="field">
          <label for="{uid}-url">Update address</label>
          <input
            id="{uid}-url"
            class="input sm mono"
            bind:value={form.updateFeedUrl}
            placeholder="https://downloads.example.com/downex/update.json"
            inputmode="url"
            spellcheck="false"
            autocomplete="off"
            {disabled}
            aria-invalid={errors.updateFeedUrl ? 'true' : undefined}
            aria-describedby={errors.updateFeedUrl ? `${uid}-updateFeedUrl-error` : `${uid}-url-hint`}
          />
          <p id="{uid}-url-hint" class="hint">
            An https address of a signed update description. Empty = updates are off.
          </p>
          {#if errors.updateFeedUrl}
            <p id="{uid}-updateFeedUrl-error" class="error-text">{errors.updateFeedUrl}</p>
          {/if}
        </div>
      </div>
    {/if}
  </div>
</section>

<style>
  .result {
    display: grid;
    gap: 10px;
  }
  /* nothing to show yet: no gap for it (it stays, as the live region) */
  .result.empty {
    block-size: 0;
    overflow: hidden;
    margin-block-start: -12px;
  }
  .found {
    display: grid;
    gap: 10px;
    padding: 12px 14px;
    border: 1px solid var(--accent-line);
    border-radius: var(--r-lg);
    background: var(--accent-soft);
  }
  .found-head {
    display: flex;
    align-items: baseline;
    gap: 8px;
    color: var(--ink);
  }
  .found-head .pill {
    flex: none;
  }
  .notes {
    max-block-size: 8rem;
    overflow: auto;
    color: var(--ink-2);
    font-size: var(--type-cell);
    white-space: pre-line;
  }
  .uptodate {
    display: flex;
    align-items: center;
    gap: 8px;
    color: var(--ink-2);
    font-size: var(--type-cell);
  }
  .uptodate :global(.icon) {
    color: var(--accent);
  }
  .confirm {
    display: grid;
    gap: 10px;
    padding: 10px 12px;
    border: 1px solid var(--warn-line);
    border-radius: var(--r-md);
    background: var(--warn-soft);
    color: var(--ink);
    font-size: var(--type-cell);
  }
  .buttons {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
  .disclosure {
    display: flex;
    align-items: center;
    gap: 6px;
    inline-size: 100%;
    min-block-size: 32px;
    padding-block: 0;
    padding-inline: 4px 8px;
    border: 0;
    border-radius: var(--r-md);
    background: transparent;
    color: var(--ink);
    text-align: start;
    cursor: pointer;
  }
  .disclosure:hover {
    background: var(--hover);
  }
  .disclosure-label {
    font: 650 var(--type-lead) / 1.3 var(--font-display);
  }
  .summary {
    min-inline-size: 0;
    margin-inline-start: 6px;
    overflow: hidden;
    color: var(--faint);
    font-size: var(--type-small);
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .chev {
    display: grid;
    color: var(--muted);
    transition: transform var(--dur-3) var(--ease-out);
  }
  .chev.open {
    transform: rotate(90deg);
  }
  :global([dir='rtl']) .chev:not(.open) {
    transform: rotate(180deg);
  }
  @media (prefers-reduced-motion: reduce) {
    .chev {
      transition: none;
    }
  }
</style>
