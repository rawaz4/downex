import { afterEach, describe, expect, it } from 'vitest';
import { ApiError, EMPTY_SCHEDULE, isDownloadEvent, type Download, type EventBatch, type MediaTrack } from './api';
import { createMockApi, type MockApi, type MockOptions } from './mock';
import { parseStandardName, qualityClass, standardName } from './quality';

const KiB = 1024;
const MiB = 1024 * KiB;
const DIR = '/home/you/Downloads';

let api: MockApi | undefined;

function make(options: MockOptions = {}): MockApi {
  api = createMockApi({ count: 0, seed: 42, autoTick: false, failureRate: 0, defaultDir: DIR, ...options });
  return api;
}

afterEach(() => api?.dispose());

async function get(a: MockApi, id: number): Promise<Download> {
  const d = (await a.list()).find((x) => x.id === id);
  if (!d) throw new Error(`no download ${id}`);
  return d;
}

/** Ticks one simulated second at a time until `done` holds. */
async function runUntil(a: MockApi, id: number, done: (d: Download) => boolean, maxSeconds = 20_000) {
  for (let i = 0; i < maxSeconds; i++) {
    const d = await get(a, id);
    if (done(d)) return d;
    a.tick(1000);
  }
  throw new Error(`condition not reached for download ${id}`);
}

function checkSegments(d: Download) {
  if (d.segments.length === 0) return;
  let next = 0;
  let sum = 0;
  for (const s of d.segments) {
    expect(s.start).toBe(next);
    const length = s.end < 0 ? Number.POSITIVE_INFINITY : s.end - s.start + 1;
    expect(s.received).toBeGreaterThanOrEqual(0);
    expect(s.received).toBeLessThanOrEqual(length);
    next = s.end + 1;
    sum += s.received;
  }
  if (d.size > 0) expect(d.segments.at(-1)?.end).toBe(d.size - 1);
  expect(sum).toBe(d.received);
}

describe('mock daemon: lifecycle', () => {
  it('queues, probes, downloads in segments and completes', async () => {
    const a = make();
    const added = await a.add({ url: `https://downloads.example.com/pub/report.pdf?size=${20 * MiB}` });
    expect(added).toMatchObject({ state: 'queued', filename: 'report.pdf', dir: `${DIR}/Documents`, category: 'Documents', received: 0 });
    expect(added.size).toBeGreaterThan(0);

    a.tick(100);
    expect((await get(a, added.id)).state).toBe('probing');

    const running = await runUntil(a, added.id, (d) => d.state === 'downloading' && d.received > 0);
    expect(running.mode).toBe('ranged');
    expect(running.size).toBe(20 * MiB);
    expect(running.segments.length).toBeGreaterThanOrEqual(8);
    expect(running.segments.length).toBeLessThanOrEqual(64);
    expect(running.connections).toBeGreaterThan(0);
    expect(running.speedBps).toBeGreaterThan(0);
    checkSegments(running);

    const done = await runUntil(a, added.id, (d) => d.state !== 'downloading');
    expect(done.state).toBe('completed');
    expect(done.received).toBe(done.size);
    expect(done.completedAt).toBeGreaterThan(0);
    expect(done.connections).toBe(0);
    expect(done.speedBps).toBe(0);
  });

  it('keeps segment invariants while splitting dynamically', async () => {
    const a = make();
    const d = await a.add({ url: 'https://downloads.example.com/pub/big-movie.mkv', maxConnections: 4 });
    let maxSegments = 0;
    for (let i = 0; i < 400; i++) {
      a.tick(1000);
      const cur = await get(a, d.id);
      checkSegments(cur);
      expect(cur.connections).toBeLessThanOrEqual(4);
      maxSegments = Math.max(maxSegments, cur.segments.length);
      if (cur.state === 'completed') break;
    }
    expect(maxSegments).toBeGreaterThanOrEqual(4);
  });

  it('starts queued downloads FIFO within max_concurrent_downloads', async () => {
    const a = make({ settings: { maxConcurrentDownloads: 2 } });
    const ids: number[] = [];
    for (let i = 0; i < 5; i++) {
      ids.push((await a.add({ url: `https://downloads.example.com/pub/clip-${i}.mp4` })).id);
    }
    a.tick(100);
    const list = await a.list();
    const running = list.filter((d) => d.state === 'probing' || d.state === 'downloading');
    expect(running.map((d) => d.id)).toEqual(ids.slice(0, 2));
    expect(list.filter((d) => d.state === 'queued').map((d) => d.queuePosition)).toEqual([1, 2, 3]);
    const status = await a.status();
    expect(status).toMatchObject({ active: 2, queued: 3 });
  });

  it('pauses without losing progress and resumes from where it stopped', async () => {
    const a = make();
    const d = await a.add({ url: 'https://downloads.example.com/pub/holiday.mp4' });
    const before = await runUntil(a, d.id, (x) => x.received > 4 * MiB);
    const paused = await a.pause(d.id);
    expect(paused).toMatchObject({ state: 'paused', speedBps: 0, connections: 0 });
    a.tick(1000);
    a.tick(1000);
    const still = await get(a, d.id);
    expect(still.received).toBe(paused.received);
    expect(still.received).toBeGreaterThanOrEqual(before.received);

    expect((await a.resume(d.id)).state).toBe('queued');
    const again = await runUntil(a, d.id, (x) => x.state === 'downloading' && x.received > paused.received);
    expect(again.segments.length).toBeGreaterThan(0);
  });

  it('pauses queued downloads and rejects invalid transitions', async () => {
    const a = make({ settings: { maxConcurrentDownloads: 1 } });
    await a.add({ url: 'https://downloads.example.com/pub/a.zip' });
    const second = await a.add({ url: 'https://downloads.example.com/pub/b.zip' });
    expect((await a.pause(second.id)).state).toBe('paused');
    await expect(a.resume(second.id, { maxConnections: 99 })).rejects.toMatchObject({ code: 'invalid_argument' });
    const resumed = await a.resume(second.id, { maxConnections: 4, speedLimit: 64 * KiB });
    expect(resumed).toMatchObject({ state: 'queued', maxConnections: 4, speedLimit: 64 * KiB });
    await expect(a.resume(second.id)).rejects.toMatchObject({ code: 'failed_precondition' });
    await expect(a.pause(12345)).rejects.toMatchObject({ code: 'not_found' });
  });
});

describe('mock daemon: failures', () => {
  it('reports validator_changed and only continues after a restart', async () => {
    const a = make();
    const d = await a.add({ url: `https://downloads.example.com/changed/movie.mp4?size=${30 * MiB}` });
    const failed = await runUntil(a, d.id, (x) => x.state === 'failed');
    expect(failed.errorCode).toBe('validator_changed');
    expect(failed.error).not.toBe('');
    expect(failed.received).toBeGreaterThan(0);

    await a.resume(d.id);
    const again = await runUntil(a, d.id, (x) => x.state === 'failed');
    expect(again.errorCode).toBe('validator_changed');

    const restarted = await a.resume(d.id, { restart: true });
    expect(restarted).toMatchObject({ state: 'queued', received: 0, errorCode: '', error: '' });
    expect(restarted.segments).toEqual([]);
    const done = await runUntil(a, d.id, (x) => x.state === 'completed' || x.state === 'failed');
    expect(done.state).toBe('completed');
  });

  it('fails while probing on an HTTP error', async () => {
    const a = make();
    const d = await a.add({ url: 'https://downloads.example.com/missing/404/file.zip' });
    const failed = await runUntil(a, d.id, (x) => x.state === 'failed');
    expect(failed).toMatchObject({ errorCode: 'http_status', received: 0 });
  });

  it('downloads files of unknown size on a single connection', async () => {
    const a = make();
    const d = await a.add({ url: `https://downloads.example.com/live/unknown-size/capture.ts?size=${10 * MiB}` });
    expect(d.size).toBe(-1);
    const running = await runUntil(a, d.id, (x) => x.state === 'downloading' && x.received > 0);
    expect(running.mode).toBe('single');
    expect(running.segments).toEqual([{ start: 0, end: -1, received: running.received }]);
    const done = await runUntil(a, d.id, (x) => x.state === 'completed');
    expect(done.size).toBe(10 * MiB);
    expect(done.received).toBe(done.size);
    expect(done.segments[0]?.end).toBe(done.size - 1);
  });

  it('validates input like the daemon', async () => {
    const a = make();
    await expect(a.add({ url: 'ftp://example.com/a' })).rejects.toBeInstanceOf(ApiError);
    await expect(a.add({ url: 'ftp://example.com/a' })).rejects.toMatchObject({ code: 'invalid_argument' });
    await expect(a.add({ url: 'https://example.com/a', maxConnections: 33 })).rejects.toMatchObject({ code: 'invalid_argument' });
    await expect(a.add({ url: 'https://example.com/a', speedLimit: -1 })).rejects.toMatchObject({ code: 'invalid_argument' });
    await expect(a.add({ url: 'https://example.com/a', dir: 'relative' })).rejects.toMatchObject({ code: 'invalid_argument' });
    await expect(a.openFile((await a.add({ url: 'https://example.com/a.zip' })).id)).rejects.toMatchObject({
      code: 'failed_precondition',
    });
  });
});

