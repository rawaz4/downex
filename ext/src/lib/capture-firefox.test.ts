import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  AttemptMemory,
  classifyResponse,
  createDownloadCreatedListener,
  createHeadersReceivedListener,
  type HeadersReceivedDetailsLike,
  parseContentDisposition,
} from './capture-firefox';
import { HANDOFF_BACKSTOP_SLACK_MS } from './capture-chromium';
import { failure, type Outcome } from './protocol';
import type { CaptureDecision } from './rules';

const CAPTURE: CaptureDecision = { capture: true, extension: 'zip' };

function details(over: Partial<HeadersReceivedDetailsLike> = {}): HeadersReceivedDetailsLike {
  return {
    requestId: '1',
    url: 'https://a.example/get?id=1',
    method: 'GET',
    type: 'main_frame',
    statusCode: 200,
    tabId: 3,
    originUrl: 'https://a.example/page',
    responseHeaders: [
      { name: 'Content-Type', value: 'application/octet-stream' },
      { name: 'Content-Disposition', value: 'attachment; filename="r\\"eport.zip"' },
      { name: 'Content-Length', value: '1234' },
    ],
    ...over,
  };
}

beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
});

describe('parseContentDisposition', () => {
  it('parses quoted and RFC 8187 file names', () => {
    expect(parseContentDisposition('attachment; filename="a; b.zip"')).toEqual({ type: 'attachment', filename: 'a; b.zip' });
    expect(parseContentDisposition("attachment; filename=x.zip; filename*=UTF-8''%E2%82%AC%20rates.zip")).toEqual({
      type: 'attachment',
      filename: '€ rates.zip',
    });
    expect(parseContentDisposition('INLINE')).toEqual({ type: 'inline' });
    expect(parseContentDisposition(undefined)).toBeUndefined();
  });
});

describe('classifyResponse', () => {
  it('takes attachments with name, MIME, size and referrer', () => {
    expect(classifyResponse(details())).toEqual({
      candidate: {
        url: 'https://a.example/get?id=1',
        filename: 'r"eport.zip',
        mime: 'application/octet-stream',
        size: 1234,
        tabId: 3,
        pageUrl: 'https://a.example/page',
      },
      target: {
        url: 'https://a.example/get?id=1',
        filename: 'r"eport.zip',
        mime: 'application/octet-stream',
        size: 1234,
        referrer: 'https://a.example/page',
      },
    });
  });
  it('ignores inline content, redirects and other types', () => {
    expect(classifyResponse(details({ responseHeaders: [{ name: 'Content-Type', value: 'text/html' }] }))).toBeUndefined();
    expect(classifyResponse(details({ responseHeaders: [{ name: 'content-type', value: 'video/mp4' }] }))).toBeUndefined();
    expect(classifyResponse(details({ statusCode: 302 }))).toBeUndefined();
    expect(classifyResponse(details({ type: 'xmlhttprequest' }))).toBeUndefined();
    expect(classifyResponse(details({ responseHeaders: [{ name: 'Content-Type', value: 'application/zip' }] }))).toBeDefined();
  });
  it('leaves POST responses to the browser: a replayed form may not work twice', () => {
    expect(classifyResponse(details({ method: 'POST' }))).toBeUndefined();
  });
  it('keeps the container of the request', () => {
    expect(classifyResponse(details({ cookieStoreId: 'firefox-container-2' }))?.target.storeId).toBe('firefox-container-2');
  });
  it('knows no tab or page for requests without them', () => {
    const c = classifyResponse(details({ tabId: -1, originUrl: 'moz-extension://x/page.html' }))?.candidate;
    expect(c).toBeDefined();
    expect(c).not.toHaveProperty('tabId');
    expect(c).not.toHaveProperty('pageUrl');
  });
});

describe('onHeadersReceived listener', () => {
  function setup(handOff: () => Promise<Outcome<unknown>>, decide: () => CaptureDecision = () => CAPTURE) {
    const attempts = new AttemptMemory();
    const listener = createHeadersReceivedListener({ decide, handOff, attempts });
    return { listener, attempts };
  }

  it('cancels the request once added', async () => {
    const { listener, attempts } = setup(async () => ({ ok: true, value: {} }));
    await expect(listener(details())).resolves.toEqual({ cancel: true });
    expect(attempts.consume('https://a.example/get?id=1')).toBe(true);
  });
  it('lets the browser download on error', async () => {
    const { listener } = setup(async () => failure('rejected', 'no'));
    await expect(listener(details())).resolves.toEqual({});
  });
  it('lets the browser download when the add times out', async () => {
    const { listener } = setup(async () => failure('timeout', 'no add response within 1500 ms'));
    await expect(listener(details())).resolves.toEqual({});
  });
  it('gives the add what is left of the 1.5 s and does not race its timer', async () => {
    const budgets: number[] = [];
    const attempts = new AttemptMemory();
    const listener = createHeadersReceivedListener({
      decide: () => new Promise((r) => setTimeout(() => r(CAPTURE), 500)),
      handOff: async (_t, ms) => {
        budgets.push(ms);
        // Answered at the very end of its budget: the listener must still cancel.
        await new Promise((r) => setTimeout(r, ms));
        return { ok: true, value: {} };
      },
      attempts,
    });
    const r = listener(details());
    await vi.advanceTimersByTimeAsync(1500);
    await expect(r).resolves.toEqual({ cancel: true });
    expect(budgets).toEqual([1000]);
  });
  it('abandons a hand-off that never answers', async () => {
    const { listener } = setup(() => new Promise(() => undefined));
    const r = listener(details());
    await vi.advanceTimersByTimeAsync(1500 + HANDOFF_BACKSTOP_SLACK_MS);
    await expect(r).resolves.toEqual({});
  });
  it('returns {} synchronously for non-downloads and skipped candidates', async () => {
    const { listener } = setup(async () => ({ ok: true, value: {} }));
    expect(listener(details({ responseHeaders: [] }))).toEqual({});
    const skip = setup(async () => ({ ok: true, value: {} }), () => ({ capture: false, reason: 'bypass' }));
    await expect(skip.listener(details())).resolves.toEqual({});
  });
});

describe('onCreated fallback', () => {
  it('skips URLs the headers path tried and hands off others', async () => {
    const calls: string[] = [];
    const attempts = new AttemptMemory();
    attempts.mark('https://a.example/tried.zip');
    const listener = createDownloadCreatedListener({
      downloads: {
        pause: async (id) => void calls.push(`pause:${id}`),
        resume: async (id) => void calls.push(`resume:${id}`),
        cancel: async (id) => void calls.push(`cancel:${id}`),
        erase: async (q) => void calls.push(`erase:${q.id}`),
      },
      decide: () => CAPTURE,
      handOff: async () => ({ ok: true, value: {} }),
      attempts,
      ownExtensionId: 'me',
    });
    listener({ id: 1, url: 'https://a.example/tried.zip', filename: '', incognito: false });
    listener({ id: 2, url: 'https://a.example/x.zip', filename: '', incognito: false, byExtensionId: 'me' });
    listener({ id: 3, url: 'https://a.example/new.zip', filename: '', incognito: false });
    await vi.waitFor(() => expect(calls).toEqual(['pause:3', 'cancel:3', 'erase:3']));
  });
});
