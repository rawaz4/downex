<script lang="ts" module>
  import type { IconName } from './Icon.svelte';
  import type { TileKind } from './FileIcon.svelte';
  import type { Download } from '../api';

  export interface Command {
    readonly id: string;
    readonly label: string;
    /** The group it is listed under ("Commands", "View", "Go to"). */
    readonly group: string;
    readonly icon?: IconName;
    /** Its keyboard shortcut, as shown ("Ctrl+N"). */
    readonly shortcut?: string;
    /** More words it is found by. */
    readonly keywords?: string;
    /** What it does, in plain words ("Stops the 3 active downloads, keeps their progress"). */
    readonly description?: string;
    readonly disabled?: boolean;
    /** Why it is disabled ("nothing selected"), read with it. */
    readonly disabledReason?: string;
    readonly run: () => void;
  }

  /** A download the palette can find (DESIGN-SYSTEM.md §3.16). */
  export interface PaletteDownload {
    readonly id: number;
    /** Its display name (bidi-escaped). */
    readonly name: string;
    /** Its server, without "www." ("" when unknown). */
    readonly host: string;
    readonly kind: TileKind;
    /** A stream's or site's badge ("HLS", "YouTube"), if any. */
    readonly tag?: string;
    readonly download: Download;
  }

  /** The heading of running downloads (empty query) and of downloads found by a query. */
  export const RUNNING_GROUP = 'Running downloads';
  export const DOWNLOADS_GROUP = 'Downloads';
  /** At most this many running downloads are listed before a query narrows them. */
  export const MAX_RUNNING = 5;

  /**
   * How well `query` matches `text` as a fuzzy subsequence (case-insensitive); -1 when it
   * does not. Consecutive letters and letters at word starts count more, so "pa" ranks
   * "Pause all" above "Open folder at path".
   */
  export function fuzzyScore(query: string, text: string): number {
    const q = query.trim().toLowerCase();
    if (q === '') return 0;
    const t = text.toLowerCase();
    let score = 0;
    let ti = 0;
    let run = 0;
    for (const ch of q) {
      if (ch === ' ') {
        run = 0;
        continue;
      }
      const at = t.indexOf(ch, ti);
      if (at < 0) return -1;
      const wordStart = at === 0 || /[\s:/(.-]/.test(t[at - 1] ?? '');
      run = at === ti && ti > 0 ? run + 1 : 0;
      score += 1 + run * 2 + (wordStart ? 3 : 0) - Math.min(3, at - ti) * 0.25;
      ti = at + 1;
    }
    return score;
  }

  /**
   * The best score of `query` in any of `fields` (a command's label, keywords and description
   * are matched one by one, so that letters of one field do not borrow from another).
   */
  function bestScore(query: string, fields: readonly string[]): number {
    let best = -1;
    for (const f of fields) if (f) best = Math.max(best, fuzzyScore(query, f));
    return best;
  }

  /**
   * A description matches only when every word of the query starts one of its words ("shut"
   * finds "…sleep, shut down…"): a loose subsequence of a sentence would match almost anything.
   */
  function describedScore(query: string, description: string): number {
    if (description === '') return -1;
    const words = description.toLowerCase().split(/[^\p{L}\p{N}]+/u);
    const ok = query
      .trim()
      .toLowerCase()
      .split(/\s+/)
      .every((q) => words.some((w) => w.startsWith(q)));
    return ok ? fuzzyScore(query, description) : -1;
  }

  function commandScore(query: string, c: Command): number {
    return Math.max(fuzzyScore(query, `${c.label} ${c.keywords ?? ''}`), describedScore(query, c.description ?? ''));
  }

  /** The commands matching `query`, best first (in their own order when the query is empty). */
  export function filterCommands(commands: readonly Command[], query: string): Command[] {
    if (query.trim() === '') return [...commands];
    return commands
      .map((c, i) => ({ c, i, s: commandScore(query, c) }))
      .filter((x) => x.s >= 0)
      .sort((a, b) => b.s - a.s || Number(!!a.c.disabled) - Number(!!b.c.disabled) || a.i - b.i)
      .map((x) => x.c);
  }

  /** A row of the palette: a command, or a download. */
  export type PaletteEntry =
    | { readonly type: 'command'; readonly key: string; readonly group: string; readonly command: Command }
    | { readonly type: 'download'; readonly key: string; readonly group: string; readonly item: PaletteDownload };

  /** A download is running: its row shows its progress, and it is listed with an empty query. */
  export function isRunning(d: Pick<Download, 'state'>): boolean {
    return d.state === 'downloading' || d.state === 'probing';
  }

  /**
   * The rows for `query`. Empty: the running downloads (at most MAX_RUNNING), then the
   * commands in their order. Otherwise commands (label, keywords, description) and downloads
   * (name, server) ranked together by their score; on a tie, commands come first. The entries
   * of one group then stay together under one heading, the groups in the order of their best
   * match (so the best match is still the first row).
   */
  export function paletteEntries(commands: readonly Command[], downloads: readonly PaletteDownload[], query: string): PaletteEntry[] {
    const cmd = (c: Command): PaletteEntry => ({ type: 'command', key: `c-${c.id}`, group: c.group, command: c });
    const dl = (d: PaletteDownload, group: string): PaletteEntry => ({ type: 'download', key: `d-${d.id}`, group, item: d });
    if (query.trim() === '') {
      const running = downloads.filter((d) => isRunning(d.download)).slice(0, MAX_RUNNING);
      return [...running.map((d) => dl(d, RUNNING_GROUP)), ...commands.map(cmd)];
    }
    const ranked: { e: PaletteEntry; s: number; tie: number; i: number }[] = [];
    commands.forEach((c, i) => {
      const s = commandScore(query, c);
      if (s >= 0) ranked.push({ e: cmd(c), s, tie: 0, i });
    });
    downloads.forEach((d, i) => {
      const s = bestScore(query, [d.name, d.host]);
      if (s >= 0) ranked.push({ e: dl(d, DOWNLOADS_GROUP), s, tie: 1, i });
    });
    // an unavailable command never comes first among equals: Enter on the best match does something
    const off = (x: { e: PaletteEntry }) => (x.e.type === 'command' && x.e.command.disabled ? 1 : 0);
    const sorted = ranked.sort((a, b) => b.s - a.s || off(a) - off(b) || a.tie - b.tie || a.i - b.i).map((x) => x.e);
    const rank = new Map<string, number>();
    for (const e of sorted) if (!rank.has(e.group)) rank.set(e.group, rank.size);
    return sorted
      .map((e, i) => ({ e, i, g: rank.get(e.group) ?? 0 }))
      .sort((a, b) => a.g - b.g || a.i - b.i)
      .map((x) => x.e);
  }

  /**
   * `text` in runs of letters that a fuzzy `query` matches and letters it does not (for
   * highlighting). No match, or a text whose lower case changes its length: one plain run.
   */
  export function highlightParts(query: string, text: string): { readonly text: string; readonly hit: boolean }[] {
    const q = query.trim().toLowerCase();
    const t = text.toLowerCase();
    const plain = [{ text, hit: false }];
    if (q === '' || t.length !== text.length) return plain;
    const hit: boolean[] = new Array<boolean>(text.length).fill(false);
    let ti = 0;
    for (const ch of q) {
      if (ch === ' ') continue;
      const at = t.indexOf(ch, ti);
      if (at < 0) return plain;
      for (let k = 0; k < ch.length; k++) hit[at + k] = true;
      ti = at + ch.length;
    }
    const parts: { text: string; hit: boolean }[] = [];
    for (let i = 0; i < text.length; i++) {
      const last = parts[parts.length - 1];
      const h = hit[i] === true;
      if (last && last.hit === h) last.text += text[i];
      else parts.push({ text: text[i] ?? '', hit: h });
    }
    return parts;
  }
</script>

<script lang="ts">
  // The command palette (Ctrl+K, DESIGN-SYSTEM.md §3.16): every action of the window and the
  // downloads, found by typing. Commands say what they do in plain words; running downloads
  // show their progress. Enter runs a command or opens a download as its row's Enter does;
  // Ctrl+Enter shows a download in its folder.
  import { tick } from 'svelte';
  import Icon from './Icon.svelte';
  import FileIcon from './FileIcon.svelte';
  import SegmentBar from './SegmentBar.svelte';
  import { trapFocus } from '../actions/focus-trap';
  import { etaSeconds, formatCount, formatDate, formatEta, formatPercent, formatSize, formatSpeed, progressFraction } from '../format';
  import { statusText } from '../display';

  interface Props {
    commands: readonly Command[];
    /** The downloads it can find (running ones are listed before any query). */
    downloads?: readonly PaletteDownload[];
    /** Enter on a download: what a row's Enter does. */
    onopendownload?: (id: number) => void;
    /** Ctrl+Enter on a download: shows it in its folder. */
    onshowfolder?: (id: number) => void;
    onclose: () => void;
    /** Where focus goes when the element it came from is gone. */
    fallbackFocus?: () => HTMLElement | null | undefined;
  }

  let { commands, downloads = [], onopendownload, onshowfolder, onclose, fallbackFocus }: Props = $props();

  const uid = $props.id();
  let query = $state('');
  let active = $state(0);
  let list: HTMLUListElement | undefined = $state();

  const results = $derived(paletteEntries(commands, downloads, query));
  const current = $derived(results[Math.min(active, results.length - 1)]);
  /** The results in runs of one group (paletteEntries keeps each group together). */
  const runs = $derived.by(() => {
    const out: { group: string; items: { e: PaletteEntry; i: number }[] }[] = [];
    results.forEach((e, i) => {
      const last = out[out.length - 1];
      if (last && last.group === e.group) last.items.push({ e, i });
      else out.push({ group: e.group, items: [{ e, i }] });
    });
    return out;
  });
  const counted = $derived.by(() => {
    let dl = 0;
    for (const e of results) if (e.type === 'download') dl++;
    return { downloads: dl, commands: results.length - dl };
  });
  const summary = $derived.by(() => {
    if (results.length === 0) return downloads.length > 0 ? 'No matching commands or downloads' : 'No matching commands';
    const c = counted.commands === 1 ? '1 command' : `${formatCount(counted.commands)} commands`;
    if (counted.downloads === 0) return c;
    const d = counted.downloads === 1 ? '1 download' : `${formatCount(counted.downloads)} downloads`;
    return counted.commands === 0 ? d : `${d} · ${c}`;
  });
  const hasDownloads = $derived(counted.downloads > 0);

  $effect(() => {
    void query;
    active = 0;
  });

  function optionId(e: PaletteEntry): string {
    return e.type === 'command' ? `${uid}-${e.command.id}` : `${uid}-dl-${e.item.id}`;
  }

  async function reveal(): Promise<void> {
    await tick();
    list?.querySelector<HTMLElement>('[aria-selected="true"]')?.scrollIntoView?.({ block: 'nearest' });
  }

  function step(delta: number): void {
    if (results.length === 0) return;
    active = (Math.min(active, results.length - 1) + delta + results.length) % results.length;
    void reveal();
  }

  /** Runs a command or opens a download (`folder`: shows it in its folder instead). */
  function run(e: PaletteEntry | undefined, folder = false): void {
    if (!e) return;
    if (e.type === 'command') {
      const c = e.command;
      if (c.disabled) return;
      onclose();
      // after the palette is gone and focus is back where it was
      void tick().then(() => c.run());
      return;
    }
    const act = folder ? onshowfolder : onopendownload;
    if (!act) return;
    const id = e.item.id;
    onclose();
    void tick().then(() => act(id));
  }

  function onkeydown(e: KeyboardEvent): void {
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault();
        step(1);
        break;
      case 'ArrowUp':
        e.preventDefault();
        step(-1);
        break;
      case 'PageDown':
        e.preventDefault();
        step(5);
        break;
      case 'PageUp':
        e.preventDefault();
        step(-5);
        break;
      case 'Enter':
        e.preventDefault();
        run(current, e.ctrlKey || e.metaKey);
        break;
    }
  }

  /** "Completed · 22 Oct · 2.3 GB": a download that is not running. */
  function restText(d: Download): string {
    const parts = [statusText(d)];
    const when = d.state === 'completed' && d.completedAt > 0 ? d.completedAt : d.createdAt;
    if (when > 0) parts.push(formatDate(when));
    if (d.size >= 0) parts.push(formatSize(d));
    return parts.join(' · ');
  }

  function runningText(d: Download): { percent: string; speed: string; eta: string } {
    const eta = etaSeconds(d);
    return {
      percent: formatPercent(progressFraction(d)),
      speed: d.state === 'downloading' ? formatSpeed(d.speedBps) : statusText(d),
      eta: d.state === 'downloading' && Number.isFinite(eta) ? `${formatEta(eta)} left` : '',
    };
  }

  /** The command's keyboard shortcut as keys ("Ctrl+Shift+P" → Ctrl, Shift, P). */
  function keys(shortcut: string): string[] {
    return shortcut === '+' ? ['+'] : shortcut.split('+').filter((k) => k !== '');
  }