describe('mock daemon: limits', () => {
  it('respects the per-download speed limit', async () => {
    const a = make();
    const d = await a.add({ url: 'https://downloads.example.com/pub/limited.mkv', speedLimit: 256 * KiB });
    const start = await runUntil(a, d.id, (x) => x.state === 'downloading');
    for (let i = 0; i < 4; i++) a.tick(1000);
    const after = await get(a, d.id);
    expect(after.received - start.received).toBeLessThanOrEqual(4 * 256 * KiB);
    expect(after.received - start.received).toBeGreaterThan(0);
  });

  it('shares the global speed limit across downloads', async () => {
    const a = make({ settings: { globalSpeedLimit: 512 * KiB, maxConcurrentDownloads: 3 } });
    const ids = [];
    for (let i = 0; i < 3; i++) ids.push((await a.add({ url: `https://downloads.example.com/pub/m${i}.mkv` })).id);
    a.tick(1000);
    a.tick(1000);
    const total = async () => (await a.list()).reduce((s, d) => s + d.received, 0);
    const t0 = await total();
    for (let i = 0; i < 5; i++) a.tick(1000);
    expect((await total()) - t0).toBeLessThanOrEqual(5 * 512 * KiB);
  });

  it('validates and stores settings', async () => {
    const a = make();
    const current = await a.getSettings();
    expect(current.categories.map((c) => c.name)).toEqual(['Video', 'Audio', 'Archives', 'Documents', 'Programs']);
    await expect(a.updateSettings({ ...current, maxConcurrentDownloads: 0 })).rejects.toMatchObject({ code: 'invalid_argument' });
    await expect(a.updateSettings({ ...current, defaultDir: 'nope' })).rejects.toMatchObject({ code: 'invalid_argument' });
    await expect(
      a.updateSettings({ ...current, categories: [...current.categories, { name: 'video', extensions: ['x'], dir: '' }] }),
    ).rejects.toMatchObject({ code: 'invalid_argument' });
    await expect(
      a.updateSettings({ ...current, categories: [{ name: 'Bad', extensions: ['.MP4'], dir: '' }] }),
    ).rejects.toMatchObject({ code: 'invalid_argument' });
    const next = { ...current, maxConcurrentDownloads: 5, globalSpeedLimit: MiB, defaultDir: '/data', defaultMaxConnections: 16 };
    expect(await a.updateSettings(next)).toEqual(next);
    expect(await a.getSettings()).toEqual(next);
    // category folders: <default>/<name>, or the category's own folder
    expect((await a.add({ url: 'https://example.com/x.zip' })).dir).toBe('/data/Archives');
    await a.updateSettings({ ...next, categories: [{ name: 'Video', extensions: ['mp4'], dir: '/films' }] });
    expect((await a.add({ url: 'https://example.com/clip.mp4' })).dir).toBe('/films');
    await a.updateSettings({ ...next, useCategoryFolders: false });
    expect(await a.add({ url: 'https://example.com/y.zip' })).toMatchObject({ dir: '/data', category: 'Archives' });
  });
});

describe('mock daemon: categories and browser hand-offs', () => {
  it('chooses the category from the file name, the URL, then the MIME hint', async () => {
    const a = make();
    expect((await a.add({ url: 'https://example.com/get?id=1', filename: 'talk.MKV' })).category).toBe('Video');
    expect((await a.add({ url: 'https://example.com/files/notes.pdf' })).category).toBe('Documents');
    expect((await a.add({ url: 'https://example.com/get?id=2', mime: 'audio/ogg' })).category).toBe('Audio');
    expect((await a.add({ url: 'https://example.com/get?id=3' })).category).toBe('');
  });

  it('keeps browser hand-offs waiting for confirmation and asks the window', async () => {
    const a = make();
    const asked: number[] = [];
    const off = a.onConfirmRequest((id) => asked.push(id));
    const d = a.browserHandoff({ url: 'https://example.com/pub/movie.mp4', filename: 'movie.mp4', expectedSize: 5 * MiB });
    expect(d).toMatchObject({
      state: 'awaiting_confirmation',
      source: 'browser',
      category: 'Video',
      dir: `${DIR}/Video`,
      expectedSize: 5 * MiB,
      size: -1,
    });
    expect(asked).toEqual([d.id]);
    for (let i = 0; i < 5; i++) a.tick(1000);
    expect((await get(a, d.id)).state).toBe('awaiting_confirmation'); // never started by the scheduler
    // other sources are queued as before
    expect((await a.add({ url: 'https://example.com/a.zip', source: 'ui' })).state).toBe('queued');
    off();
    a.browserHandoff();
    expect(asked).toHaveLength(1);
  });

  it('confirms: start, later, a new folder and name; refuses a download that is not waiting', async () => {
    const a = make();
    const first = a.browserHandoff({ url: `https://example.com/pub/a.zip?size=${4 * MiB}`, filename: 'a.zip' });
    const started = await a.confirm(first.id, { start: true, dir: '/data/in', filename: 'renamed.zip' });
    expect(started).toMatchObject({ state: 'queued', dir: '/data/in', filename: 'renamed.zip', size: 4 * MiB });
    await expect(a.confirm(first.id, { start: true })).rejects.toMatchObject({ code: 'failed_precondition' });
    await runUntil(a, first.id, (d) => d.state === 'completed');

    const second = a.browserHandoff();
    await expect(a.confirm(second.id, { start: true, dir: 'relative' })).rejects.toMatchObject({ code: 'invalid_argument' });
    expect(await a.confirm(second.id, { start: false })).toMatchObject({ state: 'paused', dir: second.dir });
  });

  it('starts browser downloads at once when confirmation is off', async () => {
    const a = make({ settings: { confirmBrowserDownloads: false } });
    expect(a.browserHandoff().state).toBe('queued');
  });

  it('pauses and resumes all downloads', async () => {
    const a = make({ settings: { maxConcurrentDownloads: 2 } });
    for (let i = 0; i < 3; i++) await a.add({ url: `https://downloads.example.com/pub/f${i}.iso` });
    const waiting = a.browserHandoff();
    a.tick(1000);
    expect(await a.pauseAll()).toBe(3);
    const list = await a.list();
    expect(list.filter((d) => d.state === 'paused')).toHaveLength(3);
    expect(list.find((d) => d.id === waiting.id)?.state).toBe('awaiting_confirmation');
    expect(await a.pauseAll()).toBe(0);
    expect(await a.resumeAll()).toBe(3);
    expect((await a.list()).filter((d) => d.state === 'queued' || d.state === 'probing')).toHaveLength(3);
  });
});

