<script lang="ts">
  // The queue manager (DESIGN-SYSTEM.md §4.10): the queues in a column like the sidebar's, and
  // the editor of the chosen one: its heading and state with Start/Stop, its settings (name,
  // simultaneous downloads, speed limit with a unit select, what happens when it is done), its
  // schedule and the order of its downloads (arrows, or dragging). The pages share the layout
  // classes (q-*) defined here.
  import { tick } from 'svelte';
  import { MAIN_QUEUE_ID, toApiError, type Capabilities, type Download, type Queue, type QueueSpec } from '../api';
  import { displayName, kindOf } from '../categories';
  import { statusText } from '../display';
  import { RATE_UNITS } from '../format';
  import {
    POST_ACTION_LABELS,
    insertionIndex,
    neighbourTarget,
    postActionChoices,
    queueForm,
    queueNote,
    reorder,
    scheduleSummary,
    validateQueueForm,
    type QueueForm,
    type QueueFormErrors,
  } from '../queues';
  import { escapeBidi } from '../text';
  import Dialog from './Dialog.svelte';
  import FileIcon from './FileIcon.svelte';
  import Icon from './Icon.svelte';
  import ScheduleEditor from './ScheduleEditor.svelte';
  import Select from './Select.svelte';
  import StatusMark, { markOfState } from './StatusMark.svelte';

  type Form = { -readonly [K in keyof QueueForm]: QueueForm[K] };

  interface Props {
    /** Every queue, the main queue first (live: events update them). */
    queues: readonly Queue[];
    /** The queue shown first (default: the main queue). */
    initial?: number | null;
    capabilities: Pick<Capabilities, 'hibernate' | 'wakeTimers'>;
    /** A download of the list, for the queue's order. */
    downloadOf: (id: number) => Download | undefined;
    oncreate: (spec: QueueSpec) => Promise<Queue>;
    onupdate: (id: number, spec: QueueSpec) => Promise<Queue>;
    ondelete: (id: number) => Promise<void>;
    onstart: (id: number) => Promise<Queue>;
    onstop: (id: number) => Promise<Queue>;
    /** Moves downloads into a queue at a position among its other downloads (-1 = the end). */
    onmove: (ids: readonly number[], queueId: number, position: number) => Promise<Queue>;
    onclose: () => void;
    /** downexd is unreachable: nothing can be changed. */
    stale?: boolean;
    fallbackFocus?: () => HTMLElement | null | undefined;
  }

  let {
    queues,
    initial = null,
    capabilities,
    downloadOf,
    oncreate,
    onupdate,
    ondelete,
    onstart,
    onstop,
    onmove,
    onclose,
    stale = false,
    fallbackFocus,
  }: Props = $props();

  const uid = $props.id();
  /** Rows listed in the order editor; more are summarised. */
  const MAX_ROWS = 200;
  const DRAG_TYPE = 'application/x-downex-download';
  const UNITS = RATE_UNITS.map((u) => ({ value: u, label: u }));

  // svelte-ignore state_referenced_locally
  const first = queues.find((q) => q.id === initial) ?? queues.find((q) => q.isMain) ?? queues[0] ?? null;
  /** The queue being edited, or null for a new one (not created yet). */
  let selected = $state<number | null>(first?.id ?? null);
  let form = $state<Form>({ ...queueForm(first) });
  let attempted = $state(false);
  let busy = $state<'save' | 'delete' | 'run' | 'move' | null>(null);
  let failure = $state<string | null>(null);
  let confirmDelete = $state(false);
  let dragId = $state<number | null>(null);
  /** Where a dragged row would go: before this download (null = the end); undefined = nowhere. */
  let dropBefore = $state<number | null | undefined>(undefined);
  let body: HTMLDivElement | undefined = $state();

  const queue = $derived(selected === null ? null : (queues.find((q) => q.id === selected) ?? null));
  const others = $derived(queues.filter((q) => q.id !== selected));
  const result = $derived(validateQueueForm(form, others, capabilities, queue));
  const errors: QueueFormErrors = $derived(attempted && !result.ok ? result.errors : {});
  const choices = $derived(
    postActionChoices(capabilities, queue?.postAction ?? 'none').map((a) => ({ value: a, label: POST_ACTION_LABELS[a] })),
  );
  const order = $derived(queue?.downloadIds ?? []);
  /** The queue's downloads that still matter for its order (completed ones do not). */
  const pendingIds = $derived(
    order.filter((id) => {
      const d = downloadOf(id);
      return d !== undefined && d.state !== 'completed';
    }),
  );
  const shownIds = $derived(pendingIds.slice(0, MAX_ROWS));
  const locked = $derived(stale || busy !== null);

  // The queue being edited was deleted elsewhere: show the main queue.
  $effect(() => {
    if (selected !== null && queue === null && busy === null) select(queues.find((q) => q.isMain)?.id ?? queues[0]?.id ?? null);
  });

  /** The downloads of a queue that wait (completed ones are not counted). */
  function pendingCount(q: Queue): number {
    let n = 0;
    for (const id of q.downloadIds) {
      const d = downloadOf(id);
      if (d && d.state !== 'completed') n++;
    }
    return n;
  }

  function select(id: number | null) {
    selected = id;
    form = { ...queueForm(id === null ? null : (queues.find((q) => q.id === id) ?? null)) };
    attempted = false;
    failure = null;
    confirmDelete = false;
  }

  async function newQueue() {
    select(null);
    await tick();
    document.getElementById(`${uid}-name`)?.focus();
  }

  async function run<T>(kind: NonNullable<typeof busy>, fn: () => Promise<T>): Promise<T | undefined> {
    busy = kind;
    failure = null;
    try {
      return await fn();
    } catch (err) {
      failure = toApiError(err).message;
      return undefined;
    } finally {
      busy = null;
    }
  }

  async function onsubmit() {
    if (locked) return;
    attempted = true;
    if (!result.ok) {
      await tick();
      body?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
      return;
    }
    const spec = result.spec;
    const id = selected;
    const saved = await run('save', () => (id === null ? oncreate(spec) : onupdate(id, spec)));
    if (saved) {
      selected = saved.id;
      form = { ...queueForm(saved) };
      attempted = false;
    }
  }

  async function toggleRunning() {
    const q = queue;
    if (!q || locked) return;
    await run('run', () => (q.running ? onstop(q.id) : onstart(q.id)));
  }

  async function remove() {
    const q = queue;
    if (!q || q.isMain || locked) return;
    if (!confirmDelete) {
      confirmDelete = true;
      return;
    }
    const ok = await run('delete', async () => {
      await ondelete(q.id);
      return true;
    });
    confirmDelete = false;
    if (ok) select(queues.find((x) => x.isMain)?.id ?? MAIN_QUEUE_ID);
  }

  /** Putting `id` before `before` (null = the end of the queue) changes the order. */
  function changesOrder(id: number, before: number | null): boolean {
    if (id === before) return false;
    const next = reorder(order, [id], before);
    return next.some((x, i) => x !== order[i]);
  }

  /** Puts `id` before `before` (null = the end of the queue). */
  async function moveBefore(id: number, before: number | null) {
    const q = queue;
    if (!q || locked || !changesOrder(id, before)) return;
    const position = insertionIndex(q.downloadIds, [id], before);
    await run('move', () => onmove([id], q.id, position));
  }

  async function step(id: number, delta: -1 | 1) {
    const target = neighbourTarget(order, shownIds, id, delta);
    if (target === undefined) return;
    await moveBefore(id, target);
    await tick();
    // Keep the keyboard on the row that moved (its other arrow once it reached an end).
    const same = document.getElementById(`${uid}-row-${id}-${delta < 0 ? 'up' : 'down'}`) as HTMLButtonElement | null;
    const other = document.getElementById(`${uid}-row-${id}-${delta < 0 ? 'down' : 'up'}`) as HTMLButtonElement | null;
    (same && !same.disabled ? same : other)?.focus();
  }

  /**
   * Where a row dropped just below the row of `id` goes: before the next download that
   * waits in the queue other than the dragged one `moved` (completed ones in between are
   * not listed), or null for the end. `id` is never `moved` (see targetOf).
   */
  function after(id: number, moved: number): number | null {
    const rest = pendingIds.filter((x) => x !== moved);
    const i = rest.indexOf(id);
    return i < 0 ? id : (rest[i + 1] ?? null);
  }

  function onDragStart(e: DragEvent, id: number) {
    if (locked) {
      e.preventDefault();
      return;
    }
    dragId = id;
    if (e.dataTransfer) {
      e.dataTransfer.setData(DRAG_TYPE, String(id));
      e.dataTransfer.effectAllowed = 'move';
    }
  }

  /**
   * The download that `moved`, dropped on the row of `id`, goes before (null = the end):
   * that row in its upper half, the next one in its lower half. A row dropped onto itself
   * stays where it is (its own id: no move).
   */
  function targetOf(e: DragEvent, id: number, moved: number | null): number | null {
    if (moved === null || id === moved) return id;
    const el = e.currentTarget instanceof HTMLElement ? e.currentTarget : null;
    const rect = el?.getBoundingClientRect();
    const lowerHalf = rect !== undefined && rect.height > 0 && e.clientY > rect.top + rect.height / 2;
    return lowerHalf ? after(id, moved) : id;
  }

  function draggedId(e: DragEvent): number | null {
    if (dragId !== null) return dragId;
    const raw = e.dataTransfer?.getData(DRAG_TYPE) ?? '';
    return /^\d+$/.test(raw) ? Number(raw) : null;
  }

  function onDragOver(e: DragEvent, id: number | null) {
    if (dragId === null && !(e.dataTransfer?.types ?? []).includes(DRAG_TYPE)) return;
    e.preventDefault();
    if (e.dataTransfer) e.dataTransfer.dropEffect = 'move';
    const moved = draggedId(e);
    const before = id === null ? null : targetOf(e, id, moved);
    // Nothing is marked where the row would stay in place.
    dropBefore = moved !== null && !changesOrder(moved, before) ? undefined : before;
  }

  function onDrop(e: DragEvent, id: number | null) {
    const moved = draggedId(e);
    if (moved === null) return;
    e.preventDefault();
    const before = id === null ? null : targetOf(e, id, moved);
    dragId = null;
    dropBefore = undefined;
    void moveBefore(moved, before);
  }

  function onDragEnd() {
    dragId = null;
    dropBefore = undefined;
  }

  function err(key: keyof QueueFormErrors): string | undefined {
    return errors[key] ? `${uid}-${key}-error` : undefined;
  }
