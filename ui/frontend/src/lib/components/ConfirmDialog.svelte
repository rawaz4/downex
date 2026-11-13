<script lang="ts">
  import { tick } from 'svelte';
  import { queueOf, toApiError, type ConfirmOptions, type Download, type Queue, type Settings } from '../api';
  import { CONFIRM_GUARD_MS, categoryFolder } from '../confirm';
  import { sizeHint } from '../display';
  import { formatBytes } from '../format';
  import { kindOf } from '../categories';
  import {
    defaultPick,
    isSite,
    pickSize,
    pickSizeText,
    pickedAudio,
    trackById,
    videoPictureText,
    videoTracks,
  } from '../media';
  import { escapeBidi } from '../text';
  import { checkDir, checkFilename } from '../validate';
  import Dialog from './Dialog.svelte';
  import FileIcon from './FileIcon.svelte';
  import Icon from './Icon.svelte';
  import MediaPicker from './MediaPicker.svelte';
  import Select, { type SelectOption } from './Select.svelte';
  import VideoCard from './VideoCard.svelte';

  interface Props {
    /** A download awaiting confirmation (DESKTOP-SPEC §1). */
    download: Download;
    settings: Pick<Settings, 'categories' | 'defaultDir' | 'useCategoryFolders'>;
    /** How many downloads await confirmation, this one included. */
    waiting?: number;
    pickFolder: (initialDir: string) => Promise<string | null>;
    /** Start download / Download later; rejects with the daemon's error. */
    onconfirm: (opts: ConfirmOptions) => Promise<void>;
    /** Cancel: removes the download; rejects with the daemon's error. */
    oncancel: () => Promise<void>;
    /** Closing leaves the download awaiting confirmation. */
    onclose: () => void;
    fallbackFocus?: () => HTMLElement | null | undefined;
    /**
     * The user asked for this dialog (the list, the details panel). Otherwise it opened by
     * itself: focus stays off its form, and its buttons and Enter do nothing for `guardMs`,
     * so input meant for something else cannot edit the name or start the download.
     */
    requested?: boolean;
    guardMs?: number;
    /**
     * The queues to choose from (M6); with fewer than two (or without `onmove`), none is
     * offered. "Start download" queues the download in the chosen queue. "Download later"
     * puts it into a stopped queue, queued, where it starts when that queue runs, and
     * into a running one paused (queued there, it would start at once).
     */
    queues?: readonly Queue[];
    /** Moves the download (still awaiting confirmation) to the end of a queue; rejects with the daemon's error. */
    onmove?: (queueId: number) => Promise<void>;
  }

  let {
    download,
    settings,
    waiting = 1,
    pickFolder,
    onconfirm,
    oncancel,
    onclose,
    fallbackFocus,
    requested = false,
    guardMs = CONFIRM_GUARD_MS,
    queues = [],
    onmove,
  }: Props = $props();

  const uid = $props.id();

  // The daemon's proposal, captured once: later updates of the download must not
  // overwrite what the user is editing. No name = the server's answer names the file
  // (DESKTOP-SPEC §1), so nothing that would not be used is shown as the name.
  // svelte-ignore state_referenced_locally
  const proposed = { dir: download.dir, filename: download.filename, category: download.category };
  // svelte-ignore state_referenced_locally
  const known = settings.categories.some((c) => c.name === proposed.category);
  // A media download (MEDIA-SPEC §6.3): its tracks are chosen here, starting from the
  // daemon's pick. Captured once, like the proposal. A web page's video (SITE-MEDIA-SPEC
  // §5.4) offers every MP4 quality; its file stays an .mp4 whatever is chosen.
  // svelte-ignore state_referenced_locally
  const media = download.kind === 'media' ? download.media : null;
  const site = isSite(media);

  let filename = $state(proposed.filename);
  let dir = $state(proposed.dir);
  let category = $state(known ? proposed.category : '');
  let attempted = $state(false);
  let busy = $state<'start' | 'later' | 'cancel' | null>(null);
  let failure = $state<string | null>(null);
  let pick = $state(defaultPick(media));
  // svelte-ignore state_referenced_locally
  const proposedQueue = queueOf(download);
  let queueId = $state(proposedQueue);
  const offerQueues = $derived(queues.length > 1 && onmove !== undefined);
  /**
   * Quality cards to choose from (a video's or stream's): they get a column of their own. Only
   * video tracks make cards (the "Audio only" card is one beside them): an audio-only stream
   * keeps one column with its audio part.
   */
  const qualities = media !== null && videoTracks(media.tracks, site, media.selectedVideo).length > 0;
  const waitingText = $derived(
    `${waiting - 1} more ${waiting === 2 ? 'download is' : 'downloads are'} waiting for confirmation.`,
  );
  const chosenQueue = $derived(queues.find((q) => q.id === queueId));
  /** "Download later" leaves it queued in the chosen queue, to start when the queue runs. */
  const laterQueued = $derived(chosenQueue !== undefined && !chosenQueue.running);
  let body: HTMLDivElement | undefined = $state();
  const openedAt = performance.now();

  /** Activation right after the dialog appeared by itself is not a decision about it. */
  function tooSoon(): boolean {
    return !requested && performance.now() - openedAt < guardMs;
  }

  const sizeLabel = $derived.by(() => {
    if (media) return pickSizeText(pickSize(media, pick)) || 'unknown';
    const hint = sizeHint(download);
    return hint > 0 ? formatBytes(hint) : 'unknown';
  });
  const errors = $derived.by((): { filename?: string; dir?: string } => {
    if (!attempted) return {};
    const out: { filename?: string; dir?: string } = {};
    const name = filename.trim();
    // Empty = the daemon's proposal (or, without one, the server's name).
    out.filename = name === '' ? undefined : (checkFilename(name) ?? undefined);
    const folder = dir.trim();
    out.dir = folder === '' ? 'Choose a folder' : (checkDir(folder) ?? undefined);
    return out;
  });

  function chooseCategory(name: string) {
    category = name;
    dir = categoryFolder(settings, name);
  }

  async function browse() {
    const picked = await pickFolder(dir.trim() || settings.defaultDir);
    if (picked) dir = picked;
  }

  async function confirm(start: boolean) {
    if (busy !== null || tooSoon()) return;
    attempted = true;
    failure = null;
    await tick();
    if (errors.filename || errors.dir) {
      body?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
      return;
    }
    const typed = filename.trim();
    // A web page's video is always one MP4 file: a renamed one keeps the extension.
    const name = site && typed !== '' && !/\.mp4$/i.test(typed) ? `${typed}.mp4` : typed;
    const folder = dir.trim();
    busy = start ? 'start' : 'later';
    try {
      // downexd queues a download confirmed with a queue_id (and with start): "Start
      // download" names only another queue; "Download later" names a stopped queue, its
      // own included, and goes to a running one by a move first, then stays paused.
      const chosen = offerQueues && chosenQueue ? chosenQueue.id : null;
      let named: number | null = null;
      if (chosen !== null && (start ? chosen !== proposedQueue : laterQueued)) named = chosen;
      else if (chosen !== null && !start && chosen !== proposedQueue && onmove) await onmove(chosen);
      // Unchanged values, and an empty name, are left to the daemon's proposal.
      const choice = {
        start,
        dir: folder === proposed.dir ? '' : folder,
        filename: name === proposed.filename ? '' : name,
        ...(named !== null ? { queueId: named } : {}),
      };
      // The chosen tracks are sent as shown, the daemon's pick included.
      await onconfirm(
        media ? { ...choice, videoTrack: pick.video, audioTrack: pickedAudio(media, pick) } : choice,
      );
    } catch (err) {
      failure = toApiError(err).message;
      busy = null;
      return;
    }
    busy = null;
    onclose();
  }

  // svelte-ignore state_referenced_locally
  const kind = kindOf(download);
  const categoryOptions: SelectOption<string>[] = $derived([
    ...settings.categories.map((c) => ({ value: c.name, label: escapeBidi(c.name) })),
    { value: '', label: 'None' },
  ]);
  const queueOptions: SelectOption<number>[] = $derived(
    queues.map((q) => ({ value: q.id, label: `${escapeBidi(q.name)}${q.running ? '' : ' (stopped)'}` })),
  );
  /** The chosen quality's picture, as the video card's tooltip. */
  const picture = $derived.by(() => {
    const video = media ? trackById(media, pick.video) : undefined;
    return video ? videoPictureText(video, site) : '';
  });

  async function cancel() {
    if (busy !== null || tooSoon()) return;
    failure = null;
    busy = 'cancel';
    try {
      await oncancel();
    } catch (err) {
      failure = toApiError(err).message;
      busy = null;
      return;
    }
    busy = null;
    onclose();
  }