describe('mock daemon: events', () => {
  it('sends a snapshot on subscribe, then coalesced updates once per tick', async () => {
    const a = make({ count: 6 });
    const batches: EventBatch[] = [];
    const off = a.onEvents((b) => batches.push(b));
    await Promise.resolve();
    expect(batches).toHaveLength(1);
    expect(batches[0]?.filter((e) => e.kind === 'snapshot')).toHaveLength(6);
    // As Watch without ids: the tools' state comes with the snapshot.
    expect(batches[0]).toHaveLength(7);
    expect(batches[0]?.at(-1)).toMatchObject({ kind: 'tools', status: { enabled: true, ready: false } });

    await a.add({ url: 'https://downloads.example.com/pub/new.zip' });
    a.tick(250);
    a.tick(250);
    expect(batches.length).toBe(3);
    for (const batch of batches.slice(1)) {
      const ids = batch.filter(isDownloadEvent).map((e) => (e.kind === 'removed' ? e.id : e.download.id));
      expect(new Set(ids).size).toBe(ids.length);
      expect(batch.every((e) => e.kind !== 'snapshot')).toBe(true);
    }
    // The new download joined the main queue: one queue update, in the first batch after it.
    expect(batches[1]?.filter((e) => e.kind === 'queue_updated')).toHaveLength(1);
    expect(batches[2]?.some((e) => e.kind === 'queue_updated')).toBe(false);

    off();
    a.tick(250);
    expect(batches.length).toBe(3);
  });

  it('emits removed events and forgets the download', async () => {
    const a = make();
    const d = await a.add({ url: 'https://downloads.example.com/pub/gone.zip' });
    const batches: EventBatch[] = [];
    a.onEvents((b) => batches.push(b));
    await Promise.resolve();
    await a.remove(d.id, { deleteFile: true });
    a.tick(250);
    expect(batches.at(-1)).toContainEqual({ kind: 'removed', id: d.id });
    expect(await a.list()).toEqual([]);
    await expect(a.remove(d.id, { deleteFile: false })).rejects.toMatchObject({ code: 'not_found' });
  });

  it('seeds a realistic, consistent download list', async () => {
    const a = make({ count: 300, seed: 7 });
    const list = await a.list();
    expect(list).toHaveLength(300);
    const states = new Set(list.map((d) => d.state));
    for (const s of ['completed', 'paused', 'failed', 'queued', 'downloading'] as const) {
      expect(states.has(s)).toBe(true);
    }
    expect(list.filter((d) => d.state === 'downloading' || d.state === 'probing').length).toBeLessThanOrEqual(3);
    for (const d of list) {
      if (d.state === 'completed') expect(d.received).toBe(d.size);
      if (d.state === 'failed') expect(d.errorCode).not.toBe('');
      checkSegments(d);
    }
    expect(list.some((d) => d.size === -1)).toBe(true);
    expect(new Set(list.map((d) => d.id)).size).toBe(300);
  });
});

describe('mock daemon: streaming media', () => {
  const HLS = 'https://media.example.org/talks/keynote/master.m3u8';
  const DASH = 'https://media.example.org/films/coast/manifest.mpd';

  it('probes HLS and DASH manifests and picks the best tracks', async () => {
    const a = make();
    const hls = await a.probeMedia({ manifestUrl: HLS });
    expect(hls).toMatchObject({ format: 'hls', container: 'mp4', selectedVideo: 'v2', selectedAudio: 'r0', title: 'keynote' });
    expect(hls.tracks.map((t) => t.kind)).toEqual(['video', 'video', 'video', 'audio', 'audio', 'audio']);
    expect(hls.durationMs).toBeGreaterThan(0);
    expect(hls.fragmentsTotal).toBe(2 * Math.ceil(hls.durationMs / 6000));
    expect(await a.probeMedia({ manifestUrl: HLS })).toEqual(hls); // stable per address

    const dash = await a.probeMedia({ manifestUrl: DASH });
    expect(dash).toMatchObject({ format: 'dash', selectedVideo: 'video-1080p', selectedAudio: 'audio-en', title: 'coast' });

    // MPEG-TS variants with their sound are converted: saved as MP4 (VIDEO-DETECT-SPEC §3.9).
    const muxed = await a.probeMedia({ manifestUrl: 'https://media.example.org/news/muxed/index.m3u8' });
    expect(muxed).toMatchObject({ container: 'mp4', selectedAudio: '' });
    expect(muxed.tracks.every((t) => t.kind === 'muxed')).toBe(true);
    expect(await a.list()).toEqual([]); // probing adds nothing
  });

  it('refuses encrypted and live streams and addresses that are no manifests', async () => {
    const a = make();
    await expect(a.probeMedia({ manifestUrl: 'https://media.example.org/drm/master.m3u8' })).rejects.toMatchObject({
      code: 'invalid_argument',
      message: expect.stringMatching(/^encrypted: /),
    });
    await expect(a.addMedia({ manifestUrl: 'https://media.example.org/encrypted.mpd' })).rejects.toMatchObject({
      code: 'invalid_argument',
      message: expect.stringMatching(/^encrypted: /),
    });
    await expect(a.addMedia({ manifestUrl: 'https://media.example.org/live/channel.m3u8' })).rejects.toMatchObject({
      code: 'invalid_argument',
      message: expect.stringMatching(/^live: /),
    });
    await expect(a.probeMedia({ manifestUrl: 'https://media.example.org/video.mp4' })).rejects.toMatchObject({ code: 'invalid_argument' });
    await expect(a.probeMedia({ manifestUrl: 'ftp://media.example.org/a.m3u8' })).rejects.toBeInstanceOf(ApiError);
    await expect(a.addMedia({ manifestUrl: HLS, videoTrack: 'r0' })).rejects.toMatchObject({ code: 'invalid_argument' });
    expect(await a.list()).toEqual([]);
  });

  it('downloads the fragments of the chosen tracks and assembles one file', async () => {
    const a = make();
    const added = await a.addMedia({ manifestUrl: HLS, source: 'ui', videoTrack: 'v0' });
    expect(added).toMatchObject({
      kind: 'media',
      state: 'queued',
      filename: 'keynote.mp4',
      category: 'Video',
      dir: `${DIR}/Video`,
      size: -1,
      source: 'ui',
    });
    expect(added.media).toMatchObject({ selectedVideo: 'v0', selectedAudio: 'r0', fragmentsDone: 0 });
    const total = added.media!.fragmentsTotal;

    const running = await runUntil(a, added.id, (d) => d.state === 'downloading' && (d.media?.fragmentsDone ?? 0) > 0);
    expect(running.segments).toEqual([]);
    expect(running.connections).toBeGreaterThan(0);
    expect(running.media!.fragmentsDone).toBeLessThan(total);
    const done = await runUntil(a, added.id, (d) => d.state === 'completed');
    expect(done.media!.fragmentsDone).toBe(total);
    expect(done.size).toBe(done.received);
    expect(done.size).toBeGreaterThan(0);
  });

  it('names streams by title, sorts audio-only ones into Audio and saves muxed HLS as .mp4', async () => {
    const a = make();
    expect(await a.addMedia({ manifestUrl: HLS, filename: 'My talk', startPaused: true })).toMatchObject({
      filename: 'My talk.mp4',
      state: 'paused',
    });
    expect(await a.addMedia({ manifestUrl: HLS, title: 'Opening keynote' })).toMatchObject({ filename: 'Opening keynote.mp4' });
    const podcast = await a.addMedia({ manifestUrl: 'https://media.example.org/podcasts/ep-12/audio.m3u8' });
    expect(podcast).toMatchObject({ category: 'Audio', dir: `${DIR}/Audio`, filename: 'ep-12.mp4' });
    expect(podcast.media).toMatchObject({ selectedVideo: '', selectedAudio: 'r0' });
    const news = await a.addMedia({ manifestUrl: 'https://media.example.org/news/evening/muxed.m3u8', audioTrack: '' });
    expect(news).toMatchObject({ filename: 'muxed.mp4', category: 'Video' });
    expect(news.media).toMatchObject({ selectedAudio: '', container: 'mp4' });
  });

  it('holds browser hand-offs for confirmation and takes the confirmed tracks', async () => {
    const a = make();
    const asked: number[] = [];
    a.onConfirmRequest((id) => asked.push(id));
    const held = a.browserMediaHandoff({ manifestUrl: DASH });
    expect(held).toMatchObject({ state: 'awaiting_confirmation', kind: 'media', source: 'browser' });
    expect(asked).toEqual([held.id]);
    const before = held.media!;

    const confirmed = await a.confirm(held.id, { start: true, videoTrack: 'video-360p', audioTrack: 'audio-es' });
    expect(confirmed.state).toBe('queued');
    expect(confirmed.media).toMatchObject({ selectedVideo: 'video-360p', selectedAudio: 'audio-es', fragmentsTotal: before.fragmentsTotal });

    const other = a.browserMediaHandoff({ manifestUrl: 'https://media.example.org/news/muxed/index.m3u8' });
    // empty = keep the daemon's pick
    expect((await a.confirm(other.id, { start: false })).media).toMatchObject({ selectedVideo: 'v2', selectedAudio: '' });
    const file = a.browserHandoff();
    await expect(a.confirm(file.id, { start: true, videoTrack: 'v0' })).rejects.toMatchObject({ code: 'invalid_argument' });
  });

  it('fails a job whose initialization segment is encrypted, and restarts streams from the first fragment', async () => {
    const a = make();
    const sealed = await a.addMedia({ manifestUrl: 'https://media.example.org/init-protected/master.m3u8' });
    const failed = await runUntil(a, sealed.id, (d) => d.state === 'failed');
    expect(failed).toMatchObject({ errorCode: 'encrypted', received: 0 });

    const flaky = await a.addMedia({ manifestUrl: 'https://media.example.org/flaky/talk/master.m3u8' });
    const broken = await runUntil(a, flaky.id, (d) => d.state === 'failed');
    expect(broken.errorCode).toBe('network');
    expect(broken.media!.fragmentsDone).toBeGreaterThan(0);
    const restarted = await a.resume(flaky.id, { restart: true });
    expect(restarted).toMatchObject({ state: 'queued', received: 0, size: -1 });
    expect(restarted.media!.fragmentsDone).toBe(0);
    const done = await runUntil(a, flaky.id, (d) => d.state === 'completed');
    expect(done.media!.fragmentsDone).toBe(done.media!.fragmentsTotal);
  });

  it('seeds a few consistent media downloads', async () => {
    const a = make({ count: 60, seed: 5 });
    const media = (await a.list()).filter((d) => d.kind === 'media');
    expect(media.length).toBeGreaterThan(0);
    for (const d of media) {
      const m = d.media!;
      expect(m.fragmentsDone).toBeLessThanOrEqual(m.fragmentsTotal);
      expect(d.segments).toEqual([]);
      if (d.state === 'completed') {
        expect(m.fragmentsDone).toBe(m.fragmentsTotal);
        expect(d.size).toBe(d.received);
      } else expect(d.size).toBe(-1);
    }
  });
});

