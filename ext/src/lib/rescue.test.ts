import { describe, expect, it } from 'vitest';
import type { StorageAreaLike } from './history';
import { failure, type AddFields, type Outcome, type RescuedResult, type RescueFields, type ResumedResult } from './protocol';
import {
  FailedList,
  MAC_NO_ACCESS,
  RESCUE_CONTEXT_MAX,
  RESCUE_CONTEXT_TTL_MS,
  RESCUE_TEXTS,
  RescueContextStore,
  RescueNotifier,
  ValidatorMemory,
  buildRescueFields,
  failedNotice,
  finishWithDownex,
  interruptText,
  notificationDownload,
  parseRescueOptions,
  rescueErrorText,
  rescueOffer,
  rescueRetry,
  rescueRow,
  resultNotice,
  savedLine,
  type FinishDeps,
  type RescueItem,
} from './rescue';

const item = (o: Partial<RescueItem> = {}): RescueItem => ({
  id: 4,
  url: 'https://cdn.example/big.iso',
  filename: 'C:\\Users\\u\\Downloads\\big.iso',
  state: 'interrupted',
  error: 'NETWORK_FAILED',
  bytesReceived: 412_000_000,
  totalBytes: 1_200_000_000,
  ...o,
});

class MemoryArea implements StorageAreaLike {
  data: Record<string, unknown> = {};
  async get(key: string) {
    return key in this.data ? { [key]: structuredClone(this.data[key]) } : {};
  }
  async set(items: Record<string, unknown>) {
    Object.assign(this.data, structuredClone(items));
  }
}

describe('rescueOffer (R-E1)', () => {
  it.each([
    ['NETWORK_FAILED', 'finish'],
    ['SERVER_FAILED', 'finish'],
    ['FILE_NO_SPACE', 'finish'],
    ['USER_SHUTDOWN', 'finish'],
    ['CRASH', 'finish'],
    ['USER_CANCELED', undefined],
    ['FILE_BLOCKED', undefined],
    ['FILE_VIRUS_INFECTED', undefined],
    ['FILE_SECURITY_CHECK_FAILED', undefined],
  ])('Chromium %s → %s', (error, want) => {
    expect(rescueOffer(item({ error }), false)).toBe(want);
  });

  it('Firefox: an error or a pause, never a cancel', () => {
    expect(rescueOffer(item({ error: 'NETWORK_FAILED' }), true)).toBe('finish');
    expect(rescueOffer(item({ error: 'USER_CANCELED' }), true)).toBeUndefined();
    expect(rescueOffer(item({ error: undefined, paused: true }), true)).toBe('finish');
    expect(rescueOffer(item({ error: 'USER_CANCELED', paused: true }), true)).toBe('finish');
  });

  it('never a flagged, private, POST, unnamed, non-http or running download', () => {
    expect(rescueOffer(item({ danger: 'file' }), false)).toBeUndefined();
    expect(rescueOffer(item({ danger: 'uncommon' }), false)).toBeUndefined();
    expect(rescueOffer(item({ danger: 'safe' }), false)).toBe('finish');
    expect(rescueOffer(item({ danger: 'accepted' }), false)).toBe('finish');
    expect(rescueOffer(item({ incognito: true }), false)).toBeUndefined();
    expect(rescueOffer(item(), false, 'POST')).toBeUndefined();
    expect(rescueOffer(item(), false, 'GET')).toBe('finish');
    expect(rescueOffer(item({ filename: '' }), false)).toBeUndefined();
    expect(rescueOffer(item({ url: 'ftp://x/a' }), false)).toBeUndefined();
    expect(rescueOffer(item({ state: 'in_progress' }), false)).toBeUndefined();
    expect(rescueOffer(item({ state: 'complete' }), false)).toBeUndefined();
  });

  it('nothing saved: download it from the start', () => {
    expect(rescueOffer(item({ bytesReceived: 0 }), false)).toBe('download');
  });
});

