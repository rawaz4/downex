import { describe, expect, it } from 'vitest';
import type { StorageAreaLike } from './history';
import { PendingBatches, fileLinks, handOffLinks, isHttpUrl, uniqueHttpLinks } from './page-links';
import { defaultRules } from './rules';
import type { Outcome } from './protocol';

describe('uniqueHttpLinks', () => {
  it('keeps http(s) links once, in page order, without fragments', () => {
    expect(
      uniqueHttpLinks([
        'https://a.example/file.zip#part1',
        'https://a.example/file.zip',
        'http://b.example/x?y=1',
        'javascript:void(0)',
        'mailto:me@example.com',
        'blob:https://a.example/123',
        'data:text/plain,hi',
        'ftp://c.example/f',
        'not a url',
        42,
        null,
        'https://a.example/file.zip#part2',
        'HTTPS://A.EXAMPLE/other.iso',
      ]),
    ).toEqual(['https://a.example/file.zip', 'http://b.example/x?y=1', 'https://a.example/other.iso']);
  });

  it('accepts only arrays', () => {
    expect(uniqueHttpLinks(undefined)).toEqual([]);
    expect(uniqueHttpLinks('https://a.example/')).toEqual([]);
  });
});

describe('fileLinks', () => {
  it('keeps only links to files of the capture list: never logout or action links', () => {
    const links = [
      'https://mail.example/logout',
      'https://mail.example/items/42/delete?confirm=1',
      'https://cdn.example/setup.exe?sig=1',
      'https://mail.example/inbox',
      'https://cdn.example/Movie.MKV',
      'https://cdn.example/notes.txt',
    ];
    expect(fileLinks(links, defaultRules())).toEqual(['https://cdn.example/setup.exe?sig=1', 'https://cdn.example/Movie.MKV']);
    expect(fileLinks(links, { extensions: ['.TXT'] })).toEqual(['https://cdn.example/notes.txt']);
    expect(fileLinks(links, { extensions: [] })).toEqual([]);
  });
});

describe('isHttpUrl', () => {
  it('recognises http(s) only', () => {
    expect(isHttpUrl('https://x.example/a')).toBe(true);
    expect(isHttpUrl('http://x.example/a')).toBe(true);
    expect(isHttpUrl('blob:https://x.example/1')).toBe(false);
    expect(isHttpUrl('file:///c:/x')).toBe(false);
    expect(isHttpUrl(undefined)).toBe(false);
    expect(isHttpUrl('')).toBe(false);
  });
});

const ok: Outcome<unknown> = { ok: true, value: {} };
const fail = (code: string): Outcome<unknown> => ({ ok: false, error: { code, message: code } });

describe('handOffLinks', () => {
  it('sends every link with bounded concurrency and counts results', async () => {
    let inFlight = 0;
    let peak = 0;
    const seen: string[] = [];
    const links = Array.from({ length: 20 }, (_, i) => `https://x.example/${i}`);
    const summary = await handOffLinks(
      links,
      async (url) => {
        inFlight++;
        peak = Math.max(peak, inFlight);
        await new Promise((r) => setTimeout(r, 1));
        inFlight--;
        seen.push(url);
        return url.endsWith('/7') ? fail('rejected') : ok;
      },
      3,
    );
    expect(summary).toEqual({ sent: 19, failed: 1, skipped: 0 });
    expect(peak).toBe(3);
    expect([...seen].sort()).toEqual([...links].sort());
  });

  it('stops starting new requests after the daemon becomes unavailable', async () => {
    const links = Array.from({ length: 10 }, (_, i) => `https://x.example/${i}`);
    let calls = 0;
    const summary = await handOffLinks(
      links,
      async () => {
        calls++;
        return calls >= 2 ? fail('daemon_unavailable') : ok;
      },
      1,
    );
    expect(summary.sent).toBe(1);
    expect(summary.failed).toBe(1);
    expect(summary.skipped).toBe(8);
    expect(summary.stoppedBy?.code).toBe('daemon_unavailable');
  });

  it('counts a throwing sender as a failure', async () => {
    const summary = await handOffLinks(['https://x.example/a'], () => Promise.reject(new Error('boom')));
    expect(summary).toEqual({ sent: 0, failed: 1, skipped: 0 });
  });

  it('handles an empty list', async () => {
    expect(await handOffLinks([], async () => ok)).toEqual({ sent: 0, failed: 0, skipped: 0 });
  });
});

function memoryArea(): StorageAreaLike & { data: Record<string, unknown> } {
  const data: Record<string, unknown> = {};
  return {
    data,
    get: async (key) => (key in data ? { [key]: structuredClone(data[key]) } : {}),
    set: async (items) => {
      Object.assign(data, structuredClone(items));
    },
  };
}

describe('PendingBatches', () => {
  it('stores, returns and takes a batch once', async () => {
    const area = memoryArea();
    const batches = new PendingBatches(area, () => 1000);
    await batches.put({ id: 'a', links: ['https://x.example/1'], referrer: 'https://x.example/', createdAt: 1000 });
    expect(await batches.get('a')).toEqual({
      id: 'a',
      links: ['https://x.example/1'],
      referrer: 'https://x.example/',
      createdAt: 1000,
    });
    expect((await batches.take('a'))?.links).toEqual(['https://x.example/1']);
    expect(await batches.take('a')).toBeUndefined();
    expect(await batches.get('a')).toBeUndefined();
  });

  it('drops expired batches', async () => {
    let now = 0;
    const batches = new PendingBatches(memoryArea(), () => now, 100);
    await batches.put({ id: 'old', links: [], createdAt: 0 });
    now = 50;
    await batches.put({ id: 'new', links: [], createdAt: 50 });
    now = 120;
    expect(await batches.get('old')).toBeUndefined();
    expect(await batches.get('new')).toBeDefined();
  });

  it('keeps concurrent puts', async () => {
    const batches = new PendingBatches(memoryArea(), () => 0);
    await Promise.all(['a', 'b', 'c'].map((id) => batches.put({ id, links: [], createdAt: 0 })));
    for (const id of ['a', 'b', 'c']) expect(await batches.get(id)).toBeDefined();
  });

  it('ignores malformed stored data', async () => {
    const area = memoryArea();
    area.data.pendingLinkBatches = { x: { id: 'x' }, y: 'junk' };
    const batches = new PendingBatches(area, () => 0);
    expect(await batches.get('x')).toBeUndefined();
  });
});
