<script lang="ts">
  // The Add dialog (DESIGN-SYSTEM.md §4.6): "Add" with the Files / Video or stream mode in the
  // header (the dialog keeps its name, "Add downloads" / "Add video or stream"). Files: the
  // addresses, the folder, and More options (name, connections, speed limit, queue, paused)
  // collapsed behind a summary of what it holds. Video or stream: the address (✓ Found once
  // listed), the video card, one quality card per class, the audio track beside the folder,
  // More options (name, queue, paused); the footer sums up what will be added.
  import { onDestroy, tick } from 'svelte';
  import type {
    AddMediaRequest,
    AddRequest,
    Download,
    MediaInfo,
    MediaOrigin,
    ProbeMediaRequest,
    Queue,
    ToolsStatus,
  } from '../api';
  import { LIMITS, MAIN_QUEUE_ID } from '../api';
  import type { AddOutcome } from '../stores/downloads.svelte';
  import { installFraction, installText, isFirstInstall } from '../stores/tools.svelte';
  import Dialog from './Dialog.svelte';
  import Icon from './Icon.svelte';
  import MediaPicker from './MediaPicker.svelte';
  import ProgressBar from './ProgressBar.svelte';
  import SegmentedControl from './SegmentedControl.svelte';
  import Select, { type SelectOption } from './Select.svelte';
  import VideoCard, { shortClassName } from './VideoCard.svelte';
  import { RATE_UNITS, formatBytes, formatSpeed, parseRate, type RateUnit } from '../format';
  import {
    codecName,
    defaultPick,
    languageName,
    looksLikeManifest,
    mediaErrorText,
    needsToolsSettings,
    pickSize,
    pickedAudio,
    qualityName,
    retrySite,
    trackById,
    videoPictureText,
    isSite,
    type SiteRetry,
    type TrackPick,
  } from '../media';
  import { splitUrls, validateAddForm, validateStreamForm, type AddFormErrors, type StreamFormErrors } from '../validate';
  import { escapeBidi } from '../text';

  interface Props {
    defaults: { dir: string; maxConnections: number };
    pickFolder: (initialDir: string) => Promise<string | null>;
    submit: (requests: AddRequest[]) => Promise<AddOutcome[]>;
    /**
     * "Video or stream" mode: lists the qualities of an HLS/DASH manifest (MEDIA-SPEC §6) or
     * of a web page's video (SITE-MEDIA-SPEC §5.4), then adds it. Without both, the dialog
     * adds files only.
     */
    probeMedia?: (req: ProbeMediaRequest) => Promise<MediaInfo>;
    submitMedia?: (req: AddMediaRequest) => Promise<Download>;
    /** yt-dlp and Deno: their install progress while a web page waits for them. */
    tools?: { readonly status: ToolsStatus | null };
    /** Opens Settings → Video sites (offered when yt-dlp could not be set up, or is off). */
    onopensettings?: () => void;
    onclose: () => void;
    /** A single manifest address opens the dialog in stream mode. */
    initialUrls?: string;
    /** The mode to open in (the Add split button's menu); a manifest address opens as a stream anyway. */
    initialMode?: 'files' | 'stream';
    /** Bytes free on the volume of a folder (absolute); without it, or when it fails, no free space is shown. */
    freeSpace?: (dir: string) => Promise<number>;
    fallbackFocus?: () => HTMLElement | null | undefined;
    /** The queues to choose from (M6); with fewer than two, no choice is offered. */
    queues?: readonly Queue[];
    /** The queue proposed (default: the main queue). */
    defaultQueue?: number | null;
  }

  let {
    defaults,
    pickFolder,
    submit,
    probeMedia,
    submitMedia,
    tools,
    onopensettings,
    onclose,
    initialUrls = '',
    initialMode = 'files',
    freeSpace,
    fallbackFocus,
    queues = [],
    defaultQueue = null,
  }: Props = $props();

  const uid = $props.id();
  const ids = {
    urls: `${uid}-urls`,
    stream: `${uid}-stream`,
    dir: `${uid}-dir`,
    filename: `${uid}-filename`,
    connections: `${uid}-connections`,
    speed: `${uid}-speed`,
    unit: `${uid}-unit`,
    queue: `${uid}-queue`,
    more: `${uid}-more`,
  };

  // svelte-ignore state_referenced_locally
  const canStream = probeMedia !== undefined && submitMedia !== undefined;
  // svelte-ignore state_referenced_locally
  const initialList = splitUrls(initialUrls);
  const manifestGiven = initialList.length === 1 && looksLikeManifest(initialList[0] ?? '');
  // svelte-ignore state_referenced_locally
  const startAsStream = canStream && (manifestGiven || initialMode === 'stream');
  /** An address given to a dialog that opens as a stream goes into the stream field. */
  const streamGiven = startAsStream && initialList.length === 1;

  // svelte-ignore state_referenced_locally
  let urls = $state(streamGiven ? '' : initialUrls);
  let mode = $state<'files' | 'stream'>(startAsStream ? 'stream' : 'files');
  let streamUrl = $state(streamGiven ? (initialList[0] ?? '') : '');
  let dir = $state('');
  let filename = $state('');
  // svelte-ignore state_referenced_locally
  let connections = $state(String(defaults.maxConnections || 8));
  let speedValue = $state('');
  let speedUnit = $state<RateUnit>('MB/s');
  let startPaused = $state(false);
  // svelte-ignore state_referenced_locally
  let queueId = $state(queues.some((q) => q.id === defaultQueue) ? (defaultQueue as number) : MAIN_QUEUE_ID);
  const chosenQueue = $derived(queues.find((q) => q.id === queueId));
  const offerQueues = $derived(queues.length > 1);
  let attempted = $state(false);
  let busy = $state<'add' | 'probe' | null>(null);
  let failures = $state.raw<{ url: string; error: string }[]>([]);
  let form: HTMLDivElement | undefined = $state();
  /** More options is open (it opens by itself when one of its fields has an error). */
  let moreOpen = $state(false);

  // Stream mode: the qualities of the address last listed, and the user's choice.
  let probed = $state.raw<{ url: string; info: MediaInfo } | null>(null);
  let pick = $state<TrackPick>({ video: '', audio: '' });
  let streamFailure = $state<string | null>(null);
  /** The failure can be helped in Settings → Video sites (yt-dlp could not be set up, or it is off). */
  let failureSettings = $state(false);
  /** What the address in the field is: a manifest, or a web page with a video. */
  const origin: MediaOrigin = $derived(looksLikeManifest(streamUrl) ? 'manifest' : 'site');

  // A web page that downexd answers with "still setting up yt-dlp" or "still reading the
  // page" is asked for again (SITE-MEDIA-SPEC §3.3) until it answers; Cancel stops that.
  let running: { controller: AbortController; url: string } | null = null;
  let waiting = $state.raw<SiteRetry | null>(null);
  let startedAt = $state(0);
  let now = $state(0);
  const elapsed = $derived(Math.max(0, Math.floor((now - startedAt) / 1000)));
  /** yt-dlp and Deno are being downloaded for this page (first use). */
  const settingUp = $derived(waiting?.kind === 'tools_installing' || isFirstInstall(tools?.status ?? null));
  const setupFraction = $derived.by(() => {
    const fromEvents = installFraction(tools?.status ?? null);
    if (Number.isFinite(fromEvents)) return fromEvents;
    return waiting?.kind === 'tools_installing' ? waiting.fraction : Number.NaN;
  });

  const urlList = $derived(splitUrls(urls));
  const urlCount = $derived(urlList.length);
  /** Files mode holds one manifest address: it is better added as a stream. */
  const manifestInFiles = $derived(canStream && urlCount === 1 && looksLikeManifest(urlList[0] ?? ''));
  const result = $derived(
    validateAddForm({
      urls,
      dir,
      filename,
      connections,
      speedValue,
      speedUnit,
      startPaused,
      // The main queue is the default: only another queue is named.
      queueId: offerQueues && queueId !== MAIN_QUEUE_ID ? queueId : 0,
    }),
  );
  const streamResult = $derived(validateStreamForm({ url: streamUrl, dir, filename }));
  /** Qualities of the address in the field (a changed address must be listed again). */
  const info = $derived(probed !== null && probed.url === streamUrl.trim() ? probed.info : null);
  /** A video's or stream's qualities are found: the dialog widens, the qualities get a column. */
  const found = $derived(mode === 'stream' && info !== null);
  /** Cancel stops a web page's wait (and leaves the dialog open). */
  const stoppable = $derived(origin === 'site' && (busy === 'probe' || (busy === 'add' && waiting !== null)));
  const errors: AddFormErrors & StreamFormErrors = $derived.by(() => {
    if (!attempted) return {};
    if (mode === 'stream') return streamResult.ok ? {} : streamResult.errors;
    return result.ok ? {} : result.errors;
  });
  /** A field inside More options has an error: it opens by itself. */
  const hiddenError = $derived(Boolean(errors.filename || errors.connections || errors.speed));
  $effect(() => {
    if (hiddenError) moreOpen = true;
  });

  type ErrorKey = keyof (AddFormErrors & StreamFormErrors);
  const errorIds: Record<ErrorKey, string> = {
    urls: `${ids.urls}-error`,
    url: `${ids.stream}-error`,
    dir: `${ids.dir}-error`,
    filename: `${ids.filename}-error`,
    connections: `${ids.connections}-error`,
    speed: `${ids.speed}-error`,
  };

  function describedBy(hint: string | null, key: ErrorKey): string | undefined {
    const parts = [hint, errors[key] ? errorIds[key] : null].filter(Boolean);
    return parts.length > 0 ? parts.join(' ') : undefined;
  }

  $effect(() => {
    if (busy === null || origin !== 'site') return;
    now = Date.now();
    const timer = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(timer);
  });

  // A changed address ends the wait for the old one.
  $effect(() => {
    const url = streamUrl.trim();
    if (running && running.url !== url) stop();
  });

  onDestroy(() => running?.controller.abort());

  // ---- free space of the folder's volume ---------------------------------------------------

  /** The folder the download goes to: the one typed, else the default. */
  const folder = $derived((dir.trim() || defaults.dir).trim());
  let free = $state<number | null>(null);
  let askedOnce = false;
  $effect(() => {
    const target = folder;
    const ask = freeSpace;
    if (!ask || !/^([A-Za-z]:[\\/]|[\\/])/.test(target)) {
      free = null;
      return;
    }
    let live = true;
    // Typing a folder asks once it pauses; the first answer comes at once.
    const timer = setTimeout(
      () => {
        ask(target).then(
          (n) => {
            if (live) free = Number.isFinite(n) && n >= 0 ? n : null;
          },
          () => {
            if (live) free = null;
          },
        );
      },
      askedOnce ? 300 : 0,
    );
    askedOnce = true;
    return () => {
      live = false;
      clearTimeout(timer);
    };
  });
  /** What the chosen video needs (bytes; -1 unknown): more than is free makes the free space a warning. */
  const needed = $derived(mode === 'stream' && info ? pickSize(info, pick).bytes : -1);
  const tight = $derived(free !== null && needed > 0 && needed > free);

  // ---- calls ---------------------------------------------------------------------------------

  /** Stops waiting for a web page (Cancel): nothing more is asked, the dialog stays open. */
  function stop() {
    running?.controller.abort();
    running = null;
    busy = null;
    waiting = null;
  }

  /**
   * Runs a call of the "Video or stream" mode. A web page's is asked for again while
   * downexd sets up yt-dlp or reads the page. Resolves to its result, or undefined after a
   * failure (shown) or a stop.
   */
  async function run<T>(url: string, kind: MediaOrigin, call: () => Promise<T>): Promise<T | undefined> {
    const controller = new AbortController();
    running = { controller, url };
    waiting = null;
    startedAt = now = Date.now();
    try {
      return kind === 'site' ? await retrySite(call, { signal: controller.signal, onwait: (r) => (waiting = r) }) : await call();
    } catch (err) {
      if (controller.signal.aborted) return undefined;
      streamFailure = mediaErrorText(err, kind);
      failureSettings = kind === 'site' && needsToolsSettings(err);
      return undefined;
    } finally {
      if (running?.controller === controller) {
        running = null;
        busy = null;
        waiting = null;
      }
    }
  }

  function setMode(next: 'files' | 'stream') {
    if (next === mode) return;
    if (next === 'stream' && streamUrl.trim() === '' && urlCount === 1) streamUrl = urlList[0] ?? '';
    if (next === 'files' && urls.trim() === '' && streamUrl.trim() !== '') urls = streamUrl.trim();
    mode = next;
    attempted = false;
    failures = [];
    streamFailure = null;
    failureSettings = false;
  }

  async function focusInvalid() {
    if (hiddenError) moreOpen = true;
    await tick();
    form?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
  }

  async function onsubmit() {
    if (busy !== null) return;
    if (mode === 'stream') return submitStream();
    attempted = true;
    if (!result.ok) return focusInvalid();
    busy = 'add';
    failures = [];
    try {
      const outcomes = await submit(result.requests);
      const failed = outcomes.flatMap((o) => (o.ok ? [] : [{ url: o.url, error: o.error }]));
      if (failed.length === 0) {
        onclose();
        return;
      }
      failures = failed;
      urls = failed.map((f) => f.url).join('\n');
    } finally {
      busy = null;
    }
  }

  /**
   * First lists the qualities (the user then chooses), then adds the stream or video. A
   * web page goes as origin 'site', without a manifest (SITE-MEDIA-SPEC §3.3).
   */
  async function submitStream() {
    attempted = true;
    const checked = streamResult;
    if (!checked.ok || !probeMedia || !submitMedia) return focusInvalid();
    streamFailure = null;
    failureSettings = false;
    const kind: MediaOrigin = looksLikeManifest(checked.url) ? 'manifest' : 'site';
    const target: ProbeMediaRequest =
      kind === 'site' ? { manifestUrl: '', pageUrl: checked.url, origin: kind } : { manifestUrl: checked.url };
    const listed = info;
    if (listed === null) {
      busy = 'probe';
      const found = await run(checked.url, kind, () => probeMedia(target));
      if (found === undefined) return;
      probed = { url: checked.url, info: found };
      pick = defaultPick(found);
      await tick();
      // The keyboard goes on with the choice: the chosen quality (or the audio track).
      form?.querySelector<HTMLElement>('[role="radiogroup"] input:checked, .audio [role="combobox"]')?.focus();
      return;
    }
    busy = 'add';
    const added = await run(checked.url, kind, () =>
      submitMedia({
        ...target,
        dir: checked.dir,
        filename: checked.filename,
        source: 'ui',
        videoTrack: pick.video,
        audioTrack: pickedAudio(listed, pick),
        startPaused,
        ...(offerQueues && queueId !== MAIN_QUEUE_ID ? { queueId } : {}),
      }),
    );
    if (added !== undefined) onclose();
  }

  async function browse() {
    const picked = await pickFolder(dir || defaults.dir);
    if (picked) dir = picked;
  }

  // ---- what the dialog says ------------------------------------------------------------------

  const submitLabel = $derived.by(() => {
    if (mode === 'stream') {
      if (busy === 'probe') return 'Finding qualities…';
      if (busy === 'add') return 'Adding…';
      if (!info) return 'Find qualities';
      return origin === 'site' ? 'Add video' : 'Add stream';
    }
    if (busy !== null) return 'Adding…';
    return urlCount > 1 ? `Add ${urlCount} downloads` : 'Add download';
  });
  const submitIcon = $derived(busy !== null ? null : mode === 'stream' && !info ? 'search' : 'arrow-down');

  const queueOptions: SelectOption<number>[] = $derived(
    queues.map((q) => ({ value: q.id, label: `${escapeBidi(q.name)}${q.running ? '' : ' (stopped)'}` })),
  );
  /** The Add footer's limit: as typed with the picked unit, or read back when a unit was typed ("2 MiB"). */
  function limitSummary(text: string, unit: RateUnit): string {
    if (/^[\d.,\s]+$/.test(text)) return `${text} ${unit}`;
    const bps = parseRate(text, unit);
    return bps !== null && bps > 0 ? formatSpeed(bps) : `${text} ${unit}`;
  }

  const unitOptions: SelectOption<RateUnit>[] = RATE_UNITS.map((u) => ({ value: u, label: u }));
  const modeOptions = [
    { value: 'files' as const, label: 'Files', icon: 'link' as const },
    { value: 'stream' as const, label: 'Video or stream', icon: 'play' as const },
  ];

  const queueName = $derived(offerQueues && chosenQueue ? escapeBidi(chosenQueue.name) : '');
  const startText = $derived(startPaused ? 'Add paused' : 'Start now');

  /** The file name More options would use, as far as the dialog knows it. */
  const streamName = $derived.by(() => {
    const typed = filename.trim();
    if (typed) return /\.mp4$/i.test(typed) ? typed : `${typed}.mp4`;
    const title = info?.title.trim() ?? '';
    return title ? `${title}.mp4` : origin === 'site' ? 'Name from the video title' : 'Name from the stream';
  });

  /** What More options holds, while it is closed. */
  const moreSummary = $derived.by(() => {
    // a video's: the queue and how it starts first, its name last (where the ellipsis cuts)
    if (mode === 'stream') return [queueName, startText, escapeBidi(streamName)].filter(Boolean).join(' · ');
    const conns = connections.trim() || String(defaults.maxConnections || 8);
    return [
      urlCount > 1 ? 'Names from the URLs' : escapeBidi(filename.trim()) || 'Name from the server',
      `${conns} connections`,
      speedValue.trim() ? `limit ${limitSummary(speedValue.trim(), speedUnit)}` : 'No speed limit',
      queueName,
      startText,
    ]
      .filter(Boolean)
      .join(' · ');
  });

  /** The footer's summary of a listed video: "1080p · H.264", "+ English audio", "about 544 MB". */
  const streamSummary = $derived.by(() => {
    if (!info) return null;
    const video = trackById(info, pick.video);
    const audio = trackById(info, pickedAudio(info, pick));
    const head = video
      ? [shortClassName(qualityName(video)), video.codecs ? codecName(video.codecs.split(',')[0] ?? '') : ''].filter(Boolean).join(' · ') ||
        'Video'
      : 'Audio only';
    const lang = audio ? languageName(audio.language) : '';
    const sound = !video
      ? lang
        ? `· ${lang}`
        : ''
      : video.kind === 'muxed'
        ? 'with its sound'
        : audio
          ? `+ ${lang ? `${lang} audio` : 'audio'}`
          : '';
    const size = pickSize(info, pick);
    const prefix = size.kind === 'estimate' ? 'about ' : size.kind === 'approx' ? '~' : '';
    return { head, sound, prefix, size: size.bytes > 0 ? formatBytes(size.bytes) : '' };
  });

  const chosenPicture = $derived.by(() => {
    if (!info) return '';
    const video = trackById(info, pick.video);
    return video ? videoPictureText(video, isSite(info)) : '';
  });

  const conns = $derived(connections.trim() || String(defaults.maxConnections || 8));