describe('mock daemon: video pages (SITE-MEDIA-SPEC)', () => {
  const PAGE = 'https://www.youtube.com/watch?v=aqz-KE-bpKQ';
  const site = { manifestUrl: '', pageUrl: PAGE, origin: 'site' as const };

  it('sets up yt-dlp and Deno on first use, reads the page, then offers one MP4 quality per class best first', async () => {
    const a = make();
    const events: EventBatch[] = [];
    a.onEvents((b) => events.push(b));
    await Promise.resolve();
    await expect(a.probeMedia(site)).rejects.toMatchObject({
      code: 'failed_precondition',
      message: 'tools_installing: downloading yt-dlp and Deno (0%)',
    });
    expect((await a.getTools()).tools.map((t) => [t.name, t.state])).toEqual([
      ['yt-dlp', 'installing'],
      ['deno', 'installing'],
    ]);
    a.tick(2000);
    await expect(a.probeMedia(site)).rejects.toMatchObject({ message: 'tools_installing: downloading yt-dlp and Deno (50%)' });
    expect(events.at(-1)?.at(-1)).toMatchObject({ kind: 'tools', status: { ready: false, tools: [{ state: 'installing', bytesDone: 9 * MiB, bytesTotal: 18 * MiB }, {}] } });
    a.tick(2000);
    expect(events.at(-1)?.at(-1)).toMatchObject({ kind: 'tools', status: { ready: true, tools: [{ state: 'ready', verified: 'openpgp+sha256' }, { state: 'ready' }] } });
    await expect(a.probeMedia(site)).rejects.toMatchObject({ message: 'site_resolving: still getting the video info' });
    a.tick(1200);
    const info = await a.probeMedia(site);
    expect(info).toMatchObject({ origin: 'site', site: 'YouTube', pageUrl: PAGE, format: 'http', container: 'mp4', manifestUrl: '', fragmentsTotal: 0 });
    const videos = info.tracks.filter((t) => t.kind !== 'audio');
    expect(videos.length).toBeGreaterThanOrEqual(5);
    // MP4 only: H.264 and AV1, never VP9, Opus or WebM.
    for (const t of info.tracks) {
      expect(t.ext).toMatch(/^(mp4|m4a)$/);
      expect(t.codecs).toMatch(/^(avc1|av01|mp4a)/);
      expect(t.size).toBeGreaterThan(0);
    }
    // One quality per class under its standard name, best first; H.264 wherever the class has
    // it; the muxed 18 is dropped for the video-only 360p.
    const classes = videos.map((t) => parseStandardName(t.name)?.cls ?? 0);
    expect(classes.every((c) => c > 0)).toBe(true);
    expect(classes).toEqual([...new Set(classes)].sort((x, y) => y - x));
    expect(classes.at(-1)).toBe(144);
    expect(videos.filter((t) => t.height <= 1080).every((t) => t.codecs.startsWith('avc1'))).toBe(true);
    expect(videos.some((t) => t.kind === 'muxed')).toBe(false);
    expect(info.selectedVideo).toBe(videos[0]?.id);
    expect(info.selectedAudio).toBe('140');
    expect(await a.probeMedia(site)).toEqual(info); // cached
    expect(await a.list()).toEqual([]); // probing adds nothing
  });

  it('names a film’s qualities by their class, never their height', async () => {
    const a = make({ toolsReady: true, resolveMs: 0 });
    for (const id of ['film-1', 'film-2', 'film-3', 'movie-4', 'movie-5']) {
      const info = await a.probeMedia({ ...site, pageUrl: `https://www.youtube.com/watch?v=${id}` });
      const videos = info.tracks.filter((t) => t.kind !== 'audio');
      for (const t of videos) {
        expect(t.width / t.height).toBeGreaterThan(2.3);
        expect(t.name).toBe(standardName(qualityClass(t.width, t.height), t.frameRate, false));
        expect(t.name).not.toContain(`${t.height}p`);
      }
      expect(videos.at(-1)?.name).toBe('144p');
    }
    // A 4K film (3840×1634) reads "4K (2160p)", as the user expects.
    let top: MediaTrack | undefined;
    for (let i = 1; i <= 30 && top?.width !== 3840; i++) {
      top = (await a.probeMedia({ ...site, pageUrl: `https://www.youtube.com/watch?v=film-${i}` })).tracks[0];
    }
    expect(top).toMatchObject({ width: 3840, height: 1634, name: expect.stringMatching(/^4K \(2160p\)( 60fps)?$/) });
  });

  it('refuses pages as yt-dlp and downexd would', async () => {
    const a = make({ toolsReady: true, resolveMs: 0 });
    const probe = (pageUrl: string) => a.probeMedia({ ...site, pageUrl });
    const cases: [string, string, RegExp][] = [
      ['https://video.example.org/drm/1', 'invalid_argument', /^encrypted: /],
      ['https://video.example.org/live/1', 'invalid_argument', /^live: /],
      ['https://www.youtube.com/playlist?list=PL1', 'invalid_argument', /^media_unsupported: a playlist/],
      ['https://podcasts.example.org/ep-1', 'invalid_argument', /^media_unsupported: no MP4 video/],
      ['https://video.example.org/private/1', 'failed_precondition', /^site_unavailable: /],
      ['https://video.example.org/signin/1', 'failed_precondition', /^auth_required: /],
      ['https://video.example.org/unsupported', 'invalid_argument', /^site_unsupported: /],
      ['https://video.example.org/broken', 'failed_precondition', /^site_error: yt-dlp could not read this page: /],
    ];
    for (const [url, code, message] of cases) {
      await expect(probe(url)).rejects.toMatchObject({ code, message: expect.stringMatching(message) });
    }
    await expect(a.probeMedia({ ...site, manifestUrl: 'https://media.example.org/a.m3u8' })).rejects.toMatchObject({ code: 'invalid_argument' });
    await expect(probe('ftp://video.example.org/a')).rejects.toMatchObject({ code: 'invalid_argument' });
    await a.updateSettings({ ...(await a.getSettings()), siteMedia: false });
    await expect(probe(PAGE)).rejects.toMatchObject({ code: 'failed_precondition', message: expect.stringMatching(/^site_media_off: /) });
    expect((await a.getTools()).tools.every((t) => t.state === 'disabled')).toBe(true);
    await expect(a.installTools(false)).rejects.toMatchObject({ message: expect.stringMatching(/^site_media_off: /) });
  });

  it('adds a video as one .mp4 that keeps its name in another quality, and downloads it in parts', async () => {
    const a = make({ toolsReady: true, resolveMs: 0 });
    const held = a.browserVideoHandoff({ pageUrl: PAGE });
    expect(held).toMatchObject({ state: 'awaiting_confirmation', kind: 'media', source: 'browser', url: PAGE, category: 'Video', filename: 'Video aqz-KE-bpKQ.mp4' });
    expect(held.media).toMatchObject({ origin: 'site', container: 'mp4' });
    const offer = await a.probeMedia(site);
    const best = offer.tracks.filter((t) => t.kind === 'video')[0]!;
    const audio = offer.tracks.find((t) => t.id === '140')!;
    expect(held.expectedSize).toBe(best.size + audio.size);

    const videos = offer.tracks.filter((t) => t.kind === 'video');
    const low = videos.at(-1)!;
    const other = await a.confirm(held.id, { start: false, videoTrack: low.id, audioTrack: '140' });
    expect(other).toMatchObject({ filename: 'Video aqz-KE-bpKQ.mp4', state: 'paused' });
    expect(other.media).toMatchObject({
      selectedVideo: low.id,
      selectedAudio: '140',
      container: 'mp4',
      fragmentsTotal: Math.ceil(low.size / (10 * MiB)) + Math.ceil(audio.size / (10 * MiB)),
    });
    expect(other.expectedSize).toBe(low.size + audio.size);

    const v = videos[1]!;
    const added = await a.addMedia({ ...site, videoTrack: v.id, source: 'ui' });
    expect(added).toMatchObject({ state: 'queued', filename: 'Video aqz-KE-bpKQ.mp4', dir: `${DIR}/Video`, size: -1 });
    const parts = added.media!.fragmentsTotal;
    expect(parts).toBe(Math.ceil(v.size / (10 * MiB)) + Math.ceil(audio.size / (10 * MiB)));
    const probing = await runUntil(a, added.id, (d) => d.state === 'probing');
    expect(probing.size).toBe(-1);
    const running = await runUntil(a, added.id, (d) => d.state === 'downloading');
    expect(running.size).toBe(v.size + audio.size); // Download.size = BytesTotal while running
    const done = await runUntil(a, added.id, (d) => d.state === 'completed');
    expect(done.media!.fragmentsDone).toBe(parts);
    expect(done.size).toBe(done.received);
  });

  it('installs on request (waiting for the end), and reports a failed install', async () => {
    const a = make();
    const installing = a.installTools(false);
    a.tick(2000);
    a.tick(2000);
    expect(await installing).toMatchObject({ ready: true, enabled: true });
    expect(await a.installTools(true)).toMatchObject({ ready: true });

    const b = make({ toolsFail: true, toolsInstallMs: 1000 });
    const failing = b.installTools(false);
    b.tick(1000);
    await expect(failing).rejects.toMatchObject({ code: 'failed_precondition', message: expect.stringMatching(/^tools_unavailable: /) });
    expect((await b.getTools()).tools[0]).toMatchObject({ state: 'failed', errorCode: 'network', error: 'could not download yt-dlp (network)' });
    await expect(b.probeMedia(site)).rejects.toMatchObject({ message: expect.stringMatching(/^tools_unavailable: /) });
  });
});