describe('interruptText', () => {
  it.each([
    ['NETWORK_TIMEOUT', false, 'Network failed'],
    ['NETWORK_INVALID_REQUEST', false, 'Network failed'],
    ['SERVER_NO_RANGE', false, 'Server error'],
    ['SERVER_CROSS_ORIGIN_REDIRECT', false, 'Server error'],
    ['SERVER_FORBIDDEN', false, 'Refused by the server'],
    ['SERVER_CERT_PROBLEM', false, 'Refused by the server'],
    ['FILE_NO_SPACE', false, 'Disk full'],
    ['FILE_TOO_LARGE', false, 'Too large for the disk'],
    ['FILE_ACCESS_DENIED', false, 'Could not write the file'],
    ['USER_SHUTDOWN', false, 'Browser closed'],
    ['CRASH', false, 'Browser crashed'],
    [undefined, true, 'Paused'],
    ['SOMETHING_NEW', false, 'Stopped'],
  ])('%s (paused %s) → %s', (error, paused, want) => {
    expect(interruptText(error, paused)).toBe(want);
  });

  it('saved line in decimal units', () => {
    expect(savedLine(item())).toBe('Network failed · 412 MB of 1.2 GB saved');
    expect(savedLine(item({ totalBytes: -1, bytesReceived: 41_200_000 }))).toBe('Network failed · 41.2 MB saved');
  });
});

describe('RescueContextStore (R-E2)', () => {
  it('keeps at most 20 for 24 h, filtered, in storage.session', async () => {
    let now = 1_000;
    const area = new MemoryArea();
    const s = new RescueContextStore(area, () => now);
    for (let i = 0; i < RESCUE_CONTEXT_MAX + 3; i++) {
      now++;
      await s.put(i, { url: `https://a.example/${i}`, method: 'GET', headers: [['Cookie', 'a=1'], ['Range', 'bytes=0-'], ['Host', 'x']] });
    }
    expect(await s.get(0)).toBeUndefined();
    expect(await s.get(3)).toMatchObject({ url: 'https://a.example/3', headers: [['Cookie', 'a=1']] });
    expect(Object.keys(area.data.rescueContext as object)).toHaveLength(RESCUE_CONTEXT_MAX);
    now += RESCUE_CONTEXT_TTL_MS + 1;
    expect(await s.get(22)).toBeUndefined();
  });

  it('keeps nothing in storage without storage.session (memory only)', async () => {
    const s = new RescueContextStore(undefined);
    await s.put(1, { url: 'https://a.example/x', method: 'GET', headers: [['Cookie', 'a=1']], etag: '"e"' });
    expect(await s.get(1)).toMatchObject({ etag: '"e"' });
    await s.delete(1);
    expect(await s.get(1)).toBeUndefined();
  });

  it('validators for a minute by address and cookie store', () => {
    let now = 0;
    const v = new ValidatorMemory(() => now);
    v.record('https://a.example/x', undefined, '"e"', undefined);
    v.record('https://a.example/y', 'c1', undefined, 'Mon, 02 Jan 2026 10:00:00 GMT');
    v.record('https://a.example/z', undefined, undefined, undefined);
    expect(v.get('https://a.example/x', undefined)).toEqual({ etag: '"e"' });
    expect(v.get('https://a.example/y', undefined)).toBeUndefined();
    expect(v.get('https://a.example/y', 'c1')).toEqual({ lastModified: 'Mon, 02 Jan 2026 10:00:00 GMT' });
    expect(v.get('https://a.example/z', undefined)).toBeUndefined();
    now = 61_000;
    expect(v.get('https://a.example/x', undefined)).toBeUndefined();
  });
});

