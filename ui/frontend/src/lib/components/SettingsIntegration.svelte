<script lang="ts">
  // Settings → Integration (DESIGN-SYSTEM.md §4.9): the browser (whether its downloads wait
  // for a confirmation, and how the extension reaches Downex: the target of "Get the browser
  // extension"), the clipboard, and the safety of downloaded files (marking, virus scan).
  // The Settings dialog owns the values; the layout classes (s-*) are the dialog's.
  import type { IntegrationForm, SettingsFormErrors } from '../validate';
  import Icon from './Icon.svelte';
  import Switch from './Switch.svelte';

  type Form = { -readonly [K in keyof IntegrationForm]: IntegrationForm[K] };

  interface Props {
    form: Form;
    /** Ask before saving the downloads the browser hands over (`confirmBrowserDownloads`). */
    confirmBrowserDownloads: boolean;
    errors: SettingsFormErrors;
    /** The native file picker; resolves to null when cancelled. Without it there is no Browse button. */
    pickFile?: (opts: { title: string; initialDir: string }) => Promise<string | null>;
    disabled?: boolean;
    /** The host's system ("mac" words the marking as quarantine, and where the extension is). */
    platform?: 'windows' | 'mac' | 'other';
  }

  let {
    form = $bindable(),
    confirmBrowserDownloads = $bindable(),
    errors,
    pickFile,
    disabled = false,
    platform = 'other',
  }: Props = $props();

  const uid = $props.id();

  const markHint = $derived(
    platform === 'windows'
      ? 'Windows then warns before opening programs from the internet (Mark of the Web).'
      : platform === 'mac'
        ? 'macOS then checks downloaded apps before they first open (quarantine).'
        : 'The file records where it came from, so the system can warn before opening it.',
  );

  /** Where the unpacked extension is, as installed. */
  const extensionDir = $derived(
    platform === 'windows'
      ? 'extension\\chrome-mv3'
      : platform === 'mac'
        ? 'Downex.app/Contents/Resources/extension/chrome-mv3'
        : 'extension/chrome-mv3',
  );

  async function browse() {
    if (!pickFile) return;
    const picked = await pickFile({ title: 'Choose the virus scanner', initialDir: form.avScanCommand.trim() });
    if (picked) form.avScanCommand = picked;
  }

  function err(key: keyof SettingsFormErrors): string | undefined {
    return errors[key] ? `${uid}-${key}-error` : undefined;
  }
</script>

<section class="s-section" aria-labelledby="{uid}-browser">
  <div class="s-head">
    <h3 class="s-title" id="{uid}-browser">Browser</h3>
  </div>
  <div class="s-card">
    <div class="s-row s-switch">
      <Switch
        bind:checked={confirmBrowserDownloads}
        label="Ask before saving downloads from the browser"
        hint="Each download the extension hands over waits until you confirm its folder and name."
        {disabled}
      />
    </div>
    <div class="s-row stack extension" id="{uid}-extension">
      <div class="ext-head">
        <span class="ext-tile" aria-hidden="true"><Icon name="plug" size={18} /></span>
        <div class="s-text">
          <span class="s-label" id="{uid}-extension-title">The Downex browser extension</span>
          <p class="s-hint">
            Downloads you start in Chrome, Edge or Firefox arrive here. Hold Alt while clicking a link to keep one in the
            browser.
          </p>
        </div>
      </div>
      <ol class="steps" aria-labelledby="{uid}-extension-title">
        <li>
          <span class="n" aria-hidden="true">1</span>
          <span>
            Load the extension: open <span class="mono">chrome://extensions</span> (Edge:
            <span class="mono">edge://extensions</span>), turn on Developer mode, choose Load unpacked and pick the
            <span class="mono">{extensionDir}</span> folder that came with Downex.
          </span>
        </li>
        <li>
          <span class="n" aria-hidden="true">2</span>
          <span>
            It reaches Downex through the relay <span class="mono">downex-nmhost</span>, which the installer registers with
            your browsers. If the extension says Downex is not set up, run <span class="mono">downex-nmhost register</span>.
          </span>
        </li>
      </ol>
    </div>
  </div>
</section>