describe('mock daemon: progress windows', () => {
  const ranges = (d: Download) => d.connectionInfo.map((c) => c.index);

  it('reports one connection per worker slot while a download runs', async () => {
    const a = make({ retryRate: 0 });
    const d = await a.add({ url: `https://downloads.example.com/pub/big.iso?size=${64 * MiB}`, maxConnections: 4 });
    a.tick(100);
    await runUntil(a, d.id, (x) => x.state === 'downloading');
    a.tick(250);
    let cur = await get(a, d.id);
    expect(ranges(cur)).toEqual([1, 2, 3, 4]);
    expect(cur.connectionInfo.every((c) => c.state === 'connecting')).toBe(true);
    a.tick(1000);
    cur = await get(a, d.id);
    expect(cur.connectionInfo.every((c) => c.state === 'receiving')).toBe(true);
    for (const c of cur.connectionInfo) {
      expect(c.fragment).toBe(-1);
      expect(c.start).toBeGreaterThanOrEqual(0);
      expect(c.end).toBeGreaterThanOrEqual(c.start);
      expect(c.received).toBeGreaterThan(0);
      expect(c.received).toBeLessThanOrEqual(c.end - c.start + 1);
      // Each connection works on one of the download's segments.
      expect(cur.segments.some((s) => s.start === c.start)).toBe(true);
    }
    expect(cur.connections).toBe(4);

    for (let i = 0; i < 2000; i++) {
      a.tick(1000);
      cur = await get(a, d.id);
      expect(cur.connectionInfo.length).toBeLessThanOrEqual(4);
      if (cur.state !== 'downloading') break;
    }
    expect(cur.state).toBe('completed');
    expect(cur.connectionInfo).toEqual([]);
  });

  it('lets connections wait and retry, and drops them when the download stops', async () => {
    const a = make({ retryRate: 5 });
    const d = await a.add({ url: `https://downloads.example.com/pub/flaky-server.bin?size=${16 * MiB}` });
    await runUntil(a, d.id, (x) => x.state === 'downloading');
    a.tick(1000);
    const cur = await get(a, d.id);
    const retrying = cur.connectionInfo.filter((c) => c.state === 'retrying');
    expect(retrying.length).toBeGreaterThan(0);
    for (const c of retrying) expect(c.detail).toMatch(/^(HTTP 503|connection reset|timed out), retrying in \d s$/);

    const paused = await a.pause(d.id);
    expect(paused.connectionInfo).toEqual([]);
    a.tick(1000);
    expect((await get(a, d.id)).connectionInfo).toEqual([]);
  });

  it('changes the limits of a running download at once', async () => {
    const a = make({ retryRate: 0 });
    const d = await a.add({ url: `https://downloads.example.com/pub/limits.mkv?size=${2048 * MiB}`, maxConnections: 8 });
    await runUntil(a, d.id, (x) => x.state === 'downloading' && x.received > 0);
    const limited = await a.setDownloadOptions(d.id, { speedLimit: 128 * KiB });
    expect(limited).toMatchObject({ speedLimit: 128 * KiB, maxConnections: 8, state: 'downloading' });
    a.tick(1000);
    const start = await get(a, d.id);
    for (let i = 0; i < 4; i++) a.tick(1000);
    expect((await get(a, d.id)).received - start.received).toBeLessThanOrEqual(4 * 128 * KiB);

    expect((await a.setDownloadOptions(d.id, { maxConnections: 2 })).maxConnections).toBe(2);
    a.tick(1000);
    const fewer = await get(a, d.id);
    expect(fewer.connectionInfo.map((c) => c.index)).toEqual([1, 2]);
    expect(fewer.connections).toBeLessThanOrEqual(2);
    expect((await a.setDownloadOptions(d.id, { speedLimit: 0, maxConnections: 0 })).speedLimit).toBe(0);

    await expect(a.setDownloadOptions(d.id, { maxConnections: 33 })).rejects.toMatchObject({ code: 'invalid_argument' });
    await expect(a.setDownloadOptions(d.id, { speedLimit: -1 })).rejects.toMatchObject({ code: 'invalid_argument' });
    await expect(a.setDownloadOptions(999, { speedLimit: 1 })).rejects.toMatchObject({ code: 'not_found' });
  });

  it('reports the fragment of each media connection and assembles before it completes', async () => {
    const a = make({ retryRate: 0, assembleMs: 1500 });
    const added = await a.addMedia({ manifestUrl: 'https://media.example.org/talks/keynote/master.m3u8', videoTrack: 'v0' });
    const running = await runUntil(a, added.id, (x) => x.state === 'downloading' && (x.media?.fragmentsDone ?? 0) > 0);
    expect(running.connectionInfo.length).toBe(running.connections);
    const done = running.media!.fragmentsDone;
    expect(running.connectionInfo.map((c) => c.fragment)).toEqual(running.connectionInfo.map((_, i) => done + i));
    const total = running.media!.fragmentsTotal;

    const assembling = await runUntil(a, added.id, (x) => x.media?.fragmentsDone === total);
    expect(assembling).toMatchObject({ state: 'downloading', connections: 0, connectionInfo: [], speedBps: 0 });
    a.tick(1000);
    expect((await get(a, added.id)).state).toBe('downloading');
    a.tick(1000);
    const complete = await get(a, added.id);
    expect(complete.state).toBe('completed');
    expect(complete.size).toBe(complete.received);
  });

  it('reads one download, tells its view, and records the progress windows and window changes', async () => {
    const a = make({ view: { view: 'progress', id: 3, openWith: true, hibernate: false, wakeTimers: false } });
    const d = await a.add({ url: 'https://downloads.example.com/pub/one.zip' });
    expect((await a.get(d.id)).filename).toBe('one.zip');
    await expect(a.get(999)).rejects.toMatchObject({ code: 'not_found' });
    expect(await a.startup()).toMatchObject({ view: 'progress', id: 3, openWith: true, hibernate: false, wakeTimers: false });
    expect(await make().startup()).toMatchObject({ view: 'main', openWith: true, hibernate: true, wakeTimers: true });
    // A caller's own view (the demo's main and progress windows) still reports the build,
    // so the demo's About page shows the window version as its diagnostics do.
    expect((await a.startup()).build).toMatchObject({ version: '0.0.0-mock', arch: 'amd64' });
    const own = { version: '9.9.9', os: 'linux', arch: 'arm64', goVersion: 'go1.27.0', wailsVersion: 'v2.17.0', commit: '', commitTime: 0, modified: false };
    expect((await make({ view: { view: 'main', openWith: true, hibernate: true, wakeTimers: true, build: own } }).startup()).build).toEqual(own);

    const b = make();
    const added = await b.add({ url: 'https://downloads.example.com/pub/two.zip' });
    await b.openProgressWindow(added.id);
    await expect(b.openProgressWindow(999)).rejects.toMatchObject({ code: 'not_found' });
    expect(b.windowState.opened).toEqual([added.id]);
    b.window.setTitle('3% two.zip');
    b.window.setSize(520, 560);
    expect(await b.window.getSize()).toEqual({ width: 520, height: 560 });
    b.window.close();
    expect(b.windowState).toMatchObject({ title: '3% two.zip', closed: true, sizes: [{ width: 520, height: 560 }] });
  });
});