</script>

<Dialog
  title={canStream ? 'Add' : 'Add downloads'}
  ariaLabel={canStream ? (mode === 'stream' ? 'Add video or stream' : 'Add downloads') : undefined}
  {onclose}
  {onsubmit}
  busy={busy !== null && !stoppable}
  {fallbackFocus}
  size="md"
  width={found ? '56rem' : undefined}
>
  {#snippet headerAside()}
    {#if canStream}
      <SegmentedControl
        value={mode}
        options={modeOptions}
        legend="What to add"
        name="{uid}-mode"
        onchange={(v) => setMode(v)}
      />
    {/if}
  {/snippet}

  <div class="form" class:split={found} bind:this={form}>
    {#if mode === 'files'}
      <div class="field">
        <div class="label-row">
          <label for={ids.urls}>URLs</label>
          <span id="{ids.urls}-hint" class="hint">
            One URL per line (http or https). {urlCount > 1 ? `${urlCount} URLs.` : ''} Ctrl+Enter adds.
          </span>
        </div>
        <textarea
          id={ids.urls}
          class="input mono"
          rows="4"
          bind:value={urls}
          data-autofocus
          spellcheck="false"
          autocomplete="off"
          placeholder="https://example.com/file.zip"
          aria-invalid={errors.urls ? 'true' : undefined}
          aria-describedby={describedBy(`${ids.urls}-hint`, 'urls')}
          onkeydown={(e) => {
            if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
              e.preventDefault();
              void onsubmit();
            }
          }}
        ></textarea>
        {#if errors.urls}
          <p id="{ids.urls}-error" class="error-text">{errors.urls}</p>
        {/if}
        {#if manifestInFiles}
          <p class="note info suggest">
            This address is a streaming playlist.
            <button type="button" class="btn link" onclick={() => setMode('stream')}>Download it as a video</button>
            to save the video rather than the playlist file.
          </p>
        {/if}
      </div>
    {:else}
      <!-- the address and what is happening to it: one part, so that the split layout's rows
           stay the same while "Getting video info…" shows (the card stays over the qualities) -->
      <div class="source">
        <div class="field">
          <label for={ids.stream} class="sr-only">Video page or stream address</label>
          <div class="address" class:found={info !== null}>
            <span class="lead" aria-hidden="true"><Icon name="globe" size={16} /></span>
            <input
              id={ids.stream}
              class="input mono"
              bind:value={streamUrl}
              data-autofocus
              inputmode="url"
              spellcheck="false"
              autocomplete="off"
              placeholder="https://www.youtube.com/watch?v=…"
              aria-invalid={errors.url ? 'true' : undefined}
              aria-describedby={describedBy(info ? `${ids.stream}-found` : `${ids.stream}-hint`, 'url')}
            />
            {#if info}
              <span class="found-mark" id="{ids.stream}-found"><Icon name="check" size={14} />Found</span>
            {/if}
          </div>
          {#if !info}
            <p id="{ids.stream}-hint" class="hint">
              A video page (YouTube and the other sites yt-dlp supports: one MP4 file in the quality you choose), or an HLS
              (.m3u8) or DASH (.mpd) manifest. DRM-protected and live videos cannot be downloaded.
            </p>
          {/if}
          {#if errors.url}
            <p id="{ids.stream}-error" class="error-text">{errors.url}</p>
          {/if}
        </div>
        {#if busy !== null && origin === 'site'}
          <div class="waiting" aria-live="polite">
            {#if settingUp}
              <ProgressBar fraction={setupFraction} state="downloading" label="Downloading yt-dlp and Deno" />
              <!-- the bar shows the percentage: the line names only what downloads -->
              <p>{installText(tools?.status ?? null, setupFraction, false)}</p>
              <p class="hint">
                The first time, Downex downloads yt-dlp (from github.com/yt-dlp) and Deno (from github.com/denoland/deno), which
                read the page.
              </p>
            {:else}
              <p class="getting">
                <span class="spinner" aria-hidden="true"></span>Getting video info…
                {#if elapsed > 0}<span class="elapsed tnum" aria-hidden="true">{elapsed} s</span>{/if}
              </p>
            {/if}
          </div>
        {/if}
      </div>
      {#if info}
        <div class="card-area"><VideoCard {info} url={streamUrl} noCookies picture={chosenPicture} /></div>
        <div class="quality-area"><MediaPicker {info} bind:pick disabled={busy !== null} part="video" /></div>
      {/if}
    {/if}

    <div class="pair">
      {#if mode === 'stream' && info}
        <MediaPicker {info} bind:pick disabled={busy !== null} part="audio" />
      {/if}
      <div class="field">
        <div class="label-row">
          <label for={ids.dir}>Save to<span class="sr-only">{' folder'}</span></label>
          {#if free !== null}
            <span class="hint free tnum" class:tight title={tight ? 'Not enough free space for this video' : undefined}
              >{formatBytes(free)} free</span
            >
          {/if}
        </div>
        <div class="folder">
          <span class="lead" aria-hidden="true"><Icon name="folder" size={16} /></span>
          <input
            id={ids.dir}
            class="input mono"
            bind:value={dir}
            placeholder={defaults.dir || 'Default download folder'}
            spellcheck="false"
            autocomplete="off"
            aria-invalid={errors.dir ? 'true' : undefined}
            aria-describedby={describedBy(`${ids.dir}-hint`, 'dir')}
          />
          <button type="button" class="btn ghost sm change" onclick={browse} aria-label="Change folder" title="Choose a folder">
            Change
          </button>
        </div>
        <p id="{ids.dir}-hint" class="sr-only">Leave empty to use the default folder.</p>
        {#if errors.dir}
          <p id="{ids.dir}-error" class="error-text">{errors.dir}</p>
        {/if}
      </div>
    </div>

    <div class="more" class:open={moreOpen}>
      <button
        type="button"
        class="more-toggle"
        aria-expanded={moreOpen}
        aria-controls={ids.more}
        aria-labelledby="{ids.more}-label"
        aria-describedby={moreOpen ? undefined : `${ids.more}-summary`}
        onclick={() => (moreOpen = !moreOpen)}
      >
        <span class="chev" aria-hidden="true"><Icon name="chevron-right" size={14} /></span>
        <span class="more-label" id="{ids.more}-label">More options</span>
        <span class="more-summary" id="{ids.more}-summary" hidden={moreOpen} title={moreSummary}>{moreSummary}</span>
      </button>
      <div id={ids.more} class="more-body" class:files={mode === 'files'} hidden={!moreOpen}>
        {#if mode === 'files'}
          <div class="field">
            <label for={ids.filename}>File name</label>
            <input
              id={ids.filename}
              class="input"
              bind:value={filename}
              disabled={urlCount > 1}
              placeholder={urlCount > 1 ? 'Taken from each URL' : 'From the server or the URL'}
              spellcheck="false"
              autocomplete="off"
              aria-invalid={errors.filename ? 'true' : undefined}
              aria-describedby={describedBy(urlCount > 1 ? `${ids.filename}-hint` : null, 'filename')}
            />
            {#if urlCount > 1}
              <p id="{ids.filename}-hint" class="hint">A file name can only be set for a single URL.</p>
            {/if}
            {#if errors.filename}
              <p id="{ids.filename}-error" class="error-text">{errors.filename}</p>
            {/if}
          </div>

          <div class="row">
            <div class="field">
              <div class="label-row">
                <label for={ids.connections}>Connections</label>
                <span id="{ids.connections}-hint" class="hint">{LIMITS.minConnections}–{LIMITS.maxConnections}</span>
              </div>
              <input
                id={ids.connections}
                class="input tnum"
                type="number"
                inputmode="numeric"
                min={LIMITS.minConnections}
                max={LIMITS.maxConnections}
                step="1"
                value={connections}
                oninput={(e) => (connections = e.currentTarget.value)}
                aria-invalid={errors.connections ? 'true' : undefined}
                aria-describedby={describedBy(`${ids.connections}-hint`, 'connections')}
              />
              {#if errors.connections}
                <p id="{ids.connections}-error" class="error-text">{errors.connections}</p>
              {/if}
            </div>

            <div class="field">
              <label for={ids.speed}>Speed limit</label>
              <div class="with-unit">
                <input
                  id={ids.speed}
                  class="input tnum"
                  inputmode="decimal"
                  bind:value={speedValue}
                  placeholder="No limit"
                  autocomplete="off"
                  aria-invalid={errors.speed ? 'true' : undefined}
                  aria-describedby={describedBy(null, 'speed')}
                />
                <div class="unit">
                  <Select id={ids.unit} bind:value={speedUnit} options={unitOptions} ariaLabel="Speed limit unit" />
                </div>
              </div>
              {#if errors.speed}
                <p id="{ids.speed}-error" class="error-text">{errors.speed}</p>
              {/if}
            </div>
          </div>
        {/if}

        {#if mode === 'stream' || offerQueues}
          <div class="row" class:single={mode !== 'stream' || !offerQueues} class:name-queue={mode === 'stream' && offerQueues}>
            {#if mode === 'stream'}
            <div class="field">
              <label for={ids.filename}>File name</label>
              <input
                id={ids.filename}
                class="input"
                bind:value={filename}
                placeholder={info?.title
                  ? escapeBidi(info.title)
                  : origin === 'site'
                    ? 'From the video title'
                    : 'From the stream title or address'}
                spellcheck="false"
                autocomplete="off"
                aria-invalid={errors.filename ? 'true' : undefined}
                aria-describedby={describedBy(`${ids.filename}-hint`, 'filename')}
              />
              <p id="{ids.filename}-hint" class="hint">The extension (.mp4) is added for you.</p>
              {#if errors.filename}
                <p id="{ids.filename}-error" class="error-text">{errors.filename}</p>
              {/if}
            </div>
            {/if}
            {#if offerQueues}
            <div class="field">
              <div class="label-row">
                <label for={ids.queue}>Queue</label>
              </div>
              <Select id={ids.queue} bind:value={queueId} options={queueOptions} ariaDescribedby="{ids.queue}-hint" />
              <p id="{ids.queue}-hint" class="hint">
                {chosenQueue && !chosenQueue.running
                  ? 'This queue is stopped: the download waits until it starts.'
                  : 'Downloads start in the order of their queue.'}
              </p>
            </div>
            {/if}
          </div>
        {/if}

        <label class="check">
          <input type="checkbox" bind:checked={startPaused} />
          Add paused (start later)
        </label>
      </div>
    </div>

    {#if mode === 'files' && failures.length > 0}
      <div class="note fail failures" role="alert">
        <p>
          {failures.length === 1 ? 'This URL could not be added:' : `${failures.length} URLs could not be added:`}
        </p>
        <ul>
          {#each failures.slice(0, 5) as f (f.url)}
            <li><bdi class="mono">{escapeBidi(f.url)}</bdi> — <bdi>{escapeBidi(f.error)}</bdi></li>
          {/each}
        </ul>
      </div>
    {:else if mode === 'stream' && streamFailure}
      <div class="note fail failures" role="alert">
        <p><bdi>{escapeBidi(streamFailure)}</bdi></p>
        {#if failureSettings && onopensettings}
          <div>
            <button type="button" class="btn sm" onclick={onopensettings}>Open settings</button>
          </div>
        {/if}
      </div>
    {/if}
  </div>

  {#snippet footerStart()}
    {#if mode === 'stream'}
      {#if streamSummary}
        <span class="sum"
          ><b>{streamSummary.head}</b>{#if streamSummary.sound}{' '}{streamSummary.sound}{/if}{#if streamSummary.size}{' · '}{streamSummary.prefix}<b
              >{streamSummary.size}</b
            >{/if}</span
        >
      {/if}
    {:else if urlCount > 0}
      <span class="sum"
        ><b>{urlCount === 1 ? '1 link' : `${urlCount} links`}</b> · {conns} connections{#if queueName}{' · '}{queueName}{/if}</span
      >
    {/if}
  {/snippet}

  {#snippet footer()}
    {#if stoppable}
      <button type="button" class="btn" onclick={stop}>Cancel</button>
    {:else}
      <button type="button" class="btn" onclick={onclose} disabled={busy !== null}>Cancel</button>
    {/if}
    <button type="submit" class="btn primary" disabled={busy !== null}
      >{#if submitIcon}<Icon name={submitIcon} size={16} />{/if}{submitLabel}</button
    >
  {/snippet}
</Dialog>

<style>
  .form {
    display: grid;
    gap: var(--sp-7);
    min-inline-size: 0;
  }
  .source {
    display: grid;
    gap: var(--sp-7);
    min-inline-size: 0;
  }
  .address,
  .folder {
    position: relative;
    display: flex;
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
  .address .input,
  .folder .input {
    padding-inline-start: 34px;
  }
  .address.found .input {
    padding-inline-end: 82px;
  }
  .found-mark {
    position: absolute;
    inset-inline-end: 11px;
    display: inline-flex;
    align-items: center;
    gap: 4px;
    color: var(--accent-text);
    font-size: var(--type-small);
    font-weight: 600;
    pointer-events: none;
  }
  .found-mark :global(.icon) {
    stroke-width: 2.2;
  }
  .folder .input {
    padding-inline-end: 76px;
  }
  .change {
    position: absolute;
    inset-inline-end: 5px;
    color: var(--muted);
  }
  .pair {
    display: flex;
    gap: var(--sp-7);
    align-items: start;
    min-inline-size: 0;
  }
  .pair > :global(*) {
    flex: 1 1 0;
    min-inline-size: 0;
  }
  .free {
    white-space: nowrap;
  }
  .free.tight {
    color: var(--warn);
    font-weight: 600;
  }
  .row {
    display: grid;
    grid-template-columns: minmax(0, 0.8fr) minmax(0, 1.4fr);
    gap: var(--sp-7);
    align-items: start;
  }
  .row.name-queue {
    grid-template-columns: minmax(0, 1.5fr) minmax(0, 1fr);
  }
  .row.single {
    grid-template-columns: minmax(0, 1fr);
  }
  .with-unit {
    display: flex;
    gap: var(--sp-4);
  }
  .with-unit .input {
    flex: 1;
    min-inline-size: 0;
  }
  .unit {
    flex: 0 0 6.75rem;
  }
  .suggest {
    margin-block-start: var(--sp-2);
  }
  .more {
    display: grid;
    gap: var(--sp-6);
    min-inline-size: 0;
  }
  .more-toggle {
    display: flex;
    align-items: center;
    gap: 6px;
    justify-self: start;
    max-inline-size: 100%;
    min-inline-size: 0;
    padding: 4px 8px 4px 2px;
    border: 0;
    border-radius: var(--r-sm);
    background: none;
    color: var(--ink-2);
    cursor: pointer;
  }
  .more-toggle:hover {
    background: var(--hover);
  }
  .chev {
    display: grid;
    flex: none;
    color: var(--muted);
    transition: transform var(--dur-3) var(--ease-out);
  }
  .more.open .chev {
    transform: rotate(90deg);
  }
  :global([dir='rtl']) .more:not(.open) .chev {
    transform: scaleX(-1);
  }
  .more-label {
    flex: none;
    font-weight: 600;
  }
  .more-summary {
    min-inline-size: 0;
    margin-inline-start: 4px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--faint);
    font-size: var(--type-small);
  }
  .more-body {
    display: grid;
    gap: var(--sp-7);
    padding: var(--sp-7);
    border: 1px solid var(--line);
    border-radius: var(--r-xl);
    background: var(--surface-2);
  }
  .more-body[hidden] {
    display: none;
  }
  /* Files: the four fields two to a row, in the order Tab takes them (File name, Connections;
     Speed limit, Queue), then "Add paused" */
  .more-body.files {
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--sp-6) var(--sp-7);
    align-items: start;
  }
  .more-body.files :global(.row) {
    display: contents;
  }
  .more-body.files > .check {
    grid-column: 1 / -1;
  }
  @container dialog (max-width: 32rem) {
    .more-body.files {
      grid-template-columns: minmax(0, 1fr);
    }
  }
  /* Video or stream, its qualities found (the dialog is 56rem then): the address and the card
     over both columns, then the qualities (start) beside the audio track, the folder and More
     options (end), in the order Tab takes them; as Confirm download (§4.6, §4.7). */
  .form.split,
  .split > .source {
    gap: var(--sp-6);
  }
  /* rows: the address (and its state), the card, then the qualities beside the audio track and
     folder and More options; a failure after them (an implicit row: no gap when there is none) */
  @container dialog (min-width: 42rem) {
    .form.split {
      grid-template-columns: minmax(0, 1fr) minmax(0, 1.25fr);
      grid-template-rows: auto auto auto minmax(0, 1fr);
      column-gap: var(--sp-8);
      align-items: start;
    }
    .split > :global(*) {
      grid-column: 1 / -1;
    }
    .split > .source {
      grid-row: 1;
    }
    .split > .card-area {
      grid-row: 2;
    }
    .split > .quality-area {
      grid-column: 1;
      grid-row: 3 / span 2;
    }
    .split > .pair {
      grid-column: 2;
      grid-row: 3;
      flex-direction: column;
      align-items: stretch;
      gap: var(--sp-6);
    }
    .split > .more {
      grid-column: 2;
      grid-row: 4;
    }
    .split > .pair > :global(*) {
      flex: none;
    }
  }
  .card-area,
  .quality-area {
    min-inline-size: 0;
  }
  .failures {
    display: grid;
    gap: var(--sp-2);
    color: var(--danger);
  }
  .failures ul {
    margin: 0;
    padding-inline-start: var(--sp-8);
  }
  .failures .mono {
    word-break: break-all;
  }
  .waiting {
    display: grid;
    gap: var(--sp-3);
    padding: var(--sp-5) var(--sp-6);
    border: 1px solid var(--line);
    border-radius: var(--r-xl);
    background: var(--surface-2);
    font-size: var(--type-cell);
  }
  .getting {
    display: flex;
    align-items: center;
    gap: var(--sp-4);
    color: var(--ink-2);
    font-weight: 500;
  }
  .elapsed {
    color: var(--muted);
    font-weight: 400;
  }
  .sum {
    display: block;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  @media (prefers-reduced-motion: reduce) {
    .chev {
      transition: none;
    }
  }
</style>
