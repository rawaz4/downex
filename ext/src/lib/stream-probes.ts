// The `probeStream` cache of the background (VIDEO-DETECT-SPEC §3.7): the MP4 qualities of the
// HLS/DASH streams a tab loaded, asked when a panel opens or the popup expands a stream (always a
// user's click). One probe per stream at a time, at most STREAM_PROBE_CONCURRENCY in the whole
// browser; answers and refusals of the stream kept STREAM_KEEP_MS for STREAM_CACHE_SIZE streams.
// The headers are the stream hand-off's (M5 rules). Pure: the client is injected.

import type { MediaItem } from './media';
import { type MediaFieldsDeps, prepareAddMediaFields } from './media-handoff';
import type { VideoTaskState } from './messages';
import {
  type Outcome,
  type ProbeStreamFields,
  type RequestOptions,
  type Timers,
  type VideoInfoResult,
  daemonCode,
  systemTimers,
} from './protocol';
import { VideoProbes } from './video-handoff';

/** How long a probe may take: the relay keeps 200 ms of it, the daemon answers before. */
export const STREAM_PROBE_DEADLINE_MS = 28_000;
/** Probes sent to Downex at once; more wait for a free one. */
export const STREAM_PROBE_CONCURRENCY = 2;
/** Answers and refusals are kept this long (VideoProbes' 10 minutes) … */
export const STREAM_KEEP_MS = 10 * 60_000;
/** … for at most this many streams. */
export const STREAM_CACHE_SIZE = 50;

/** Refusals that hold for the stream whatever asks again soon: kept, and the stream left out. */
export const KEPT_STREAM_REFUSALS: ReadonlySet<string> = new Set(['encrypted', 'live', 'media_unsupported', 'media_invalid']);

/** The stream's refusal code (`encrypted`, `live`, `media_unsupported`, `media_invalid`), if it is one. */
export function streamRefusal(o: Outcome<unknown> | undefined): string | undefined {
  if (!o || o.ok || o.error.code !== 'rejected') return undefined;
  const code = daemonCode(o.error);
  return KEPT_STREAM_REFUSALS.has(code) ? code : undefined;
}

/** Whether a probe's answer is kept: its qualities, or a refusal of the stream. */
export function keepStreamProbe(o: Outcome<VideoInfoResult>): boolean {
  return o.ok || streamRefusal(o) !== undefined;
}

/** The cache key of a stream: its cookie store and manifest URL. */
export function streamProbeKey(item: Pick<MediaItem, 'storeId' | 'url'>): string {
  return `${item.storeId ?? ''}\n${item.url}`;
}

export interface StreamSender {
  probeStream(fields: ProbeStreamFields, options?: RequestOptions<VideoInfoResult>): Promise<Outcome<VideoInfoResult>>;
}

export interface StreamProbeDeps extends MediaFieldsDeps {
  client: StreamSender;
  probes?: VideoProbes;
  timers?: Timers;
  now?: () => number;
}

export interface StreamProber {
  readonly probes: VideoProbes;
  /** The MP4 qualities of a stream, through the cache; never rejects. */
  probe(item: MediaItem): Promise<Outcome<VideoInfoResult>>;
  /** What the cache knows of a stream: a probe running, or the kept answer. */
  state(item: Pick<MediaItem, 'storeId' | 'url'>): VideoTaskState<VideoInfoResult> | undefined;
  /** The stream was refused (protected, live, no MP4 video, invalid): panels and the badge leave it out. */
  refused(item: Pick<MediaItem, 'storeId' | 'url'>): boolean;
}

export function createStreamProber(deps: StreamProbeDeps): StreamProber {
  const probes =
    deps.probes ??
    new VideoProbes({
      concurrency: STREAM_PROBE_CONCURRENCY,
      maxEntries: STREAM_CACHE_SIZE,
      keep: keepStreamProbe,
      ...(deps.now ? { now: deps.now } : {}),
    });
  const timers = deps.timers ?? systemTimers;

  const send = async (item: MediaItem): Promise<Outcome<VideoInfoResult>> => {
    const built = await prepareAddMediaFields(item, deps, timers);
    if (!built.ok) return built;
    const { manifestUrl, headers, pageUrl } = built.value;
    const fields: ProbeStreamFields = { manifestUrl, headers, deadlineMs: STREAM_PROBE_DEADLINE_MS };
    if (pageUrl) fields.pageUrl = pageUrl;
    return deps.client.probeStream(fields, { timeoutMs: STREAM_PROBE_DEADLINE_MS });
  };

  const state = (item: Pick<MediaItem, 'storeId' | 'url'>) => probes.state(streamProbeKey(item));

  return {
    probes,
    probe: (item) => probes.run(streamProbeKey(item), () => send(item)),
    state,
    refused: (item) => {
      const s = state(item);
      return s !== undefined && 'result' in s && streamRefusal(s.result) !== undefined;
    },
  };
}