</script>

<Dialog title="Queues" {onclose} {onsubmit} busy={busy !== null} {fallbackFocus} width="72rem" fixedHeight="48rem" flush>
  <div class="queues-shell">
    <nav class="list" aria-label="Queues">
      <p class="caps list-head" aria-hidden="true">Queues</p>
      <ul>
        {#each queues as q (q.id)}
          {@const note = queueNote(q)}
          <li>
            <button type="button" class="queue" class:active={selected === q.id} aria-pressed={selected === q.id} onclick={() => select(q.id)}>
              <span class="qicon" class:on={q.running}><Icon name="queue" size={16} /></span>
              <span class="qname" dir="auto">{escapeBidi(q.name)}</span>
              {#if note}<span class="qnote">{note}</span>{/if}
              {#if !q.running}<span class="sr-only">stopped</span>{/if}
              <span class="qcount tnum">{pendingCount(q)}</span>
            </button>
          </li>
        {/each}
        {#if selected === null}
          <li>
            <button type="button" class="queue active" aria-pressed="true">
              <span class="qicon"><Icon name="add" size={16} /></span>
              <span class="qname">New queue</span>
            </button>
          </li>
        {/if}
      </ul>
      <button type="button" class="btn new" onclick={() => void newQueue()} disabled={locked}>
        <Icon name="add" size={16} />
        New queue
      </button>
    </nav>

    <div class="editor" bind:this={body}>
      <section class="head" aria-labelledby="{uid}-heading">
        <div class="head-line">
          <h3 id="{uid}-heading" dir="auto">{queue ? escapeBidi(queue.name) : 'New queue'}</h3>
          {#if queue}
            <span class="pill" class:ok={queue.running} class:done={!queue.running}>
              {#if queue.running}<span class="dot" aria-hidden="true"></span>{/if}{queue.running ? 'Running' : 'Stopped'}
            </span>
            <button type="button" class="btn sm run" onclick={() => void toggleRunning()} disabled={locked}>
              <Icon name={queue.running ? 'pause' : 'resume'} size={14} />
              {busy === 'run' ? (queue.running ? 'Stopping…' : 'Starting…') : queue.running ? 'Stop' : 'Start'}
            </button>
          {/if}
        </div>
        {#if queue}
          <p class="q-hint summary">
            <Icon name="clock" size={13} />{scheduleSummary(queue.schedule)}{queue.isMain ? ' · The main queue cannot be deleted.' : ''}
          </p>
        {:else}
          <p class="q-hint summary">A new queue starts stopped; add downloads to it from the list or when you add them.</p>
        {/if}
      </section>

      <section class="q-section settings" aria-labelledby="{uid}-settings">
        <h4 class="q-title" id="{uid}-settings">Settings</h4>
        <div class="q-card fields">
          <div class="field">
            <label for="{uid}-name">Name</label>
            <input
              id="{uid}-name"
              class="input sm"
              bind:value={form.name}
              autocomplete="off"
              dir="auto"
              data-autofocus
              disabled={stale}
              aria-invalid={errors.name ? 'true' : undefined}
              aria-describedby={err('name')}
            />
            {#if errors.name}
              <p id="{uid}-name-error" class="error-text">{errors.name}</p>
            {/if}
          </div>
          <div class="field">
            <div class="label-row">
              <label for="{uid}-concurrent">Simultaneous downloads</label>
              <span id="{uid}-concurrent-hint" class="hint">0 = the global setting</span>
            </div>
            <input
              id="{uid}-concurrent"
              class="input sm tnum"
              type="number"
              min="0"
              max="16"
              step="1"
              value={form.maxConcurrent}
              oninput={(e) => (form.maxConcurrent = e.currentTarget.value)}
              disabled={stale}
              aria-invalid={errors.maxConcurrent ? 'true' : undefined}
              aria-describedby={err('maxConcurrent') ?? `${uid}-concurrent-hint`}
            />
            {#if errors.maxConcurrent}
              <p id="{uid}-maxConcurrent-error" class="error-text">{errors.maxConcurrent}</p>
            {/if}
          </div>
          <div class="field">
            <label for="{uid}-speed">Speed limit of the queue</label>
            <div class="rate">
              <input
                id="{uid}-speed"
                class="input sm tnum"
                inputmode="decimal"
                bind:value={form.speedValue}
                placeholder="No limit"
                autocomplete="off"
                disabled={stale}
                aria-invalid={errors.speed ? 'true' : undefined}
                aria-describedby={err('speed')}
              />
              <span class="unit">
                <Select bind:value={form.speedUnit} options={UNITS} size="sm" ariaLabel="Queue speed limit unit" disabled={stale} />
              </span>
            </div>
            {#if errors.speed}
              <p id="{uid}-speed-error" class="error-text">{errors.speed}</p>
            {/if}
          </div>
          <div class="field">
            <div class="label-row">
              <label for="{uid}-after">When the queue is done</label>
              <span id="{uid}-after-hint" class="hint">Can be cancelled for 60 s</span>
            </div>
            <Select
              id="{uid}-after"
              bind:value={form.postAction}
              options={choices}
              size="sm"
              icon="power"
              disabled={stale}
              invalid={errors.postAction !== undefined}
              ariaDescribedby={err('postAction') ?? `${uid}-after-hint`}
            />
            {#if errors.postAction}
              <p id="{uid}-postAction-error" class="error-text">{errors.postAction}</p>
            {/if}
          </div>
        </div>
      </section>

      <ScheduleEditor bind:form {errors} wakeTimers={capabilities.wakeTimers} disabled={stale} />

      {#if queue}
        <section class="q-section order" aria-labelledby="{uid}-order">
          <div class="order-head">
            <h4 class="q-title" id="{uid}-order">Order</h4>
            {#if shownIds.length > 0}
              <p class="q-hint" id="{uid}-order-hint">Drag downloads, or use the arrows, to change the order in which they start.</p>
            {/if}
          </div>
          {#if shownIds.length === 0}
            <p class="q-card empty">No downloads wait in this queue.</p>
          {:else}
            <ol class="q-card downloads" aria-labelledby="{uid}-order" aria-describedby="{uid}-order-hint">
              {#each shownIds as id, i (id)}
                {@const d = downloadOf(id)}
                <li
                  class="dl"
                  class:dragging={dragId === id}
                  class:drop-before={dropBefore === id}
                  draggable={locked ? 'false' : 'true'}
                  ondragstart={(e) => onDragStart(e, id)}
                  ondragover={(e) => onDragOver(e, id)}
                  ondrop={(e) => onDrop(e, id)}
                  ondragend={onDragEnd}
                >
                  <span class="grip" aria-hidden="true"><svg viewBox="0 0 8 14" width="8" height="14"><path d="M2 2h.01M6 2h.01M2 7h.01M6 7h.01M2 12h.01M6 12h.01" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" /></svg></span>
                  <span class="pos tnum">{i + 1}</span>
                  {#if d}<FileIcon kind={kindOf(d)} size={16} />{/if}
                  <bdi class="dname" title={d ? displayName(d) : undefined}>{d ? displayName(d) : `#${id}`}</bdi>
                  {#if d}
                    <span class="dstate st-{markOfState(d.state)}"><StatusMark kind={markOfState(d.state)} size={12} />{statusText(d)}</span>
                  {/if}
                  <span class="moves">
                    <button
                      type="button"
                      class="icon-btn sm"
                      id="{uid}-row-{id}-up"
                      aria-label="Move {d ? displayName(d) : `#${id}`} up"
                      title="Move up"
                      disabled={locked || i === 0}
                      onclick={() => void step(id, -1)}
                    >
                      <Icon name="chevron-up" size={14} />
                    </button>
                    <button
                      type="button"
                      class="icon-btn sm"
                      id="{uid}-row-{id}-down"
                      aria-label="Move {d ? displayName(d) : `#${id}`} down"
                      title="Move down"
                      disabled={locked || i === shownIds.length - 1}
                      onclick={() => void step(id, 1)}
                    >
                      <Icon name="chevron-down" size={14} />
                    </button>
                  </span>
                </li>
              {/each}
              <li
                class="end"
                class:drop-before={dropBefore === null}
                aria-hidden="true"
                ondragover={(e) => onDragOver(e, null)}
                ondrop={(e) => onDrop(e, null)}
              ></li>
            </ol>
            {#if pendingIds.length > shownIds.length}
              <p class="q-hint">And {pendingIds.length - shownIds.length} more, after these.</p>
            {/if}
          {/if}
        </section>
      {/if}

      {#if confirmDelete && queue}
        <div class="note fail confirm" role="alert">
          <p>Delete <bdi>{escapeBidi(queue.name)}</bdi>? Its downloads move to the end of the main queue.</p>
        </div>
      {/if}
    </div>
  </div>

  {#snippet footerStart()}
    {#if queue && !queue.isMain}
      <div class="delete-group">
        <button type="button" class="btn danger-text" onclick={() => void remove()} disabled={locked}>
          <Icon name="remove" size={16} />
          {busy === 'delete' ? 'Deleting…' : confirmDelete ? 'Delete queue now' : 'Delete queue…'}
        </button>
        {#if confirmDelete}
          <button type="button" class="btn" onclick={() => (confirmDelete = false)} disabled={busy !== null}>Keep it</button>
        {/if}
      </div>
    {/if}
    {#if attempted && !result.ok}
      <p class="foot-msg" role="alert"><Icon name="alert" size={14} />Some fields need attention.</p>
    {/if}
    {#if failure}
      <p class="foot-msg" role="alert"><Icon name="alert" size={14} /><bdi>{escapeBidi(failure)}</bdi></p>
    {/if}
  {/snippet}

  {#snippet footer()}
    <button type="button" class="btn" onclick={onclose} disabled={busy !== null}>Close</button>
    <button type="submit" class="btn primary" disabled={locked}>
      {busy === 'save' ? 'Saving…' : queue ? 'Save queue' : 'Create queue'}
    </button>
  {/snippet}
</Dialog>

<style>
  /* The dialog's body holds only this shell: its one row fills the fixed height, so that the
     list column runs from the header to the footer and only the editor scrolls. */
  :global(.body.flush:has(> .queues-shell)) {
    grid-template-rows: minmax(0, 1fr);
    overflow: hidden;
  }
  .queues-shell {
    display: grid;
    grid-template-columns: 14.5rem minmax(0, 1fr);
    min-block-size: 0;
    border-block-start: 1px solid var(--line);
  }

  /* ---- the list column, like the sidebar ---- */
  .list {
    display: flex;
    flex-direction: column;
    gap: 6px;
    min-block-size: 0;
    padding: 10px 8px;
    border-inline-end: 1px solid var(--line);
    background: var(--surface-2);
  }
  .list-head {
    padding: 4px 10px 2px;
  }
  .list ul {
    display: grid;
    gap: 2px;
    flex: 0 1 auto;
    min-block-size: 0;
    overflow-y: auto;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .queue {
    display: flex;
    align-items: center;
    gap: 9px;
    inline-size: 100%;
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
    transition: background-color var(--dur-2) ease;
  }
  .queue:hover:not(.active) {
    background: var(--hover);
    color: var(--ink);
  }
  .queue.active {
    background: var(--sel);
    box-shadow: inset 0 0 0 1px var(--sel-line);
    color: var(--ink);
    font-weight: 600;
  }
  .queue:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }
  .qicon {
    display: grid;
    flex: none;
    color: var(--muted);
  }
  .qicon.on,
  .queue.active .qicon {
    color: var(--accent);
  }
  .qname {
    flex: 1 1 auto;
    min-inline-size: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .qnote {
    flex: none;
    color: var(--faint);
    font-size: var(--type-meta);
    font-weight: 400;
  }
  .qcount {
    flex: none;
    min-inline-size: 1.5ch;
    color: var(--faint);
    font-size: var(--type-small);
    font-weight: 400;
    text-align: end;
  }
  .queue.active .qcount {
    color: var(--ink-2);
    font-weight: 600;
  }
  /* small print on the selection's tint is --muted (§2.8): --faint is 4.43:1 there in dark */
  .queue.active .qnote {
    color: var(--muted);
  }
  .btn.new {
    align-self: start;
    margin-block-start: 4px;
    margin-inline-start: 2px;
  }

  /* ---- the editor: the one part that scrolls (the order list scrolls with it) ---- */
  .editor {
    container: queue-editor / inline-size;
    display: grid;
    align-content: start;
    gap: 20px;
    min-block-size: 0;
    overflow-y: auto;
    scrollbar-gutter: stable;
    padding-block: 18px 24px;
    padding-inline: 24px 18px;
  }
  .head {
    display: grid;
    gap: 4px;
  }
  .head-line {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  h3 {
    min-inline-size: 0;
    overflow-wrap: anywhere;
    font: 650 var(--type-page) / 1.2 var(--font-display);
    letter-spacing: -0.02em;
    color: var(--ink);
  }
  .head-line .pill {
    flex: none;
  }
  .run {
    flex: none;
    margin-inline-start: auto;
  }
  .summary {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .summary :global(.icon) {
    flex: none;
  }

  /* shared with ScheduleEditor (q-*) */
  .editor :global(:where(.q-section)) {
    display: grid;
    gap: 8px;
    min-inline-size: 0;
  }
  .editor :global(:where(.q-title)) {
    margin: 0;
    font: 650 var(--type-lead) / 1.3 var(--font-display);
    color: var(--ink);
  }
  .editor :global(:where(.q-card)) {
    display: grid;
    min-inline-size: 0;
    margin: 0;
    border: 1px solid var(--line);
    border-radius: var(--r-lg);
    background: var(--surface);
  }
  .editor :global(:where(.q-row)) {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: center;
    gap: 6px 20px;
    min-block-size: 48px;
    padding-block: 9px;
    padding-inline: 14px;
  }
  .editor :global(:where(.q-row + .q-row)) {
    border-block-start: 1px solid var(--line-soft);
  }
  .editor :global(:where(.q-row.q-switch)) {
    display: block;
    padding-block: 2px;
  }
  .editor :global(:where(.q-text)) {
    display: grid;
    gap: 2px;
    min-inline-size: 0;
  }
  .editor :global(:where(.q-label)) {
    color: var(--ink);
    font-size: var(--type-body);
    font-weight: 500;
  }
  .editor :global(:where(.q-hint)) {
    color: var(--faint);
    font-size: var(--type-small);
  }
  .editor :global(:where(.q-error)) {
    grid-column: 1 / -1;
  }

  .fields {
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 14px 16px;
    padding: 14px;
  }
  .fields .field {
    gap: 6px;
    align-content: start;
  }
  .fields label {
    font-size: var(--type-cell);
    white-space: nowrap;
  }
  /* a narrow column puts the hint under its label rather than squeezing both */
  .fields .label-row {
    flex-wrap: wrap;
    row-gap: 0;
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
    flex: 0 0 6rem;
  }

  /* ---- the order ---- */
  .order-head {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    justify-content: space-between;
    gap: 2px 16px;
  }
  .empty {
    padding: 14px;
    color: var(--muted);
    font-size: var(--type-cell);
  }
  /* no scroller of its own (it would scroll inside the editor): it clips its rows' hover to its corners */
  .downloads {
    overflow: clip;
    padding: 0;
    list-style: none;
  }
  .dl {
    display: flex;
    align-items: center;
    gap: 8px;
    min-block-size: 34px;
    padding-inline: 8px 6px;
    border-block-start: 2px solid transparent;
    cursor: grab;
  }
  .dl + .dl {
    border-block-start: 1px solid var(--line-soft);
    padding-block-start: 1px;
  }
  .dl:hover {
    background: var(--hover);
  }
  .dl.dragging {
    opacity: 0.5;
  }
  .drop-before {
    box-shadow: inset 0 2px 0 var(--accent);
  }
  .end {
    min-block-size: 8px;
  }
  .grip {
    display: grid;
    flex: none;
    place-items: center;
    inline-size: 12px;
    color: var(--faint);
  }
  .pos {
    flex: none;
    inline-size: 2.5ch;
    color: var(--faint);
    font-size: var(--type-small);
    text-align: end;
  }
  .dname {
    flex: 1 1 auto;
    min-inline-size: 0;
    overflow: hidden;
    color: var(--ink);
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .dstate {
    display: inline-flex;
    flex: none;
    align-items: center;
    gap: 6px;
    color: var(--muted);
    font-size: var(--type-cell);
  }
  .dstate.st-downloading {
    color: var(--accent-text);
    font-weight: 600;
  }
  .dstate.st-paused {
    color: var(--warn);
    font-weight: 600;
  }
  .dstate.st-failed {
    color: var(--danger);
    font-weight: 600;
  }
  .dstate.st-awaiting {
    color: var(--info);
    font-weight: 600;
  }
  .moves {
    display: flex;
    flex: none;
    gap: 1px;
  }
  .confirm {
    color: var(--danger);
  }

  /* ---- the footer ---- */
  .delete-group {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .foot-msg {
    display: flex;
    align-items: flex-start;
    gap: 7px;
    margin-block-start: 4px;
    color: var(--danger);
    font-size: var(--type-cell);
  }
  .foot-msg :global(.icon) {
    flex: none;
    margin-block-start: 1px;
  }

  /* A narrow dialog (larger text in a small window): the queues become a row above the editor. */
  @container dialog (max-width: 40rem) {
    .queues-shell {
      grid-template-columns: minmax(0, 1fr);
      grid-template-rows: auto minmax(0, 1fr);
    }
    .list {
      flex-direction: row;
      align-items: center;
      padding-block: 6px;
      border-inline-end: 0;
      border-block-end: 1px solid var(--line);
    }
    .list-head {
      display: none;
    }
    .list ul {
      display: flex;
      flex: 1 1 auto;
      min-inline-size: 0;
      overflow-x: auto;
      overflow-y: hidden;
      scrollbar-width: none;
    }
    .queue {
      inline-size: auto;
      white-space: nowrap;
    }
    .qname {
      overflow: visible;
    }
    .btn.new {
      flex: none;
      align-self: center;
      margin: 0;
    }
  }
  /* A wide dialog (it is up to 72rem; the editor is 50rem or more then): the settings and the
     order on the start side, the schedule beside them, in the order Tab takes them (settings,
     schedule, order). The dialog's width, since the editor cannot query its own. */
  @container dialog (min-width: 64.5rem) {
    .editor {
      grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
      grid-template-rows: auto auto minmax(0, 1fr) auto;
      align-items: start;
      column-gap: 24px;
    }
    .editor > :global(*) {
      grid-column: 1 / -1;
    }
    .editor > .settings {
      grid-column: 1;
      grid-row: 2;
    }
    .editor > :global(.schedule) {
      grid-column: 2;
      grid-row: 2 / span 2;
    }
    .editor > .order {
      grid-column: 1;
      grid-row: 3;
    }
    /* without a schedule (its card is one switch), the schedule goes under the settings and the
       order takes the other column from the top: a long order uses the height, not a half */
    .editor:not(:has(:global(.schedule .times))) > :global(.schedule) {
      grid-column: 1;
      grid-row: 3;
    }
    .editor:not(:has(:global(.schedule .times))) > .order {
      grid-column: 2;
      grid-row: 2 / span 2;
    }
  }
  /* a narrow editor puts the settings in one column */
  @container queue-editor (max-width: 30rem) {
    .fields {
      grid-template-columns: minmax(0, 1fr);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .queue {
      transition: none;
    }
  }
</style>