describe('buildRescueFields (R-E3)', () => {
  const deps = { getCookies: async () => [{ name: 'sid', value: '9' }], userAgent: 'UA/1' };
  it('a saved context: its headers and validators, no cookie lookup', async () => {
    const f = await buildRescueFields(
      item({ startTime: '2026-01-02T10:00:00.000Z', referrer: 'https://page.example/p#x' }),
      { url: 'https://orig.example/big.iso', method: 'GET', headers: [['Cookie', 'a=1'], ['Referer', 'https://page.example/']], etag: 'W/"v1"', lastModified: 'x\ny', ts: 1 },
      { ...deps, getCookies: async () => { throw new Error('not asked'); } },
    );
    expect(f).toEqual({
      url: 'https://orig.example/big.iso',
      headers: [['Cookie', 'a=1'], ['Referer', 'https://page.example/'], ['User-Agent', 'UA/1']],
      target: 'C:\\Users\\u\\Downloads\\big.iso',
      bytesReceived: 412_000_000,
      totalBytes: 1_200_000_000,
      startTime: Date.parse('2026-01-02T10:00:00.000Z'),
      etag: 'W/"v1"',
      pageUrl: 'https://page.example/p',
      startPaused: true,
      deadlineMs: 30_000,
    });
  });

  it('without one: the cookies of that address and the default-policy Referer', async () => {
    const f = await buildRescueFields(item({ referrer: 'https://page.example/p', totalBytes: 0 }), undefined, deps);
    expect(f.headers).toEqual([['Cookie', 'sid=9'], ['Referer', 'https://page.example/'], ['User-Agent', 'UA/1']]);
    expect(f.totalBytes).toBe(-1);
    expect(f.etag).toBeUndefined();
  });
});

/** A fake downloads API and native client recording the order of calls. */
function fakeFlow(o: { items?: RescueItem[]; rescue?: Outcome<RescuedResult>; resume?: Outcome<ResumedResult>; add?: Outcome<{ type: 'added'; downloadId: number }>; late?: boolean } = {}) {
  const calls: string[] = [];
  let items = o.items ?? [item()];
  let lateCb: ((x: Outcome<RescuedResult>) => void) | undefined;
  const lates: unknown[] = [];
  const deps: FinishDeps = {
    isFirefox: false,
    downloads: {
      search: async ({ id }) => {
        calls.push(`search ${id}`);
        return items.filter((i) => i.id === id);
      },
      cancel: async (id) => {
        calls.push(`cancel ${id}`);
        items = items.map((i) => (i.id === id ? { ...i, state: 'interrupted', error: 'USER_CANCELED' } : i));
      },
      erase: async ({ id }) => {
        calls.push(`erase ${id}`);
        items = items.filter((i) => i.id !== id);
      },
    },
    client: {
      rescue: async (f: RescueFields, opts) => {
        calls.push(`rescue ${f.bytesReceived} paused=${f.startPaused}`);
        if (o.late) {
          lateCb = opts?.onLate;
          return failure('timeout', 'no rescue response within 32000 ms');
        }
        return o.rescue ?? { ok: true, value: { type: 'rescued', downloadId: 57, keptBytes: 4096 } };
      },
      resume: async (id) => {
        calls.push(`resume ${id}`);
        return o.resume ?? { ok: true, value: { type: 'resumed', downloadId: id } };
      },
      add: async (f: AddFields) => {
        calls.push(`add ${f.url}`);
        return o.add ?? { ok: true, value: { type: 'added', downloadId: 58 } };
      },
    },
    fields: async (i) => ({ url: i.url, headers: [], target: i.filename, bytesReceived: i.bytesReceived ?? 0, startPaused: true }),
    addFields: async (i) => ({ ok: true, value: { url: i.url, method: 'GET', headers: [] } }),
    onLate: (x) => lates.push(x),
  };
  return { deps, calls, setRunning: () => (items = items.map((i) => ({ ...i, state: 'in_progress' }))), late: (x: Outcome<RescuedResult>) => lateCb?.(x), lates };
}

