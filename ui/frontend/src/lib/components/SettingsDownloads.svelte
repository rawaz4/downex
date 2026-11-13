<script lang="ts">
  // Settings → Downloads (DESIGN-SYSTEM.md §4.9): how many downloads run at once and with how
  // many connections, the global speed limit (value + unit), the default folder, and the
  // categories (their names, extensions and folders). The Settings dialog owns the values and
  // validates them; the layout classes (s-*) are the dialog's.
  import { tick } from 'svelte';
  import { LIMITS } from '../api';
  import { categoryKind } from '../categories';
  import { joinDir } from '../confirm';
  import { RATE_UNITS, type RateUnit } from '../format';
  import { escapeBidi } from '../text';
  import { parseExtensions, type CategoryDraft, type CategoryDraftErrors, type SettingsFormErrors } from '../validate';
  import FileIcon from './FileIcon.svelte';
  import Icon from './Icon.svelte';
  import Select from './Select.svelte';
  import Switch from './Switch.svelte';

  type Draft = { -readonly [K in keyof CategoryDraft]: CategoryDraft[K] };

  interface Props {
    maxConcurrent: string;
    defaultConnections: string;
    speedValue: string;
    speedUnit: RateUnit;
    defaultDir: string;
    categories: Draft[];
    useCategoryFolders: boolean;
    errors: SettingsFormErrors;
    pickFolder: (initialDir: string) => Promise<string | null>;
    /** Gives a new category row its key. */
    newKey: () => number;
    disabled?: boolean;
  }

  let {
    maxConcurrent = $bindable(),
    defaultConnections = $bindable(),
    speedValue = $bindable(),
    speedUnit = $bindable(),
    defaultDir = $bindable(),
    categories = $bindable(),
    useCategoryFolders = $bindable(),
    errors,
    pickFolder,
    newKey,
    disabled = false,
  }: Props = $props();

  const uid = $props.id();
  const UNITS = RATE_UNITS.map((u) => ({ value: u, label: u }));

  async function browse() {
    const picked = await pickFolder(defaultDir);
    if (picked) defaultDir = picked;
  }

  async function browseCategory(i: number) {
    const c = categories[i];
    if (!c) return;
    const picked = await pickFolder(c.dir || defaultCategoryDir(c));
    if (picked) categories[i] = { ...c, dir: picked };
  }

  async function addCategory() {
    const key = newKey();
    categories.push({ key, name: '', extensions: '', dir: '' });
    await tick();
    document.getElementById(`${uid}-cat-${key}-name`)?.focus();
  }

  async function removeCategory(i: number) {
    categories.splice(i, 1);
    await tick();
    // Keep the keyboard in the list: the next row's name, else the previous, else "Add".
    const target = categories[i] ?? categories[i - 1];
    document.getElementById(target ? `${uid}-cat-${target.key}-name` : `${uid}-cat-add`)?.focus();
  }

  function defaultCategoryDir(c: CategoryDraft): string {
    return joinDir(defaultDir.trim(), c.name.trim() || 'Category');
  }

  /** The tile of a category as it is being typed (its name and extensions decide the kind). */
  function kindOfDraft(c: CategoryDraft) {
    return categoryKind({ name: c.name.trim(), extensions: parseExtensions(c.extensions).extensions });
  }

  function err(key: Exclude<keyof SettingsFormErrors, 'categoryRows'>): string | undefined {
    return errors[key] ? `${uid}-${key}-error` : undefined;
  }

  function rowErrors(key: number): CategoryDraftErrors {
    return errors.categoryRows?.[key] ?? {};
  }
</script>

