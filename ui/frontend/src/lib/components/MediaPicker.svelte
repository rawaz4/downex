<script lang="ts" module>
  import type { MediaInfo, MediaTrack } from '../api';
  import { codecName, codecNames, pickSize, withVideo, type TrackPick } from '../media';
  import { formatBitrate, formatBytes } from '../format';

  /** Codecs that are smaller than H.264 for the same picture but need a recent player. */
  const MODERN = new Set(['AV1', 'HEVC', 'VP9']);

  /**
   * The line under the cards when they mix H.264 with a newer codec (DESIGN-SYSTEM.md §3.19),
   * naming the codecs actually present; "" when they do not mix. Short, so that it keeps to one
   * line in the quality column of a small window.
   */
  function codecNote(tracks: readonly Pick<MediaTrack, 'codecs'>[]): string {
    const names = new Set(tracks.map((t) => codecName(t.codecs.split(',')[0] ?? '')).filter((n) => n !== ''));
    if (!names.has('H.264')) return '';
    const modern = [...names].filter((n) => MODERN.has(n)).sort();
    if (modern.length === 0) return '';
    const list = modern.length === 1 ? (modern[0] ?? '') : `${modern.slice(0, -1).join(', ')} and ${modern.at(-1) ?? ''}`;
    return `${list} ${modern.length === 1 ? 'is' : 'are'} smaller; H.264 plays everywhere.`;
  }

  /**
   * A card's size: the file the quality makes (with the audio it is paired with), "~" when it
   * is an estimate (the site's, or downexd's from the bitrate); else the bitrate; "" unknown.
   */
  function cardSize(info: Pick<MediaInfo, 'tracks' | 'durationMs'> & Partial<Pick<MediaInfo, 'origin'>>, pick: TrackPick, t: MediaTrack): string {
    const size = pickSize(info, withVideo(info, pick, t.id));
    if (size.kind === 'exact') return formatBytes(size.bytes);
    if (size.kind === 'approx' || size.kind === 'estimate') return `~${formatBytes(size.bytes)}`;
    return formatBitrate(t.bandwidth);
  }

  /** What an audio track is besides its name: "AAC 129 kbit/s" (a page's), "128 kbit/s · mp4a.40.2" (a manifest's). */
  function audioDetail(t: Pick<MediaTrack, 'codecs' | 'bandwidth'>, site: boolean): string {
    const rate = formatBitrate(t.bandwidth);
    if (site) return [t.codecs ? codecNames(t.codecs) : '', rate].filter(Boolean).join(' ');
    return [rate, t.codecs].filter(Boolean).join(' · ');
  }
</script>

<script lang="ts">
  // The quality picker (DESIGN-SYSTEM.md §3.19): one card per quality class (never invented or
  // merged: downexd's list in its order and names), each a label around a visually hidden
  // native radio, so the arrow keys move between them as in any radio group; and the audio
  // track as a custom Select. The Add dialog places the two parts apart (`part`).
  import {
    audioOnlyOffered,
    audioTrackLabel,
    audioTrackTitle,
    audioTracks,
    isSite,
    needsAudio,
    NO_SOUND,
    noSound,
    qualityName,
    trackById,
    trackSizeText,
    videoPictureText,
    videoTracks,
  } from '../media';
  import { escapeBidi } from '../text';
  import Icon from './Icon.svelte';
  import Select, { type SelectOption } from './Select.svelte';
  import { shortClassName } from './VideoCard.svelte';

  interface Props {
    /** The stream or web page's video (a probe result, or a media download's info). */
    info: MediaInfo;
    /** The chosen tracks (see defaultPick); a muxed video track needs no audio track. */
    pick: TrackPick;
    disabled?: boolean;
    /** all: the cards, then the audio track; video: the cards; audio: the audio track. */
    part?: 'all' | 'video' | 'audio';
  }

  let { info, pick = $bindable(), disabled = false, part = 'all' }: Props = $props();

  const uid = $props.id();
  const site = $derived(isSite(info));
  const videos = $derived(videoTracks(info.tracks, site, info.selectedVideo));
  const audios = $derived(audioTracks(info.tracks, site));
  // Only where "" (the daemon's pick) means no video: see audioOnlyOffered.
  const offerNoVideo = $derived(audioOnlyOffered(info));
  const videoOnly = $derived(audios.length > 0 && needsAudio(info, pick.video));
  /** The audio is chosen only when there is a choice (SITE-MEDIA-SPEC §5.3). */
  const showAudio = $derived(videoOnly && audios.length > 1);
  /** The one audio track a video-only quality is paired with. */
  const onlyAudio = $derived(videoOnly && audios.length === 1 ? audios[0] : undefined);
  const chosen = $derived(trackById(info, pick.video));
  const muxedChosen = $derived(videos.length > 0 && audios.length > 0 && chosen?.kind === 'muxed');
  /** A chosen video without sound: the page or stream has none (noSound). */
  const silentChosen = $derived(chosen !== undefined && noSound(info, chosen));
  const note = $derived(codecNote(videos));
  const showVideo = $derived(part !== 'audio');
  const showAudioPart = $derived(part !== 'video');

  /** One card per class: its name split into the class and the standard suffix. */
  const cards = $derived(
    videos.map((t) => {
      const name = qualityName(t) || (t.kind === 'muxed' ? 'Video and audio' : 'Video');
      const cls = shortClassName(name) || name;
      const suffix = name.slice(cls.length).trim();
      const size = cardSize(info, pick, t);
      const codec = t.codecs ? codecName(t.codecs.split(',')[0] ?? '') : '';
      const codecs = codecNames(t.codecs);
      const picture = videoPictureText(t, site);
      const rate = formatBitrate(t.bandwidth);
      return {
        t,
        cls,
        suffix,
        codec,
        size,
        label: [name, codecs, noSound(info, t) ? NO_SOUND : '', size].filter(Boolean).join(' · '),
        // the whole name first: a narrow column cuts its suffix ("(2160p) 60fps HDR")
        tip: [name, picture, site ? '' : rate, t.kind === 'muxed' ? 'with its sound' : noSound(info, t) ? NO_SOUND : '']
          .filter(Boolean)
          .join(' · '),
      };
    }),
  );

  const noVideoSize = $derived.by(() => {
    if (!offerNoVideo) return '';
    const s = pickSize(info, { video: '', audio: pick.audio || info.selectedAudio });
    return s.kind === 'exact' ? formatBytes(s.bytes) : s.bytes > 0 ? `~${formatBytes(s.bytes)}` : '';
  });

  const audioOptions: SelectOption<string>[] = $derived(
    audios.map((t) => ({
      value: t.id,
      label: escapeBidi(audioTrackTitle(t, site)),
      detail: audioDetail(t, site) || undefined,
      meta: site ? trackSizeText(t) || undefined : undefined,
      title: escapeBidi(audioTrackLabel(t, site)),
    })),
  );

  const hint = $derived(videos.length === 1 ? 'The only MP4 quality offered' : 'One MP4 per resolution, best first');

  function choose(video: string) {
    pick = withVideo(info, pick, video);
  }
