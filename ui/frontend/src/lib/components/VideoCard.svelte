<script lang="ts" module>
  import type { MediaInfo } from '../api';
  import { formatLabel, isSite, qualityName, videoTracks } from '../media';

  /** The short class of a quality: "4K (2160p)" → "4K", "1080p (Full HD) 60fps" → "1080p". */
  export function shortClassName(name: string): string {
    const cut = name.indexOf(' (');
    return (cut >= 0 ? name.slice(0, cut) : name).trim();
  }

  /** "Up to 4K": the best class a stream or page offers ("" without a named video quality). */
  function bestClassText(info: Pick<MediaInfo, 'tracks' | 'origin' | 'selectedVideo'>): string {
    const best = videoTracks(info.tracks, isSite(info), info.selectedVideo)[0];
    const cls = best ? shortClassName(qualityName(best)) : '';
    return cls ? `Up to ${cls}` : '';
  }

  /** The host of an address for display, without "www." ("" when it does not parse). */
  function displayHost(url: string): string {
    try {
      return new URL(url).hostname.replace(/^www\./i, '');
    } catch {
      return '';
    }
  }

  /** "YouTube · youtube.com"; a manifest's "HLS stream · media.example.org". */
  function siteLine(info: Pick<MediaInfo, 'origin' | 'site' | 'format' | 'pageUrl' | 'manifestUrl'>, url = ''): string {
    const host = displayHost(info.pageUrl || info.manifestUrl || url);
    const name = isSite(info)
      ? info.site.trim() || 'Video page'
      : formatLabel(info.format)
        ? `${formatLabel(info.format)} stream`
        : 'Stream';
    return [name, host].filter(Boolean).join(' · ');
  }
</script>

<script lang="ts">
  // The video card (DESIGN-SYSTEM.md §3.19): a poster placeholder (never a page image: nothing
  // is fetched from the page), the title on up to two lines, then one line with the site (and,
  // where the dialog has no address field of its own, the address) and the fact chips, which
  // wrap under it when the card is narrow. "No cookies sent" is shown only where it is true (the
  // desktop Add dialog never sends cookies; a browser hand-off may carry them, so the
  // confirmation dialog leaves the chip out).
  import { containerLabel } from '../media';
  import { formatDuration } from '../format';
  import { escapeBidi } from '../text';
  import Icon from './Icon.svelte';

  interface Props {
    info: MediaInfo;
    /** Shown when the stream has no title (a proposed file name). */
    fallbackTitle?: string;
    /** The address the card is about, when the info names none (a download's url). */
    url?: string;
    /** The "No cookies sent" chip (only where no cookie is sent). */
    noCookies?: boolean;
    /** A tooltip with the chosen quality's picture ("1920×1080"). */
    picture?: string;
    /**
     * Show `url` in the card, after the site's name, as a read-only field (the full address:
     * selectable, copyable, in the tab order and named `addressLabel`), where the dialog has
     * no address field (Confirm download); its host is then not repeated.
     */
    showAddress?: boolean;
    /** The accessible name of the address ("Page", "Address"). */
    addressLabel?: string;
  }

  let {
    info,
    fallbackTitle = '',
    url = '',
    noCookies = false,
    picture = '',
    showAddress = false,
    addressLabel = 'Address',
  }: Props = $props();

  const title = $derived(info.title.trim() || fallbackTitle.trim() || 'Untitled video');
  const duration = $derived(info.durationMs > 0 ? formatDuration(info.durationMs) : '');
  const best = $derived(bestClassText(info));
  const container = $derived(containerLabel(info.container) || (isSite(info) ? 'MP4' : ''));
  const line = $derived(siteLine(info, url));
  /** The address as the user reads it: a bidi control shown as a marker (text.ts). */
  const shownUrl = $derived(escapeBidi(url));
  /** The site's name alone ("YouTube", "HLS stream"), before the address. */
  const siteName = $derived(siteLine({ ...info, pageUrl: '', manifestUrl: '' }));
</script>