</script>

<Dialog
  title="Confirm download"
  description={site
    ? 'A video from the browser is waiting. Choose its quality and where it is saved, then start it.'
    : media
      ? 'A stream from the browser is waiting. Choose its quality and where it is saved, then start it.'
      : 'A download from the browser is waiting. Check where it is saved, then start it.'}
  {onclose}
  onsubmit={() => void confirm(true)}
  busy={busy !== null}
  {fallbackFocus}
  focusDialog={!requested}
  size="md"
  width={qualities ? '56rem' : '44rem'}
>
  {#snippet icon()}
    <FileIcon {kind} size={32} />
  {/snippet}

  {#snippet headerAside()}
    {#if waiting > 1}
      <!-- in the header, so that it never costs the form a line -->
      <span class="chip waiting" title={waitingText}
        ><span aria-hidden="true">{waiting - 1} more waiting</span><span class="sr-only">{waitingText}</span></span
      >
    {/if}
  {/snippet}

  <div class="form" class:split={qualities} class:failed={failure !== null} bind:this={body}>
    {#if media}
      <!-- the address is the card's (one line after the site's name), not a field of its own -->
      <div class="card-area">
        <VideoCard
          info={media}
          fallbackTitle={proposed.filename}
          url={download.url}
          {picture}
          showAddress
          addressLabel={site ? 'Page' : 'Address'}
        />
      </div>
      {#if qualities}
        <div class="quality-area">
          <MediaPicker info={media} bind:pick disabled={busy !== null} part="video" />
        </div>
      {/if}
    {:else}
      <div class="field">
        <span class="label">Address</span>
        <!-- a few lines at most, never a scroller of its own: the whole address is the tooltip,
             and a triple click selects all of it -->
        <p class="url mono" dir="auto" title={escapeBidi(download.url)}><span class="clamp">{escapeBidi(download.url)}</span></p>
      </div>
    {/if}

    <div class="fields">
      {#if media}
        <MediaPicker info={media} bind:pick disabled={busy !== null} part={qualities ? 'audio' : 'all'} />
      {/if}

      <div class="field">
        <div class="label-row">
          <label for="{uid}-name">File name</label>
          <span class="hint tnum" id="{uid}-size">Size: {sizeLabel}{site ? ' · saved as MP4' : ''}</span>
        </div>
        <input
          id="{uid}-name"
          class="input"
          bind:value={filename}
          data-autofocus={requested ? '' : undefined}
          placeholder={proposed.filename || 'Named by the server'}
          spellcheck="false"
          autocomplete="off"
          dir="auto"
          aria-invalid={errors.filename ? 'true' : undefined}
          aria-describedby="{uid}-size{errors.filename ? ` ${uid}-name-error` : ''}"
        />
        {#if errors.filename}
          <p id="{uid}-name-error" class="error-text">{errors.filename}</p>
        {/if}
      </div>

      <!-- where it goes: the category (and the queue), then the folder. The folder gets a row of
           its own wherever the category would leave it too little room to show its path (a
           video's fields column, or under two selects); a file's without queues sits beside
           the category. -->
      <div class="where" class:with-queue={offerQueues}>
        <div class="field">
          <label for="{uid}-category">Category</label>
          <Select
            id="{uid}-category"
            value={category}
            options={categoryOptions}
            onchange={(v) => chooseCategory(v)}
            ariaDescribedby="{uid}-category-hint"
            disabled={busy !== null}
          />
        </div>

        {#if offerQueues}
          <div class="field">
            <label for="{uid}-queue">Queue</label>
            <Select
              id="{uid}-queue"
              bind:value={queueId}
              options={queueOptions}
              ariaDescribedby="{uid}-queue-hint"
              disabled={busy !== null}
            />
          </div>
          <!-- the queue's hint under both selects, at the row's width -->
          <p class="hint queue-hint" id="{uid}-queue-hint">
            {laterQueued
              ? 'Download later puts it in this queue: it starts when the queue runs.'
              : 'Start download queues it here; Download later keeps it paused in this queue.'}
          </p>
        {/if}

        <div class="field dir">
          <!-- the category's hint at the end of this label row: it is about this folder -->
          <div class="label-row">
            <label for="{uid}-dir">Save to<span class="sr-only">{' folder'}</span></label>
            <span class="hint" id="{uid}-category-hint">Choosing a category proposes its folder.</span>
          </div>
          <div class="with-button">
            <div class="folder">
              <span class="lead" aria-hidden="true"><Icon name="folder" size={16} /></span>
              <input
                id="{uid}-dir"
                class="input mono"
                bind:value={dir}
                title={escapeBidi(dir)}
                spellcheck="false"
                autocomplete="off"
                aria-invalid={errors.dir ? 'true' : undefined}
                aria-describedby={errors.dir ? `${uid}-dir-error` : undefined}
              />
            </div>
            <button type="button" class="btn" onclick={browse} disabled={busy !== null}>Browse…</button>
          </div>
          {#if errors.dir}
            <p id="{uid}-dir-error" class="error-text">{errors.dir}</p>
          {/if}
        </div>
      </div>

    </div>

    <!-- after the fields; beside them, under the qualities, where a video's dialog has room -->
    {#if failure}
      <p class="note fail failure" role="alert">{escapeBidi(failure)}</p>
    {/if}
  </div>

  {#snippet footer()}
    <button type="button" class="btn danger-text cancel" onclick={() => void cancel()} disabled={busy !== null}>
      {busy === 'cancel' ? 'Canceling…' : 'Cancel download'}
    </button>
    <button type="button" class="btn" onclick={() => void confirm(false)} disabled={busy !== null}>
      {busy === 'later' ? 'Saving…' : 'Download later'}
    </button>
    <button type="submit" class="btn primary" disabled={busy !== null}>
      {busy === 'start' ? 'Starting…' : 'Start download'}
    </button>
  {/snippet}
</Dialog>

<style>
  /* One column; a video's or stream's qualities get a column of their own beside the fields
     once the dialog is wide (it widens to 56rem for them, §4.7): the card over both columns,
     then the qualities (start) and the fields (end), in the order Tab takes them. */
  .form {
    display: grid;
    gap: var(--sp-6);
    min-inline-size: 0;
  }
  .fields {
    display: grid;
    gap: var(--sp-6);
    align-content: start;
    min-inline-size: 0;
  }
  .card-area,
  .quality-area {
    min-inline-size: 0;
  }
  @container dialog (min-width: 42rem) {
    .form.split {
      grid-template-columns: minmax(0, 1fr) minmax(0, 1.25fr);
      grid-template-areas:
        'card card'
        'quality fields';
      gap: var(--sp-6) var(--sp-8);
      align-items: start;
    }
    .split .card-area {
      grid-area: card;
    }
    .split .quality-area {
      grid-area: quality;
    }
    .split .fields {
      grid-area: fields;
    }
    /* a refusal (not enough disk space, …) goes under the qualities, in the room the fields
       column leaves there, so that it costs no row */
    .form.split.failed {
      grid-template-rows: auto auto minmax(0, 1fr);
      grid-template-areas:
        'card card'
        'quality fields'
        'failure fields';
    }
    .split .failure {
      grid-area: failure;
    }
  }
  /* at most three lines with an ellipsis (a signed storage link runs to 600 characters); the
     clamp is inside the frame, so that its padding never shows part of a fourth line */
  .url {
    margin: 0;
    padding: 7px 11px;
    border: 1px solid var(--line);
    border-radius: 9px;
    background: var(--surface-2);
    color: var(--ink-2);
    overflow-wrap: anywhere;
    user-select: text;
  }
  .url .clamp {
    display: -webkit-box;
    -webkit-box-orient: vertical;
    -webkit-line-clamp: 3;
    line-clamp: 3;
    overflow: hidden;
  }
  /* the category beside the folder (a file's, 44rem wide: the folder still shows its path) */
  .where {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1.9fr);
    gap: var(--sp-6) var(--sp-7);
    align-items: start;
  }
  /* with a queue: the two selects side by side, the queue's hint and then the folder under them */
  .where.with-queue {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .where.with-queue > .queue-hint,
  .where.with-queue > .dir {
    grid-column: 1 / -1;
  }
  .queue-hint {
    margin-block-start: calc(-1 * var(--sp-3));
  }
  /* a video's fields column is too narrow to share: the category and the folder one per row */
  .split .where:not(.with-queue) {
    grid-template-columns: minmax(0, 1fr);
  }
  .with-button {
    display: flex;
    gap: var(--sp-4);
  }
  .folder {
    position: relative;
    display: flex;
    flex: 1;
    align-items: center;
    min-inline-size: 0;
  }
  .lead {
    position: absolute;
    inset-inline-start: 11px;
    display: grid;
    color: var(--faint);
    pointer-events: none;
  }
  .folder .input {
    padding-inline-start: 34px;
  }
  .with-button .btn {
    block-size: 36px;
    min-block-size: 36px;
  }
  .cancel {
    margin-inline-end: auto;
  }
  .waiting {
    flex: none;
    border-color: transparent;
    background: var(--info-soft);
    color: var(--info);
    font-weight: 600;
  }
  /* the dialog's width (Dialog), not the window's */
  @container dialog (max-width: 32rem) {
    .where,
    .where.with-queue {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>