</script>

{#snippet qualityCards()}
  <div class="quality" role="group" aria-labelledby="{uid}-title">
    <div class="label-row">
      <span class="label" id="{uid}-title">Quality</span>
      <span class="hint" id="{uid}-hint">{hint}</span>
    </div>
    <div
      class="cards"
      role="radiogroup"
      aria-label="Video quality"
      aria-describedby="{uid}-hint{note ? ` ${uid}-note` : ''}"
      aria-disabled={disabled ? 'true' : undefined}
    >
      {#each cards as c (c.t.id)}
        <label class="qcard" class:on={pick.video === c.t.id} class:off={disabled} title={c.tip || undefined}>
          <input
            class="sr-only"
            type="radio"
            name="{uid}-video"
            value={c.t.id}
            checked={pick.video === c.t.id}
            {disabled}
            aria-label={escapeBidi(c.label)}
            onchange={() => choose(c.t.id)}
          />
          <span class="line1"
            ><bdi class="cls">{escapeBidi(c.cls)}</bdi>{#if c.suffix}<span class="suffix">{escapeBidi(c.suffix)}</span>{/if}</span
          >
          <span class="line2">
            {#if c.codec}<span class="codec">{c.codec}</span>{/if}
            <span class="size tnum">{c.size}</span>
          </span>
          {#if pick.video === c.t.id}<span class="tick" aria-hidden="true"><Icon name="check" size={12} /></span>{/if}
        </label>
      {/each}
      {#if offerNoVideo}
        <label class="qcard" class:on={pick.video === ''} class:off={disabled}>
          <input
            class="sr-only"
            type="radio"
            name="{uid}-video"
            value=""
            checked={pick.video === ''}
            {disabled}
            aria-label="No video (audio only)"
            onchange={() => choose('')}
          />
          <span class="line1"><span class="cls">Audio only</span><span class="suffix">(no video)</span></span>
          <span class="line2"><span class="size tnum">{noVideoSize}</span></span>
          {#if pick.video === ''}<span class="tick" aria-hidden="true"><Icon name="check" size={12} /></span>{/if}
        </label>
      {/if}
    </div>
    {#if note}
      <p class="note-line" id="{uid}-note" title={note}><Icon name="info" size={14} /><span>{note}</span></p>
    {/if}
  </div>
{/snippet}

{#snippet audioPart()}
  {#if showAudio}
    <div class="field audio">
      <label for="{uid}-audio">Audio track</label>
      <Select
        id="{uid}-audio"
        value={pick.audio}
        options={audioOptions}
        icon="headphones"
        {disabled}
        onchange={(v) => (pick = { ...pick, audio: v })}
      />
    </div>
  {:else if onlyAudio || muxedChosen || silentChosen}
    <div class="field audio">
      <span class="label">Audio track</span>
      <p class="static">
        <Icon name="headphones" size={16} />
        {#if onlyAudio}
          {@const detail = audioDetail(onlyAudio, site)}
          <span class="sound" title={escapeBidi(audioTrackLabel(onlyAudio, site))}
            ><bdi>{escapeBidi(audioTrackTitle(onlyAudio, site))}</bdi>{#if detail}<span class="detail">{` · ${detail}`}</span>{/if}</span
          >
        {:else if silentChosen}
          <span>This video has no sound.</span>
        {:else}
          <span>This video quality includes its sound.</span>
        {/if}
      </p>
    </div>
  {/if}
{/snippet}

{#if part === 'all'}
  <div class="picker">
    {#if videos.length > 0}{@render qualityCards()}{/if}
    {@render audioPart()}
    {#if videos.length === 0 && audios.length === 0}
      <p class="hint">The stream offers no qualities to choose from.</p>
    {/if}
  </div>
{:else if showVideo}
  {#if videos.length > 0}
    {@render qualityCards()}
  {:else if audios.length === 0}
    <p class="hint">The stream offers no qualities to choose from.</p>
  {/if}
{:else if showAudioPart}
  {@render audioPart()}
{/if}

<style>
  .picker {
    display: grid;
    gap: var(--sp-7);
  }
  /* The quality section is a size container of its own: its cards follow the room the section
     has, which is a column of its own in the wide Confirm dialog (§3.19), not the dialog's. */
  .quality {
    container: quality / inline-size;
    display: grid;
    gap: var(--sp-3);
    min-inline-size: 0;
  }
  .cards {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: var(--sp-4);
  }
  /* compact (0.8.2 fit rule): two short lines, still at least 44 px high to click */
  .qcard {
    position: relative;
    display: grid;
    align-content: center;
    gap: 4px;
    min-inline-size: 0;
    min-block-size: 2.75rem;
    padding: 6px 10px;
    border: 1px solid var(--line-strong);
    border-radius: var(--r-lg);
    background: var(--raised);
    cursor: pointer;
    transition:
      border-color var(--dur-2) ease,
      background-color var(--dur-2) ease,
      box-shadow var(--dur-2) ease;
  }
  .qcard:hover:not(.on, .off) {
    border-color: var(--line-field);
    background: var(--hover);
  }
  .qcard.on {
    border-color: var(--accent);
    background: var(--accent-soft);
    box-shadow: inset 0 0 0 1px var(--accent);
  }
  .qcard.off {
    cursor: default;
  }
  .qcard.off:not(.on) {
    opacity: 0.7;
  }
  .qcard:has(input:focus-visible) {
    outline: 2px solid var(--focus);
    outline-offset: 2px;
  }
  .line1 {
    display: flex;
    align-items: baseline;
    gap: 5px;
    min-inline-size: 0;
    padding-inline-end: 16px;
    white-space: nowrap;
  }
  .cls {
    flex: none;
    color: var(--ink);
    font: 650 var(--type-lead) / 1.2 var(--font-display);
    letter-spacing: -0.01em;
  }
  .suffix {
    min-inline-size: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    color: var(--muted);
    font-size: var(--type-small);
  }
  .line2 {
    display: flex;
    align-items: center;
    gap: var(--sp-4);
    min-block-size: 16px;
  }
  .qcard.on .codec {
    background: var(--raised);
  }
  .size {
    margin-inline-start: auto;
    color: var(--ink-2);
    font-size: var(--type-small);
    font-weight: 600;
    white-space: nowrap;
  }
  .tick {
    position: absolute;
    inset-block-start: 8px;
    inset-inline-end: 8px;
    display: grid;
    place-items: center;
    inline-size: 16px;
    block-size: 16px;
    border-radius: 50%;
    background: var(--accent);
    color: var(--accent-on);
  }
  .tick :global(.icon) {
    stroke-width: 2.6;
  }
  .note-line {
    display: flex;
    align-items: center;
    gap: 6px;
    color: var(--faint);
    font-size: var(--type-small);
  }
  .note-line :global(.icon) {
    flex: none;
  }
  /* one line in any column: cut with an ellipsis where it must be, whole in its tooltip */
  .note-line > span {
    min-inline-size: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .audio {
    min-inline-size: 0;
  }
  .static {
    display: flex;
    align-items: center;
    gap: 8px;
    min-block-size: 36px;
    padding-inline: 11px;
    border: 1px solid var(--line);
    border-radius: 9px;
    background: var(--surface-2);
    color: var(--ink);
    font-size: var(--type-body);
  }
  .static :global(.icon) {
    color: var(--faint);
  }
  .static > span {
    min-inline-size: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .static .detail {
    color: var(--muted);
  }
  /* the section's width (a column of the Confirm dialog, or the Add dialog's body), not the
     window's: two to a row in a narrow column, four once there is room (the Add dialog widens
     for the qualities), so a page's usual seven or eight take two rows */
  @container quality (max-width: 27.99rem) {
    .cards {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
  }
  @container quality (min-width: 40rem) {
    .cards {
      grid-template-columns: repeat(4, minmax(0, 1fr));
    }
  }
  @media (forced-colors: active) {
    .qcard {
      border-color: ButtonText;
    }
    .qcard.on {
      border-color: Highlight;
      outline: 2px solid Highlight;
    }
  }
</style>