</script>

{#snippet highlighted(text: string)}
  {#each highlightParts(query, text) as part, i (i)}{#if part.hit}<mark>{part.text}</mark>{:else}{part.text}{/if}{/each}
{/snippet}

<div class="backdrop" role="presentation" onpointerdown={(e) => e.target === e.currentTarget && onclose()}>
  <div
    class="palette"
    role="dialog"
    aria-modal="true"
    aria-label="Command palette"
    use:trapFocus={{ onEscape: onclose, fallback: fallbackFocus }}
  >
    <div class="field">
      <span class="glass" aria-hidden="true"><Icon name="search" size={17} /></span>
      <input
        class="query"
        type="text"
        role="combobox"
        aria-label="Search commands"
        aria-expanded="true"
        aria-controls="{uid}-list"
        aria-autocomplete="list"
        aria-activedescendant={current ? optionId(current) : undefined}
        aria-describedby="{uid}-keys"
        autocomplete="off"
        spellcheck="false"
        placeholder="Type a command or a download’s name"
        bind:value={query}
        {onkeydown}
      />
      <span class="scope" aria-hidden="true">Commands and downloads</span>
    </div>
    <ul class="list" id="{uid}-list" role="listbox" aria-label="Commands and downloads" bind:this={list}>
      {#each runs as seg, g (g)}
        <li class="run" role="presentation">
          <ul class="groupbox" role="group" aria-labelledby="{uid}-g-{g}">
            <li class="group" id="{uid}-g-{g}" role="presentation">{seg.group}</li>
            {#each seg.items as { e, i } (e.key)}
              {#if e.type === 'command'}
                {@const c = e.command}
                <!-- svelte-ignore a11y_click_events_have_key_events (the combobox input takes the keys: arrows and Enter) -->
                <li
                  id={optionId(e)}
                  class="option command"
                  class:current={current === e}
                  role="option"
                  aria-selected={current === e}
                  aria-disabled={c.disabled ? 'true' : undefined}
                  aria-describedby={[c.description ? `${uid}-${c.id}-desc` : '', c.disabled ? `${uid}-${c.id}-why` : '']
                    .filter((x) => x !== '')
                    .join(' ') || undefined}
                  onpointermove={() => (active = i)}
                  onclick={() => run(e)}
                >
                  <span class="glyph">{#if c.icon}<Icon name={c.icon} size={16} />{/if}</span>
                  <span class="label">{@render highlighted(c.label)}</span>
                  {#if c.description}<span class="desc" id="{uid}-{c.id}-desc" aria-hidden="true">{c.description}</span>{/if}
                  {#if c.disabled}<span class="why" id="{uid}-{c.id}-why" aria-hidden="true">Unavailable{c.disabledReason ? `: ${c.disabledReason}` : ''}</span>{/if}
                  {#if c.shortcut}<span class="keys" aria-hidden="true">{#each keys(c.shortcut) as k, ki (ki)}<kbd>{k}</kbd>{/each}</span>{/if}
                </li>
              {:else}
                {@const item = e.item}
                {@const d = item.download}
                <!-- svelte-ignore a11y_click_events_have_key_events (the combobox input takes the keys: Enter, Ctrl+Enter) -->
                <li
                  id={optionId(e)}
                  class="option download"
                  class:current={current === e}
                  role="option"
                  aria-selected={current === e}
                  onpointermove={() => (active = i)}
                  onclick={(ev) => run(e, ev.ctrlKey || ev.metaKey)}
                >
                  <span class="glyph"><FileIcon kind={item.kind} size={20} /></span>
                  <span class="label name" dir="auto">{@render highlighted(item.name)}</span>
                  {#if item.tag}<span class="tag" aria-hidden="true">{item.tag}</span>{/if}
                  {#if isRunning(d)}
                    {@const r = runningText(d)}
                    {#if r.percent}<span class="pct tnum">{r.percent}</span>{/if}
                    <SegmentBar download={d} size="mini" />
                    <span class="meta tnum">{r.speed}{r.eta ? ` · ${r.eta}` : ''}</span>
                  {:else}
                    <span class="meta tnum">{restText(d)}</span>
                    {#if item.host}<span class="host" dir="auto">{@render highlighted(item.host)}</span>{/if}
                  {/if}
                </li>
              {/if}
            {/each}
          </ul>
        </li>
      {/each}
    </ul>
    {#if results.length === 0}
      <p class="none" aria-hidden="true">Nothing matches “{query.trim()}”.</p>
    {/if}
    <div class="foot">
      <span class="hints" id="{uid}-keys">
        <span><kbd>↑</kbd><kbd>↓</kbd> move</span>
        <span><kbd>Enter</kbd> {hasDownloads ? 'open' : 'run'}</span>
        {#if hasDownloads && onshowfolder}<span><kbd>Ctrl</kbd><kbd>Enter</kbd> show in folder</span>{/if}
        <span><kbd>Esc</kbd> close</span>
      </span>
      <span class="summary" role="status">{summary}</span>
    </div>
  </div>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 70;
    display: grid;
    justify-items: center;
    align-items: start;
    padding-block-start: 7px;
    padding-inline: var(--sp-6);
    background: var(--dim);
    animation: fade var(--dur-2) ease-out;
  }
  /* 37.5rem: 600 px at the usual text size, wider with larger text (within the window) */
  .palette {
    inline-size: min(37.5rem, 100%);
    max-block-size: min(32rem, calc(100vh - 24px));
    display: grid;
    grid-template-rows: auto minmax(0, 1fr) auto auto;
    border: 1px solid var(--line-strong);
    border-radius: var(--r-xl);
    background: var(--raised);
    box-shadow: var(--shadow-lg);
    overflow: hidden;
    transform-origin: 50% 0;
    animation: open var(--dur-3) var(--ease-out);
  }
  .field {
    display: flex;
    align-items: center;
    gap: 10px;
    block-size: 46px;
    padding-inline: 14px;
    border-block-end: 1px solid var(--line);
  }
  .glass {
    display: grid;
    color: var(--accent);
  }
  .query {
    flex: 1;
    min-inline-size: 0;
    block-size: 100%;
    padding: 0;
    border: 0;
    background: transparent;
    font: inherit;
    font-size: 0.9375rem;
    color: var(--ink);
  }
  .query:focus-visible {
    outline: none;
  }
  .field:focus-within {
    box-shadow: inset 0 -1px 0 var(--accent);
  }
  .query::placeholder {
    color: var(--faint);
  }
  .scope {
    flex: none;
    font-size: var(--type-small);
    color: var(--faint);
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 2px 6px 6px;
    overflow-y: auto;
    overscroll-behavior: contain;
  }
  .run,
  .groupbox {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .group {
    padding-block: 10px 5px;
    padding-inline: 9px;
    font-size: var(--type-caps);
    font-weight: 600;
    letter-spacing: 0.07em;
    text-transform: uppercase;
    color: var(--faint);
  }
  .option {
    display: flex;
    align-items: center;
    gap: 10px;
    min-block-size: 38px;
    padding-inline: 9px;
    border-radius: var(--r-md);
    color: var(--ink);
    cursor: pointer;
    transition: background-color var(--dur-2) ease;
  }
  /* the keyboard's current option (aria-activedescendant): Mint tint plus its ring */
  /* the keyboard's option: the tint and a --focus ring (the tint alone is under 3:1), as in
     ContextMenu; its small print in --muted (--faint is under 4.5:1 on the tint) */
  .option.current {
    background: var(--sel);
    box-shadow: inset 0 0 0 1px var(--sel-line);
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }
  .option.current .desc,
  .option.current .meta,
  .option.current .host {
    color: var(--muted);
  }
  .option[aria-disabled='true'] {
    color: var(--faint);
    cursor: default;
  }
  /* an unavailable command's matched letters do not draw the eye */
  .option[aria-disabled='true'] mark {
    color: inherit;
    font-weight: inherit;
  }
  .glyph {
    flex: none;
    display: grid;
    place-items: center;
    inline-size: 20px;
    color: var(--muted);
  }
  .option.current .glyph {
    color: var(--accent);
  }
  .label {
    flex: none;
    max-inline-size: 60%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 0.84375rem;
    font-weight: 500;
  }
  .download .label {
    flex: 0 1 auto;
    min-inline-size: 6em;
    max-inline-size: none;
  }
  mark {
    background: none;
    color: var(--accent-text);
    font-weight: 650;
  }
  .desc,
  .meta {
    flex: 1 1 auto;
    min-inline-size: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: var(--type-small);
    color: var(--faint);
  }
  /* the name keeps its room; the facts and the server give way first */
  .download .meta {
    flex: 0 40 auto;
  }
  .pct {
    flex: none;
    font-size: var(--type-small);
    font-weight: 600;
    color: var(--accent-text);
  }
  .host {
    flex: 0 20 auto;
    min-inline-size: 0;
    margin-inline-start: auto;
    padding-inline-start: 8px;
    max-inline-size: 30%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: var(--type-small);
    color: var(--faint);
  }
  /* why a disabled option is unavailable: shown on the current option, always its description */
  .why {
    display: none;
    flex: none;
    font-size: var(--type-small);
    color: var(--muted);
  }
  .option.current .why {
    display: inline;
  }
  .option.current[aria-disabled='true'] .desc {
    display: none;
  }
  .keys {
    flex: none;
    display: inline-flex;
    gap: 3px;
    margin-inline-start: auto;
  }
  .none {
    margin: 0;
    padding: 18px 16px 20px;
    text-align: center;
    color: var(--muted);
  }
  .foot {
    display: flex;
    align-items: center;
    gap: 14px;
    min-block-size: 38px;
    padding-inline: 12px;
    border-block-start: 1px solid var(--line);
    background: var(--surface-2);
    font-size: var(--type-small);
    color: var(--muted);
  }
  .hints {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 14px;
    min-inline-size: 0;
  }
  .hints > span {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    white-space: nowrap;
  }
  .summary {
    flex: none;
    margin-inline-start: auto;
    color: var(--faint);
    font-variant-numeric: tabular-nums;
  }
  @keyframes fade {
    from {
      opacity: 0;
    }
  }
  @keyframes open {
    from {
      opacity: 0;
      transform: translateY(-4px);
    }
  }
  @media (forced-colors: active) {
    .option.current {
      outline: 2px solid Highlight;
      outline-offset: -2px;
    }
    mark {
      color: Highlight;
    }
  }
</style>