describe('finishWithDownex (behaviour A.4)', () => {
  it('rescue paused, erase the entry, then the first start', async () => {
    const f = fakeFlow();
    await expect(finishWithDownex(4, f.deps)).resolves.toEqual({ ok: true, value: { downloadId: 57, keptBytes: 4096, resumed: true, fromStart: false } });
    expect(f.calls).toEqual(['search 4', 'rescue 412000000 paused=true', 'search 4', 'erase 4', 'resume 57']);
  });

  it('cancels an entry the browser runs again before erasing it', async () => {
    const f = fakeFlow();
    const rescue = f.deps.client.rescue;
    f.deps.client.rescue = async (x, o) => {
      const r = await rescue(x, o);
      f.setRunning();
      return r;
    };
    await finishWithDownex(4, f.deps);
    expect(f.calls.slice(2)).toEqual(['search 4', 'cancel 4', 'erase 4', 'resume 57']);
  });

  it('a refused rescue leaves the entry; a failed first start is reported', async () => {
    const refused = fakeFlow({ rescue: failure('rejected', 'rescue_changed: bytes 1-2 differ', 'rescue_changed') });
    await expect(finishWithDownex(4, refused.deps)).resolves.toMatchObject({ ok: false, error: { reason: 'rescue_changed' } });
    expect(refused.calls).not.toContain('erase 4');
    const paused = fakeFlow({ resume: failure('rejected', 'not a rescue') });
    await expect(finishWithDownex(4, paused.deps)).resolves.toMatchObject({ ok: true, value: { resumed: false } });
  });

  it('a late rescued still erases and starts it', async () => {
    const f = fakeFlow({ late: true });
    await expect(finishWithDownex(4, f.deps)).resolves.toMatchObject({ ok: false, error: { code: 'timeout' } });
    expect(f.calls).not.toContain('erase 4');
    f.late({ ok: true, value: { type: 'rescued', downloadId: 60, keptBytes: 9 } });
    await new Promise((r) => setTimeout(r, 0));
    await new Promise((r) => setTimeout(r, 0));
    expect(f.calls.slice(-2)).toEqual(['erase 4', 'resume 60']);
    expect(f.lates).toEqual([{ ok: true, value: { downloadId: 60, keptBytes: 9, resumed: true, fromStart: false } }]);
  });

  it('nothing saved, or from the start: add, and erase only once Downex has it', async () => {
    const empty = fakeFlow({ items: [item({ bytesReceived: 0 })] });
    await expect(finishWithDownex(4, empty.deps)).resolves.toMatchObject({ ok: true, value: { downloadId: 58, fromStart: true } });
    expect(empty.calls).toEqual(['search 4', 'add https://cdn.example/big.iso', 'search 4', 'erase 4']);
    const refused = fakeFlow({ add: failure('daemon_unavailable', 'downexd is not running') });
    await finishWithDownex(4, refused.deps, true);
    expect(refused.calls).toEqual(['search 4', 'add https://cdn.example/big.iso']);
  });

  it('stops when the download is no longer offered', async () => {
    const f = fakeFlow({ items: [item({ error: 'USER_CANCELED' })] });
    await expect(finishWithDownex(4, f.deps)).resolves.toMatchObject({ ok: false, error: { code: 'gone' } });
    expect(f.calls).toEqual(['search 4']);
  });
});