<section class="s-section" aria-labelledby="{uid}-clipboard">
  <div class="s-head">
    <h3 class="s-title" id="{uid}-clipboard">Clipboard</h3>
  </div>
  <div class="s-card">
    <div class="s-row s-switch">
      <Switch
        bind:checked={form.clipboardMonitor}
        label="Offer download links that I copy"
        hint="When you copy the address of a file (a video, an archive, a program…), Downex asks whether to download it. Text that other apps mark as private, such as passwords, is ignored."
        {disabled}
      />
    </div>
  </div>
</section>

<section class="s-section" aria-labelledby="{uid}-safety">
  <div class="s-head">
    <h3 class="s-title" id="{uid}-safety">Safety</h3>
  </div>
  <div class="s-card">
    <div class="s-row s-switch">
      <Switch bind:checked={form.markDownloadedFiles} label="Mark downloaded files as coming from the internet" hint={markHint} {disabled} />
    </div>
    <div class="s-row s-switch">
      <Switch
        bind:checked={form.avScanEnabled}
        label="Scan each finished download with a virus scanner"
        hint="If the scanner reports a problem, Downex tells you and never opens the file by itself."
        {disabled}
      />
    </div>
    <div class="s-row stack">
      <div class="field">
        <label for="{uid}-command">Scanner program</label>
        <div class="with-button">
          <input
            id="{uid}-command"
            class="input sm mono"
            bind:value={form.avScanCommand}
            placeholder="The scanner's command-line program"
            spellcheck="false"
            autocomplete="off"
            {disabled}
            aria-invalid={errors.avScanCommand ? 'true' : undefined}
            aria-describedby={err('avScanCommand')}
          />
          {#if pickFile}
            <button type="button" class="btn" onclick={() => void browse()} {disabled}>Browse…</button>
          {/if}
        </div>
        {#if errors.avScanCommand}
          <p id="{uid}-avScanCommand-error" class="error-text">{errors.avScanCommand}</p>
        {/if}
      </div>
      <div class="field">
        <div class="label-row">
          <label for="{uid}-args">Arguments</label>
          <span id="{uid}-args-hint" class="hint">[File] is replaced by the downloaded file; no shell runs it.</span>
        </div>
        <input
          id="{uid}-args"
          class="input sm mono"
          bind:value={form.avScanArgs}
          placeholder="[File]"
          spellcheck="false"
          autocomplete="off"
          {disabled}
          aria-invalid={errors.avScanArgs ? 'true' : undefined}
          aria-describedby={err('avScanArgs') ?? `${uid}-args-hint`}
        />
        {#if errors.avScanArgs}
          <p id="{uid}-avScanArgs-error" class="error-text">{errors.avScanArgs}</p>
        {/if}
      </div>
    </div>
  </div>
</section>

<style>
  .ext-head {
    display: flex;
    align-items: flex-start;
    gap: 12px;
  }
  .ext-tile {
    display: grid;
    flex: none;
    place-items: center;
    inline-size: 32px;
    block-size: 32px;
    border-radius: var(--r-md);
    background: var(--accent-soft);
    color: var(--accent-text);
  }
  .steps {
    display: grid;
    gap: 8px;
    margin: 0;
    padding: 0;
    padding-inline-start: 44px;
    list-style: none;
    color: var(--ink-2);
    font-size: var(--type-cell);
    line-height: 1.5;
  }
  .steps li {
    display: flex;
    gap: 8px;
  }
  .n {
    display: grid;
    flex: none;
    place-items: center;
    inline-size: 18px;
    block-size: 18px;
    margin-block-start: 1px;
    border: 1px solid var(--line-strong);
    border-radius: 50%;
    color: var(--muted);
    font-size: var(--type-meta);
    font-weight: 600;
    line-height: 1;
  }
  .steps .mono {
    padding-inline: 4px;
    border-radius: var(--r-xs);
    background: var(--surface-2);
    color: var(--ink);
    overflow-wrap: anywhere;
  }
  .with-button {
    display: flex;
    gap: 8px;
    align-items: center;
  }
  .with-button .input {
    flex: 1;
    min-inline-size: 0;
  }
</style>