<section class="s-section" aria-labelledby="{uid}-downloads">
  <div class="s-head">
    <h3 class="s-title" id="{uid}-downloads">Downloads</h3>
  </div>
  <!-- the three limits side by side, then the default folder beside whether categories keep
       folders of their own (0.8.2 fit rule: the page fits the default window with its
       categories); one per row in a narrow pane -->
  <div class="s-card limits">
    <div class="field">
      <label for="{uid}-concurrent">Simultaneous downloads</label>
      <input
        id="{uid}-concurrent"
        class="input sm s-num tnum"
        type="number"
        min={LIMITS.minConcurrent}
        max={LIMITS.maxConcurrent}
        step="1"
        value={maxConcurrent}
        oninput={(e) => (maxConcurrent = e.currentTarget.value)}
        {disabled}
        aria-invalid={errors.maxConcurrent ? 'true' : undefined}
        aria-describedby={err('maxConcurrent') ?? `${uid}-concurrent-hint`}
      />
      <p id="{uid}-concurrent-hint" class="hint">Others wait in the queue ({LIMITS.minConcurrent}–{LIMITS.maxConcurrent}).</p>
      {#if errors.maxConcurrent}
        <p id="{uid}-maxConcurrent-error" class="error-text">{errors.maxConcurrent}</p>
      {/if}
    </div>

    <div class="field">
      <label for="{uid}-connections">Connections per download</label>
      <input
        id="{uid}-connections"
        class="input sm s-num tnum"
        type="number"
        min={LIMITS.minConnections}
        max={LIMITS.maxConnections}
        step="1"
        value={defaultConnections}
        oninput={(e) => (defaultConnections = e.currentTarget.value)}
        {disabled}
        aria-invalid={errors.defaultConnections ? 'true' : undefined}
        aria-describedby={err('defaultConnections') ?? `${uid}-connections-hint`}
      />
      <p id="{uid}-connections-hint" class="hint">Default for new downloads ({LIMITS.minConnections}–{LIMITS.maxConnections}).</p>
      {#if errors.defaultConnections}
        <p id="{uid}-defaultConnections-error" class="error-text">{errors.defaultConnections}</p>
      {/if}
    </div>

    <div class="field">
      <label for="{uid}-speed">Global speed limit</label>
      <div class="rate">
        <input
          id="{uid}-speed"
          class="input sm s-rate tnum"
          inputmode="decimal"
          bind:value={speedValue}
          placeholder="No limit"
          autocomplete="off"
          data-field="globalSpeedLimit"
          {disabled}
          aria-invalid={errors.speed ? 'true' : undefined}
          aria-describedby={err('speed') ?? `${uid}-speed-hint`}
        />
        <span class="s-unit">
          <Select bind:value={speedUnit} options={UNITS} size="sm" ariaLabel="Global speed limit unit" {disabled} />
        </span>
      </div>
      <p id="{uid}-speed-hint" class="hint">Shared by all downloads. Empty = no limit.</p>
      {#if errors.speed}
        <p id="{uid}-speed-error" class="error-text">{errors.speed}</p>
      {/if}
    </div>

    <div class="field wide">
      <div class="label-row">
        <label for="{uid}-dir">Default folder</label>
        <span id="{uid}-dir-hint" class="hint">Where new downloads are saved.</span>
      </div>
      <div class="with-button">
        <span class="s-field-icon">
          <Icon name="folder" size={16} />
          <input
            id="{uid}-dir"
            class="input sm mono"
            bind:value={defaultDir}
            title={escapeBidi(defaultDir)}
            spellcheck="false"
            autocomplete="off"
            {disabled}
            aria-invalid={errors.defaultDir ? 'true' : undefined}
            aria-describedby={err('defaultDir') ?? `${uid}-dir-hint`}
          />
        </span>
        <button type="button" class="btn sm" onclick={browse} {disabled}>Browse…</button>
      </div>
      {#if errors.defaultDir}
        <p id="{uid}-defaultDir-error" class="error-text">{errors.defaultDir}</p>
      {/if}
    </div>

    <div class="wide own-folders">
      <Switch
        bind:checked={useCategoryFolders}
        label="Save downloads into their category's folder"
        hint="Without it, every download goes to the default folder."
        {disabled}
      />
    </div>
  </div>
</section>

<section class="s-section" aria-labelledby="{uid}-categories">
  <!-- the title and lead, with Add category at the end of the head (not a row under the table) -->
  <div class="s-head cat-headline">
    <div class="s-titles">
      <h3 class="s-title" id="{uid}-categories">Categories</h3>
      <p class="s-lead" id="{uid}-categories-hint">A new download joins the first category that lists its file extension.</p>
    </div>
    <div class="s-after">
      <span class="s-hint tnum">{categories.length} of {LIMITS.maxCategories}</span>
      <button
        type="button"
        class="btn sm"
        id="{uid}-cat-add"
        onclick={() => void addCategory()}
        disabled={disabled || categories.length >= LIMITS.maxCategories}
      >
        <Icon name="add" size={14} />
        Add category
      </button>
    </div>
  </div>
  <div class="s-card">
    {#if categories.length > 0}
      <div class="cat-grid">
        <div class="cat-head" aria-hidden="true">
          <span></span>
          <span>Name</span>
          <span>Extensions</span>
          <span>Folder</span>
          <span></span>
        </div>
        <ul class="categories" aria-describedby="{uid}-categories-hint">
          {#each categories as c, i (c.key)}
            {@const rowErr = rowErrors(c.key)}
            {@const id = `${uid}-cat-${c.key}`}
            <li class="category">
              <span class="cat-tile"><FileIcon kind={kindOfDraft(c)} size={20} /></span>
              <div class="cell">
                <label class="sr-only" for="{id}-name">Name</label>
                <input
                  id="{id}-name"
                  class="input sm"
                  bind:value={c.name}
                  autocomplete="off"
                  dir="auto"
                  {disabled}
                  aria-invalid={rowErr.name ? 'true' : undefined}
                  aria-describedby={rowErr.name ? `${id}-name-error` : undefined}
                />
                {#if rowErr.name}
                  <p id="{id}-name-error" class="error-text">{rowErr.name}</p>
                {/if}
              </div>
              <div class="cell">
                <label class="sr-only" for="{id}-exts">Extensions</label>
                <input
                  id="{id}-exts"
                  class="input sm"
                  bind:value={c.extensions}
                  placeholder="mp4 m4v"
                  spellcheck="false"
                  autocomplete="off"
                  {disabled}
                  aria-invalid={rowErr.extensions ? 'true' : undefined}
                  aria-describedby={rowErr.extensions ? `${id}-exts-error` : undefined}
                />
                {#if rowErr.extensions}
                  <p id="{id}-exts-error" class="error-text">{rowErr.extensions}</p>
                {/if}
              </div>
              <div class="cell">
                <label class="sr-only" for="{id}-dir">Folder</label>
                <input
                  id="{id}-dir"
                  class="input sm mono"
                  bind:value={c.dir}
                  placeholder={defaultCategoryDir(c)}
                  spellcheck="false"
                  autocomplete="off"
                  {disabled}
                  aria-invalid={rowErr.dir ? 'true' : undefined}
                  aria-describedby={rowErr.dir ? `${id}-dir-error` : undefined}
                />
                {#if rowErr.dir}
                  <p id="{id}-dir-error" class="error-text">{rowErr.dir}</p>
                {/if}
              </div>
              <div class="cat-actions">
                <button
                  type="button"
                  class="icon-btn sm"
                  onclick={() => void browseCategory(i)}
                  {disabled}
                  aria-label="Choose the folder of {c.name.trim() || 'this category'}"
                  title="Browse…"
                >
                  <Icon name="folder" size={16} />
                </button>
                <button
                  type="button"
                  class="icon-btn sm"
                  onclick={() => void removeCategory(i)}
                  {disabled}
                  aria-label="Remove {c.name.trim() || 'this category'}"
                  title="Remove category"
                >
                  <Icon name="remove" size={16} />
                </button>
              </div>
            </li>
          {/each}
        </ul>
      </div>
    {:else if !disabled}
      <p class="s-row s-empty">No categories: every download is listed under Other and saved in the default folder.</p>
    {/if}
    {#if errors.categories}
      <p id="{uid}-categories-error" class="error-text s-row s-error-row">{errors.categories}</p>
    {/if}
  </div>
</section>

<style>
  .s-num {
    inline-size: 5.5rem;
    text-align: end;
  }
  .s-rate {
    inline-size: 6.5rem;
    text-align: end;
  }
  .s-unit {
    display: block;
    inline-size: 6.25rem;
  }
  /* the limits: three to a row, then the folder and the switch, two to a row */
  .limits {
    grid-template-columns: repeat(6, minmax(0, 1fr));
    align-items: start;
    gap: 14px 20px;
    padding: 12px 14px 14px;
  }
  .limits > .field {
    grid-column: span 2;
    gap: 6px;
    align-content: start;
  }
  .limits > .wide {
    grid-column: span 3;
  }
  .limits .hint {
    color: var(--faint);
    font-size: var(--type-small);
  }
  .rate,
  .with-button {
    display: flex;
    gap: 8px;
    min-inline-size: 0;
  }
  .with-button .s-field-icon {
    flex: 1 1 auto;
  }
  .own-folders {
    align-self: end;
  }
  .s-head.cat-headline {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 8px 16px;
  }
  .s-titles {
    display: grid;
    gap: 3px;
    min-inline-size: 0;
  }
  .cat-headline .s-after {
    flex: none;
  }
  .cat-grid {
    --cat-cols: 20px minmax(6rem, 0.8fr) minmax(0, 1.2fr) minmax(0, 1.5fr) 54px;
    padding-block: 4px 8px;
  }
  .cat-head,
  .category {
    display: grid;
    grid-template-columns: var(--cat-cols);
    gap: 8px;
    padding-inline: 14px 10px;
  }
  .cat-head {
    align-items: end;
    block-size: 26px;
    font-size: var(--type-caps);
    font-weight: 600;
    letter-spacing: 0.07em;
    text-transform: uppercase;
    color: var(--faint);
  }
  .categories {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
  }
  .category {
    align-items: start;
    padding-block: 4px;
  }
  .cat-tile {
    display: grid;
    place-items: center;
    block-size: 30px;
  }
  .cell {
    display: grid;
    gap: 4px;
    min-inline-size: 0;
  }
  .cat-actions {
    display: flex;
    align-items: center;
    gap: 2px;
    block-size: 30px;
  }
  /* the Settings pane's width (SettingsDialog), not the window's */
  /* a narrower pane (a small window, larger text): two to a row, the switch on a row of its own */
  @container settings-pane (max-width: 44rem) {
    .limits > .field {
      grid-column: span 3;
    }
    .limits > .own-folders {
      grid-column: 1 / -1;
    }
    .s-head.cat-headline {
      flex-wrap: wrap;
    }
  }
  @container settings-pane (max-width: 32rem) {
    .limits > .field {
      grid-column: 1 / -1;
    }
    .cat-head {
      display: none;
    }
    .category {
      grid-template-columns: 20px minmax(0, 1fr) auto;
    }
    .category .cell {
      grid-column: 2;
    }
    .cat-actions {
      grid-column: 3;
      grid-row: 1;
    }
  }
</style>