describe('mock daemon: download complete dialog', () => {
  it('marks a download for the dialog and counts its running time without pauses', async () => {
    let clock = 1_800_000_000_000;
    const a = make({ retryRate: 0, now: () => clock });
    const d = await a.add({ url: `https://downloads.example.com/pub/marked.iso?size=${64 * MiB}` });
    expect(d).toMatchObject({ completionDialog: false, activeMs: 0, startedAt: 0 });
    expect((await a.setDownloadOptions(d.id, { completionDialog: true })).completionDialog).toBe(true);
    // Other options leave the mark as it is.
    expect((await a.setDownloadOptions(d.id, { speedLimit: 0 })).completionDialog).toBe(true);

    clock += 1000;
    a.tick(1000);
    const started = await get(a, d.id);
    expect(started.startedAt).toBe(clock);
    expect(started.activeMs).toBe(1000);
    await a.pause(d.id);
    for (let i = 0; i < 5; i++) a.tick(1000);
    expect((await get(a, d.id)).activeMs).toBe(1000);
    await a.resume(d.id);
    clock += 5000;
    const done = await runUntil(a, d.id, (x) => x.state === 'completed');
    expect(done.startedAt).toBe(started.startedAt);
    expect(done.activeMs).toBeGreaterThan(1000);
    expect(done.completionDialog).toBe(true);
    // As downexd: a completed download takes no options.
    await expect(a.setDownloadOptions(d.id, { completionDialog: true })).rejects.toMatchObject({ code: 'failed_precondition' });
  });

  it('opens completed downloads with another app only', async () => {
    const a = make();
    const d = await a.add({ url: `https://downloads.example.com/pub/report.pdf?size=${MiB}` });
    await expect(a.openWith(d.id)).rejects.toMatchObject({ code: 'failed_precondition' });
    await runUntil(a, d.id, (x) => x.state === 'completed');
    await expect(a.openWith(d.id)).resolves.toBeUndefined();
    await expect(a.openWith(999)).rejects.toMatchObject({ code: 'not_found' });
  });

  it('gives seeded history its running time', async () => {
    const a = make({ count: 40 });
    for (const d of await a.list()) {
      if (d.state === 'completed' && d.kind === 'file') {
        expect(d.startedAt).toBeGreaterThan(0);
        expect(d.completedAt).toBeGreaterThanOrEqual(d.startedAt);
        expect(d.activeMs).toBeGreaterThan(0);
        expect(d.activeMs).toBeLessThanOrEqual(d.completedAt - d.startedAt);
      }
      if (d.state === 'queued' && d.startedAt === 0) expect(d.activeMs).toBe(0);
    }
  });
});