describe('texts (R-E7)', () => {
  it('words the codes, with the macOS privacy hint', () => {
    const e = (reason: string) => failure('rejected', `${reason}: details`, reason).error;
    expect(rescueErrorText(e('rescue_changed'), false)).toBe(RESCUE_TEXTS.rescue_changed);
    expect(rescueErrorText(e('rescue_no_access'), true)).toBe(RESCUE_TEXTS.rescue_no_access + MAC_NO_ACCESS);
    expect(rescueErrorText(e('rescue_no_access'), false)).toBe(RESCUE_TEXTS.rescue_no_access);
    expect(rescueErrorText(e('disk_full'), false)).toBe('details');
    expect(rescueErrorText(e('constructor'), false)).toBe('details');
    expect(rescueErrorText(failure('timeout', 'x').error, false)).toBe('Downex did not answer in time. Check its list before trying again.');
    expect(rescueErrorText(failure('gone', 'x').error, false)).toBe("This download is no longer in the browser's list.");
  });

  it('offers a retry or a download from the start', () => {
    const e = (reason: string) => failure('rejected', reason, reason).error;
    expect(rescueRetry(e('rescue_changed'))).toBe('from-start');
    expect(rescueRetry(e('rescue_no_ranges'))).toBe('from-start');
    expect(rescueRetry(e('rescue_size_unknown'))).toBe('from-start');
    expect(rescueRetry(e('rescue_locked'))).toBe('retry');
    expect(rescueRetry(e('link_expired'))).toBe('none');
    expect(rescueRetry(failure('daemon_unavailable', 'x').error)).toBe('retry');
  });

  it('the rows of the card', () => {
    const row = { downloadId: 4, name: 'big.iso', line: savedLine(item()), kind: 'finish' as const };
    expect(rescueRow(row)).toEqual({ line: 'Network failed · 412 MB of 1.2 GB saved', tone: 'muted', button: 'Finish with Downex', busy: false, action: 'finish' });
    expect(rescueRow({ ...row, kind: 'download' }).button).toBe('Download with Downex');
    expect(rescueRow({ ...row, state: { phase: 'checking' } })).toMatchObject({ line: 'Checking the file with the server…', busy: true });
    expect(rescueRow({ ...row, state: { phase: 'done', keptBytes: 412_000_000, resumed: true, fromStart: false, at: 0 } })).toMatchObject({
      line: 'Continuing in Downex from 412 MB',
      button: '',
    });
    expect(rescueRow({ ...row, state: { phase: 'done', keptBytes: 1, resumed: false, fromStart: false, at: 0 } }).line).toBe('Downex has it, paused. Resume it in Downex.');
    expect(rescueRow({ ...row, state: { phase: 'failed', text: 'x', retry: 'from-start' } })).toMatchObject({ button: 'Download from the start with Downex', action: 'from-start' });
    expect(rescueRow({ ...row, state: { phase: 'failed', text: 'x', retry: 'retry' } })).toMatchObject({ button: 'Retry', action: 'finish' });
  });

  it('notifications', () => {
    expect(failedNotice(item())).toEqual({ title: 'Download failed: big.iso', message: 'Network failed. Downex can finish it from 412 MB.' });
    expect(resultNotice('big.iso', { ok: true, value: { downloadId: 1, keptBytes: 412_000_000, resumed: true, fromStart: false } }, false)).toEqual({
      title: 'Continuing in Downex',
      message: 'big.iso from 412 MB',
    });
    expect(resultNotice('big.iso', failure('rejected', 'r', 'rescue_locked'), false)).toEqual({
      title: 'Downex could not finish big.iso',
      message: RESCUE_TEXTS.rescue_locked,
    });
    expect(notificationDownload('downex-rescue:12')).toBe(12);
    expect(notificationDownload('other:12')).toBeUndefined();
  });
});

describe('RescueNotifier (R-E6)', () => {
  it('once per download, 3 a minute, never for the scan or without the option or permission', () => {
    let now = 0;
    const n = new RescueNotifier(() => now);
    const on = { enabled: true, permitted: true, fromScan: false };
    expect(n.allow(1, { ...on, enabled: false })).toBe(false);
    expect(n.allow(1, { ...on, permitted: false })).toBe(false);
    expect(n.allow(1, { ...on, fromScan: true })).toBe(false);
    expect([1, 1, 2, 3, 4].map((id) => n.allow(id, on))).toEqual([true, false, true, true, false]);
    now = 60_001;
    expect(n.allow(4, on)).toBe(true);
  });
});

describe('FailedList', () => {
  it('at most 5, hidden ones left out, finished rows for 5 s', () => {
    let now = 0;
    const l = new FailedList(() => now);
    const offered = [1, 2, 3, 4, 5, 6, 7].map((id) => ({ item: item({ id, filename: `/d/f${id}.bin` }), kind: 'finish' as const }));
    expect(l.rows(offered, new Set([2])).map((r) => r.downloadId)).toEqual([1, 3, 4, 5, 6]);
    l.set(9, { phase: 'done', keptBytes: 1, resumed: true, fromStart: false, at: 0 }, { downloadId: 9, name: 'f9', line: '', kind: 'finish' });
    expect(l.rows([], new Set()).map((r) => r.downloadId)).toEqual([9]);
    now = 5_001;
    expect(l.rows([], new Set())).toEqual([]);
  });

  it('the option is off unless stored on', () => {
    expect(parseRescueOptions(undefined)).toEqual({ rescueNotify: false });
    expect(parseRescueOptions({ rescueNotify: 'yes' })).toEqual({ rescueNotify: false });
    expect(parseRescueOptions({ rescueNotify: true })).toEqual({ rescueNotify: true });
  });
});