<div class="card" title={picture || undefined}>
  <div class="poster" aria-hidden="true">
    <span class="disc">
      <svg class="play" viewBox="0 0 28 28" width="14" height="14" focusable="false">
        <path d="M9.5 6.6v14.8a1.1 1.1 0 0 0 1.68.93l11.7-7.4a1.1 1.1 0 0 0 0-1.86L11.18 5.67A1.1 1.1 0 0 0 9.5 6.6z" />
      </svg>
    </span>
  </div>
  <div class="about">
    <!-- dir="auto" on the block itself (it skips text inside a <bdi>): a right-to-left title is
         right-aligned; the block isolates it from what follows. Clamped to two lines, it has the
         whole title as its tooltip (over the card's picture). -->
    <p class="title" dir="auto" title={escapeBidi(title)}>{escapeBidi(title)}</p>
    <div class="meta">
      {#if showAddress && url}
        <p class="site with-address">
          <Icon name="globe" size={14} />
          <bdi class="name">{escapeBidi(siteName)}</bdi>
          <span class="sep" aria-hidden="true">·</span>
          <input
            class="address mono"
            type="text"
            readonly
            value={shownUrl}
            title={shownUrl}
            dir="ltr"
            spellcheck="false"
            aria-label={addressLabel}
          />
        </p>
      {:else}
        <p class="site">
          <Icon name="globe" size={14} />
          <bdi>{escapeBidi(line)}</bdi>
        </p>
      {/if}
      <ul class="chips" aria-label="About this video">
        {#if duration}
          <li class="chip"><Icon name="clock" size={12} /><span class="tnum">{duration}</span></li>
        {/if}
        {#if best}<li class="chip">{best}</li>{/if}
        {#if container}<li class="chip">Saved as {container}</li>{/if}
        {#if noCookies}
          <li class="chip"><Icon name="shield" size={12} />No cookies sent</li>
        {/if}
      </ul>
    </div>
  </div>
</div>

<style>
  /* Compact (0.8.2 fit rule): a 96×54 poster (the popup's), the duration once (a chip, not also
     a badge), and the chips on the site's line where they fit. */
  .card {
    display: flex;
    align-items: center;
    gap: var(--sp-6);
    min-inline-size: 0;
    padding: var(--sp-5);
    border: 1px solid var(--line);
    border-radius: var(--r-xl);
    background: var(--surface-2);
  }
  .poster {
    position: relative;
    display: grid;
    flex: none;
    align-self: flex-start;
    place-items: center;
    inline-size: 96px;
    block-size: 54px;
    border-radius: var(--r-md);
    /* the popup's poster: a pale video tint with a play disc, so violet stays an accent and
       never becomes the dialog's largest colour field */
    background: color-mix(in srgb, var(--c-video) 12%, var(--surface));
    box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--c-video) 22%, transparent);
    overflow: hidden;
  }
  .disc {
    display: grid;
    place-items: center;
    inline-size: 28px;
    block-size: 28px;
    border-radius: 50%;
    background: var(--c-video);
  }
  .play {
    margin-inline-start: 2px;
    fill: var(--tile-glyph);
  }
  .about {
    display: grid;
    flex: 1 1 auto;
    gap: 6px;
    min-inline-size: 0;
  }
  .title {
    display: -webkit-box;
    -webkit-box-orient: vertical;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    overflow: hidden;
    color: var(--ink);
    font: 650 var(--type-section) / 1.3 var(--font-display);
    letter-spacing: -0.01em;
    overflow-wrap: anywhere;
    text-align: start;
    user-select: text;
  }
  .meta {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 12px;
    min-inline-size: 0;
  }
  .site {
    display: flex;
    flex: 1 1 12rem;
    align-items: center;
    gap: 6px;
    min-inline-size: 0;
    color: var(--muted);
    font-size: var(--type-cell);
  }
  .site :global(.icon) {
    flex: none;
    color: var(--faint);
  }
  .site bdi {
    min-inline-size: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .site .name {
    flex: none;
    max-inline-size: 50%;
  }
  .sep {
    flex: none;
    color: var(--faint);
  }
  /* the address as text that can be selected, copied and reached with Tab */
  .address {
    flex: 1 1 auto;
    min-inline-size: 4rem;
    block-size: auto;
    margin: 0;
    padding: 1px 3px;
    border: 0;
    border-radius: var(--r-xs);
    background: transparent;
    color: var(--ink-2);
    font: var(--mono-weight) 0.75rem/1.4 var(--font-mono);
    text-overflow: ellipsis;
  }
  .address:hover {
    background: var(--hover);
  }
  .address:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: 0;
  }
  .chips {
    display: flex;
    flex: 0 1 auto;
    flex-wrap: wrap;
    gap: 6px;
    min-inline-size: 0;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .chips .chip {
    background: var(--raised);
  }
  @media (forced-colors: active) {
    .card,
    .poster {
      border: 1px solid CanvasText;
    }
  }
</style>