describe('mock daemon: queues (M6)', () => {
  const spec = { name: 'Later', maxConcurrent: 1, speedLimit: 0, schedule: EMPTY_SCHEDULE, postAction: 'none' as const };

  it('has a running main queue and a stopped Night queue; downloads join the main queue', async () => {
    const a = make();
    const [main, night] = await a.listQueues();
    expect(main).toMatchObject({ id: 1, isMain: true, running: true });
    expect(night).toMatchObject({ name: 'Night', running: false, schedule: { enabled: true, startMinute: 60, stopMinute: 420 } });
    const d = await a.add({ url: 'https://downloads.example.com/pub/a.zip' });
    expect(d.queueId).toBe(1);
    expect((await a.listQueues())[0]?.downloadIds).toEqual([d.id]);
  });

  it('starts a queue only while it runs, up to its own limit, in its order', async () => {
    const a = make({ settings: { maxConcurrentDownloads: 3 } });
    const q = await a.createQueue(spec);
    const ids: number[] = [];
    for (let i = 0; i < 3; i++) ids.push((await a.add({ url: `https://downloads.example.com/pub/q${i}.iso`, queueId: q.id })).id);
    a.tick(250);
    expect((await a.list()).every((d) => d.state === 'queued')).toBe(true);
    // Reorder: the last one first.
    expect((await a.moveDownloads([ids[2] as number], q.id, 0)).downloadIds).toEqual([ids[2], ids[0], ids[1]]);
    await a.startQueue(q.id);
    a.tick(250);
    const list = await a.list();
    expect(list.filter((d) => d.state === 'probing').map((d) => d.id)).toEqual([ids[2]]);
    expect(list.find((d) => d.id === ids[0])?.queuePosition).toBe(1);
    // Stopping it puts its running download back in the queue.
    await a.stopQueue(q.id);
    expect((await get(a, ids[2] as number)).state).toBe('queued');
    await expect(a.createQueue({ ...spec, name: 'later' })).rejects.toMatchObject({ code: 'already_exists' });
    await expect(a.createQueue({ ...spec, name: 'Other', maxConcurrent: 17 })).rejects.toMatchObject({ code: 'invalid_argument' });
  });

  it('moves downloads between queues and back to the main queue when a queue is deleted', async () => {
    const a = make();
    const d1 = await a.add({ url: 'https://downloads.example.com/pub/1.zip' });
    const d2 = await a.add({ url: 'https://downloads.example.com/pub/2.zip' });
    const q = await a.createQueue(spec);
    await a.moveDownloads([d2.id, d1.id], q.id, -1);
    expect((await get(a, d1.id)).queueId).toBe(q.id);
    expect((await a.listQueues())[0]?.downloadIds).toEqual([]);
    await a.deleteQueue(q.id);
    expect((await get(a, d1.id)).queueId).toBe(1);
    expect((await a.listQueues())[0]?.downloadIds).toEqual([d2.id, d1.id]);
    await expect(a.deleteQueue(1)).rejects.toMatchObject({ code: 'failed_precondition' });
    await expect(a.moveDownloads([999], 1, 0)).rejects.toMatchObject({ code: 'not_found' });
  });

  it('confirms "download later" into a stopped queue as queued there', async () => {
    const a = make();
    const night = (await a.listQueues())[1] as { id: number };
    const waiting = a.browserHandoff();
    const d = await a.confirm(waiting.id, { start: false, queueId: night.id });
    expect(d).toMatchObject({ state: 'queued', queueId: night.id });
    a.tick(250);
    expect((await get(a, d.id)).state).toBe('queued');
    // As downexd, a running queue named in the confirmation takes it queued too.
    const main = (await a.listQueues())[0] as { id: number };
    const other = a.browserHandoff();
    expect(await a.confirm(other.id, { start: false, queueId: main.id })).toMatchObject({ state: 'queued', queueId: main.id });
  });

  it('counts down the action of a finished queue, which can be cancelled', async () => {
    let t = 1_000_000;
    const a = make({ now: () => t });
    const q = await a.createQueue({ ...spec, postAction: 'shutdown' });
    const d = await a.add({ url: `https://downloads.example.com/pub/small.zip?size=${MiB}`, queueId: q.id });
    const events: EventBatch[] = [];
    a.onEvents((b) => events.push(b));
    await a.startQueue(q.id);
    await runUntil(a, d.id, (x) => x.state === 'completed');
    a.tick(250);
    const pending = events.flat().find((e) => e.kind === 'post_action');
    expect(pending).toMatchObject({ kind: 'post_action', pending: { queueId: q.id, action: 'shutdown', cancelled: false } });
    await a.cancelPostAction(q.id);
    a.tick(250);
    expect(events.flat().filter((e) => e.kind === 'post_action').at(-1)).toMatchObject({ pending: { cancelled: true } });
    t += 120_000;
    a.tick(250);
    // Nothing more once cancelled.
    expect(events.flat().filter((e) => e.kind === 'post_action')).toHaveLength(2);
  });

  it('asks for credentials until a site login with a password matches', async () => {
    const a = make();
    const d = await a.add({ url: 'https://private.example.net/files/report.pdf' });
    const failed = await runUntil(a, d.id, (x) => x.state === 'failed');
    expect(failed.errorCode).toBe('auth_required');
    const s = await a.getSettings();
    const saved = await a.updateSettings({ ...s, siteLogins: [{ id: '', urlPattern: 'https://private.example.net/*', username: 'me', hasPassword: false }] });
    const id = saved.siteLogins[0]?.id ?? '';
    expect(id).not.toBe('');
    await a.setSecret('site_login', id, 'pw');
    expect((await a.getSettings()).siteLogins[0]?.hasPassword).toBe(true);
    await a.resume(d.id);
    expect((await runUntil(a, d.id, (x) => x.state === 'completed' || x.state === 'failed')).state).toBe('completed');
    await a.setSecret('proxy', '', 'x');
    expect((await a.getSettings()).proxy.hasPassword).toBe(true);
    await a.clearSecret('proxy', '');
    expect((await a.getSettings()).proxy.hasPassword).toBe(false);
    await expect(a.setSecret('site_login', 'nope', 'x')).rejects.toMatchObject({ code: 'not_found' });
  });

  it('checks for updates only with an https feed', async () => {
    const a = make();
    await expect(a.checkForUpdate()).rejects.toMatchObject({ code: 'failed_precondition' });
    const s = await a.getSettings();
    await expect(a.updateSettings({ ...s, updateFeedUrl: 'http://x.example/feed' })).rejects.toMatchObject({ code: 'invalid_argument' });
    await a.updateSettings({ ...s, updateFeedUrl: 'https://updates.example.com/feed.json' });
    expect(await a.checkForUpdate()).toMatchObject({ available: true, latestVersion: '0.0.1-mock' });
    expect(await a.pickFile({ initialDir: '/opt' })).toBe('/opt/scanner');
  });
});
describe('mock daemon: the Studio scene and the demo data', () => {
  const byState = (list: Download[], state: Download['state']) => list.filter((d) => d.state === state);

  it('starts as the design shows the main window', async () => {
    const a = make({ scene: 'studio' });
    const list = await a.list();
    expect(list).toHaveLength(28);
    const running = byState(list, 'downloading');
    expect(running.map((d) => d.connections).sort((x, y) => y - x)).toEqual([32, 16, 4]);
    const youtube = running.find((d) => d.kind === 'media');
    expect(youtube).toMatchObject({ filename: 'conference-keynote.mp4', category: 'Video' });
    expect(youtube?.media).toMatchObject({ origin: 'site', site: 'YouTube', container: 'mp4' });
    expect(youtube?.size).toBeGreaterThan(0);
    for (const d of running.filter((x) => x.kind === 'file')) {
      expect(d.connectionInfo).toHaveLength(d.connections);
      expect(d.connectionInfo.every((c) => c.state === 'receiving' && c.speedBps > 0)).toBe(true);
      expect(new Set(d.connectionInfo.map((c) => c.speedBps)).size).toBeGreaterThan(1);
    }
    expect(byState(list, 'awaiting_confirmation')).toEqual([
      expect.objectContaining({ filename: 'budget-plan.xlsx', source: 'browser', size: -1, expectedSize: expect.any(Number) }),
    ]);
    expect(byState(list, 'paused')).toHaveLength(1);
    const queued = byState(list, 'queued').sort((x, y) => x.queuePosition - y.queuePosition);
    expect(queued.map((d) => d.queuePosition)).toEqual([1, 2, 3, 4, 5, 6, 7]);
    expect(queued.slice(0, 2).map((d) => d.filename)).toEqual(['setup-x64-312.exe', 'annual-report-2025.pdf']);
    expect(byState(list, 'failed')).toEqual([expect.objectContaining({ errorCode: 'http_status', error: expect.stringContaining('HTTP 503') })]);
    const completed = byState(list, 'completed');
    expect(completed).toHaveLength(15);
    expect(completed.find((d) => d.kind === 'media')).toMatchObject({ filename: 'keynote-2026.mp4', media: { format: 'hls' } });
    for (const d of list) {
      if (d.state === 'completed') {
        expect(d.received).toBe(d.size);
        expect(d.completedAt).toBeGreaterThanOrEqual(d.startedAt);
      }
      checkSegments(d);
    }
    // the sidebar of the design: Video 11, Audio 3, Archives 6, Documents 3, Programs 4, Other 1
    const counts = new Map<string, number>();
    for (const d of list) counts.set(d.category || 'Other', (counts.get(d.category || 'Other') ?? 0) + 1);
    expect(Object.fromEntries(counts)).toEqual({ Video: 11, Audio: 3, Archives: 6, Documents: 3, Programs: 4, Other: 1 });
  });

  it('is the default without a count, and stays consistent while it runs', async () => {
    const a = createMockApi({ autoTick: false, failureRate: 0, defaultDir: DIR });
    api = a;
    expect(await a.list()).toHaveLength(28);
    for (let i = 0; i < 40; i++) a.tick(250);
    const list = await a.list();
    expect(list.filter((d) => d.state === 'downloading' || d.state === 'probing').length).toBeLessThanOrEqual(3);
    for (const d of list) checkSegments(d);
    const media = list.find((d) => d.kind === 'media' && d.state === 'downloading');
    expect(media?.connectionInfo).toHaveLength(media?.connections ?? -1);
  });

  it('names videos .mp4 and audio .mp3 or .m4a only', async () => {
    for (const a of [make({ scene: 'studio' }), make({ count: 400, seed: 7 })]) {
      for (const d of await a.list()) {
        expect(d.filename).not.toMatch(/\.(webm|mkv|mov|flac|ogg)$/i);
        if (d.category === 'Video') expect(d.filename).toMatch(/\.mp4$/);
        if (d.category === 'Audio') expect(d.filename).toMatch(/\.(mp3|m4a)$/);
      }
      a.dispose();
    }
  });
});

describe('mock daemon: free space and the window (Studio)', () => {
  it('answers the free space of an absolute folder: 214 GiB less what completes', async () => {
    const a = make({ retryRate: 0 });
    expect(await a.freeSpace(DIR)).toBe(214 * 1024 * MiB);
    expect(await a.freeSpace('C:/Users/you/Downloads')).toBe(214 * 1024 * MiB);
    await expect(a.freeSpace('Downloads')).rejects.toMatchObject({ code: 'invalid_argument', message: 'dir must be an absolute path' });
    await expect(a.freeSpace('')).rejects.toMatchObject({ code: 'invalid_argument' });
    const d = await a.add({ url: `https://downloads.example.com/pub/fill.iso?size=${8 * MiB}` });
    await runUntil(a, d.id, (x) => x.state === 'completed');
    expect(await a.freeSpace(DIR)).toBe(214 * 1024 * MiB - 8 * MiB);
    expect(await make({ freeBytes: 5 }).freeSpace('/')).toBe(5);
  });

  it('records what the page asks of its window', async () => {
    const a = make();
    expect(await a.window.isMaximised()).toBe(false);
    a.window.toggleMaximise();
    expect(await a.window.isMaximised()).toBe(true);
    a.window.minimise();
    a.window.toggleMaximise();
    expect(a.windowState).toMatchObject({ minimised: 1, maximised: false });
  });
});

describe('mock daemon: download again, theme and About (notes 2, 4, 8)', () => {
  async function completedFile(a: MockApi, name = 'report.pdf'): Promise<Download> {
    const added = await a.add({ url: `https://downloads.example.com/pub/${name}?size=${4 * MiB}` });
    return runUntil(a, added.id, (d) => d.state === 'completed');
  }

  it('downloads a completed file again as "name (1).ext", keeping the file', async () => {
    const a = make();
    const done = await completedFile(a);
    await expect(a.resume(done.id)).rejects.toMatchObject({ code: 'failed_precondition' });
    await expect(a.resume(done.id, { existingFile: 'keepBoth' })).rejects.toMatchObject({ code: 'invalid_argument' });
    expect(await a.fileExists(done.id)).toBe(true);

    const again = await a.resume(done.id, { restart: true });
    expect(again).toMatchObject({ state: 'queued', filename: 'report (1).pdf', received: 0, completedAt: 0, error: '' });
    expect(again.segments).toEqual([]);
    const second = await runUntil(a, done.id, (d) => d.state === 'completed');
    expect(second.filename).toBe('report (1).pdf');
    expect(second.received).toBe(second.size);

    // Again: the next free name, never one the first copies hold.
    const third = await a.resume(done.id, { restart: true, existingFile: 'keepBoth' });
    expect(third.filename).toBe('report (1) (1).pdf');
  });

  it('replaces the file once the new copy is complete', async () => {
    const a = make();
    const done = await completedFile(a, 'movie.mp4');
    const free = await a.freeSpace(DIR);
    const again = await a.resume(done.id, { restart: true, existingFile: 'replace' });
    expect(again).toMatchObject({ state: 'queued', filename: 'movie.mp4', received: 0 });
    const replaced = await runUntil(a, done.id, (d) => d.state === 'completed');
    expect(replaced.filename).toBe('movie.mp4');
    // The old copy is gone, so the volume holds one copy as before.
    expect(await a.freeSpace(DIR)).toBe(free);
  });

  it('downloads a file that is gone again under its own name, whatever the choice', async () => {
    const a = make();
    const done = await completedFile(a, 'gone.zip');
    a.deleteFile(done.id);
    expect(await a.fileExists(done.id)).toBe(false);
    expect((await a.resume(done.id, { restart: true })).filename).toBe('gone.zip');
    await runUntil(a, done.id, (d) => d.state === 'completed');
    expect(await a.fileExists(done.id)).toBe(true);
    a.deleteFile(done.id);
    expect((await a.resume(done.id, { restart: true, existingFile: 'replace' })).filename).toBe('gone.zip');
  });

  it('restarts a completed video of a web page from its page', async () => {
    const a = make({ toolsReady: true, resolveMs: 0 });
    const v = a.browserVideoHandoff({ source: 'ui' });
    const done = await runUntil(a, v.id, (d) => d.state === 'completed');
    const again = await a.resume(done.id, { restart: true });
    expect(again.filename).toMatch(/ \(1\)\.mp4$/);
    expect(again.media?.fragmentsDone).toBe(0);
    expect((await runUntil(a, done.id, (d) => d.state === 'probing' || d.state === 'downloading')).state).not.toBe('completed');
  });

  it('records the theme and the folders the page asks for, and refuses anything else', async () => {
    const a = make();
    await a.setTheme('dark');
    await a.setTheme('system');
    expect(a.windowState.themes).toEqual(['dark', 'system']);
    await expect(a.setTheme('purple' as never)).rejects.toMatchObject({ code: 'invalid_argument' });
    await a.openAppFolder('logs');
    await a.openAppFolder('downloads');
    expect(a.windowState.folders).toEqual(['logs', 'downloads']);
    await expect(a.openAppFolder('C:\\Windows' as never)).rejects.toMatchObject({ code: 'invalid_argument' });
  });

  it('answers the About page: build, folders, update support, diagnostics without private data, notices', async () => {
    const a = make({ defaultDir: 'C:\\Users\\you\\Downloads' });
    const view = await a.startup();
    expect(view.build).toMatchObject({ version: '0.0.0-mock', os: 'windows', arch: 'amd64' });
    const s = await a.status();
    expect(s).toMatchObject({
      dataDir: 'C:\\Users\\you\\AppData\\Roaming\\Downex',
      logDir: 'C:\\Users\\you\\AppData\\Local\\Downex\\logs',
      toolsDir: 'C:\\Users\\you\\AppData\\Local\\Downex\\tools',
      updatesSupported: false,
    });
    await a.add({ url: 'https://secret.example.com/private-name.iso' });
    const text = await a.diagnostics({ webview: '141.0.3537.71' });
    expect(text).toContain('WebView2 141.0.3537.71');
    expect(text).toContain('%LOCALAPPDATA%\\Downex\\logs');
    for (const secret of [s.endpoint, 'mock://', 'secret', 'private', 'C:\\Users\\you']) expect(text).not.toContain(secret);
    await expect(a.diagnostics({ webview: '<b>' })).rejects.toMatchObject({ code: 'invalid_argument' });
    expect(await a.thirdPartyNotices()).toContain('THIRD-PARTY NOTICES');

    const unix = make({ defaultDir: DIR });
    expect((await unix.status()).dataDir).toBe('/home/you/.config/Downex');
    expect((await unix.startup()).build?.os).toBe('linux');
  });
});
