import { describe, expect, it } from 'vitest';
import {
  CONTEXT_TTL_MS,
  MAX_HEADER_COUNT,
  RequestContextStore,
  buildAddFields,
  defaultPolicyReferer,
  encodeRequestBody,
  filterHeaders,
  findContext,
  resolveBody,
  type BuildAddDeps,
  type CookieLike,
} from './context';
import type { HeaderPair } from './protocol';

function clock(start = 1000) {
  let t = start;
  return { now: () => t, advance: (ms: number) => (t += ms) };
}

describe('RequestContextStore', () => {
  it('expires entries after the TTL', () => {
    const c = clock();
    const s = new RequestContextStore({ now: c.now });
    s.recordHeaders('https://a/x.zip', 'get', [['Cookie', 'a=1']]);
    c.advance(CONTEXT_TTL_MS);
    expect(s.get('https://a/x.zip')?.method).toBe('GET');
    c.advance(1);
    expect(s.get('https://a/x.zip')).toBeUndefined();
    expect(s.size).toBe(0);
  });

  it('refreshes TTL on update and prunes expired entries on insert', () => {
    const c = clock();
    const s = new RequestContextStore({ now: c.now });
    s.recordHeaders('https://a/1', 'GET', []);
    s.recordHeaders('https://a/2', 'GET', []);
    c.advance(CONTEXT_TTL_MS - 10);
    s.recordHeaders('https://a/1', 'GET', []);
    c.advance(20);
    s.recordHeaders('https://a/3', 'GET', []);
    expect(s.size).toBe(2);
    expect(s.get('https://a/2')).toBeUndefined();
    expect(s.get('https://a/1')).toBeDefined();
  });

  it('merges a POST body with the headers of the same request only', () => {
    const s = new RequestContextStore();
    const body = encodeRequestBody({ raw: [{ bytes: new TextEncoder().encode('q=1').buffer }] });
    s.recordBody('https://a/post', body);
    s.recordHeaders('https://a/post', 'POST', [['Content-Type', 'text/plain']]);
    expect(s.get('https://a/post')?.body).toEqual({ kind: 'data', base64: btoa('q=1'), bytes: 3 });
    // A later GET to the same URL replaces the record and drops the body.
    s.recordHeaders('https://a/post', 'GET', []);
    expect(s.get('https://a/post')?.body).toBeUndefined();
  });

  it('keeps requests of different cookie stores (containers) apart', () => {
    const s = new RequestContextStore();
    s.recordHeaders('https://a/f.zip', 'GET', [['Cookie', 'work=1']], 'firefox-container-1');
    expect(s.get('https://a/f.zip')).toBeUndefined();
    expect(s.get('https://a/f.zip', 'firefox-default')).toBeUndefined();
    expect(s.get('https://a/f.zip', 'firefox-container-1')?.headers).toEqual([['Cookie', 'work=1']]);
    s.recordHeaders('https://a/f.zip', 'GET', [['Cookie', 'home=1']]);
    expect(s.size).toBe(2);
  });

  it('remembers the tab that sent the request', () => {
    const s = new RequestContextStore();
    s.recordHeaders('https://a/f.zip', 'GET', [], undefined, 12);
    expect(s.get('https://a/f.zip')?.tabId).toBe(12);
    // webRequest reports -1 for requests that no tab sent.
    s.recordHeaders('https://a/g.zip', 'GET', [], undefined, -1);
    expect(s.get('https://a/g.zip')).not.toHaveProperty('tabId');
  });

  it('evicts the oldest entries beyond maxEntries', () => {
    const s = new RequestContextStore({ maxEntries: 2 });
    s.recordHeaders('https://a/1', 'GET', []);
    s.recordHeaders('https://a/2', 'GET', []);
    s.recordHeaders('https://a/3', 'GET', []);
    expect(s.get('https://a/1')).toBeUndefined();
    expect(s.size).toBe(2);
  });
});

describe('filterHeaders', () => {
  it('drops forbidden, pseudo and malformed headers', () => {
    const input: HeaderPair[] = [
      ['Accept', 'text/html'],
      ['Host', 'a'],
      ['Connection', 'keep-alive'],
      ['Content-Length', '3'],
      ['Range', 'bytes=0-'],
      ['If-Range', 'x'],
      ['Transfer-Encoding', 'chunked'],
      ['Proxy-Authorization', 'Basic x'],
      [':authority', 'a'],
      ['Bad Name', 'x'],
      ['X-Inject', 'a\r\nEvil: 1'],
      ['Cookie', 'a=1'],
      ['Referer', 'https://r/'],
      ['User-Agent', 'UA'],
      ['Accept-Encoding', 'gzip'],
    ];
    expect(filterHeaders(input)).toEqual([
      ['Cookie', 'a=1'],
      ['Referer', 'https://r/'],
      ['User-Agent', 'UA'],
      ['Accept', 'text/html'],
      ['Accept-Encoding', 'gzip'],
    ]);
  });

  it('enforces count and byte limits while keeping priority headers', () => {
    const many: HeaderPair[] = Array.from({ length: 150 }, (_, i) => [`X-${i}`, 'v'] as HeaderPair);
    many.push(['Cookie', 'c'.repeat(1000)]);
    const out = filterHeaders(many);
    expect(out).toHaveLength(MAX_HEADER_COUNT);
    expect(out[0]).toEqual(['Cookie', 'c'.repeat(1000)]);
    const big: HeaderPair[] = [
      ['X-Big', 'x'.repeat(64 * 1024)],
      ['Cookie', 'a=1'],
    ];
    expect(filterHeaders(big)).toEqual([['Cookie', 'a=1']]);
  });
});

describe('request bodies', () => {
  it('encodes raw bytes, rejects files and oversize bodies', () => {
    const bytes = new Uint8Array([0, 255, 1]).buffer;
    expect(encodeRequestBody({ raw: [{ bytes }] })).toEqual({ kind: 'data', base64: 'AP8B', bytes: 3 });
    expect(encodeRequestBody({ raw: [{ file: 'C:/x' }] }).kind).toBe('unavailable');
    expect(encodeRequestBody({ raw: [{ bytes: new ArrayBuffer(11) }] }, 10)).toEqual({ kind: 'too_large' });
    expect(encodeRequestBody({ error: 'oops' }).kind).toBe('unavailable');
    expect(encodeRequestBody(undefined)).toEqual({ kind: 'data', base64: '', bytes: 0 });
  });

  it('rebuilds urlencoded forms only', () => {
    const form = encodeRequestBody({ formData: { a: ['1', '2'], 'b c': ['é'] } });
    const r = resolveBody(form, 'application/x-www-form-urlencoded; charset=UTF-8');
    expect(r.ok && atob(r.value)).toBe(new TextDecoder('latin1').decode(new TextEncoder().encode('a=1&a=2&b+c=%C3%A9')));
    expect(resolveBody(form, 'multipart/form-data; boundary=x').ok).toBe(false);
  });

  it('names the body limit as an exact byte count', () => {
    expect(resolveBody({ kind: 'too_large' }, undefined)).toMatchObject({ ok: false, error: { message: 'POST body exceeds 1,048,576 bytes' } });
    const form = encodeRequestBody({ formData: { a: ['x'.repeat(20)] } });
    expect(resolveBody(form, 'application/x-www-form-urlencoded', 10)).toMatchObject({ ok: false, error: { message: 'POST body exceeds 10 bytes' } });
  });
});

describe('buildAddFields', () => {
  function deps(cookies: CookieLike[] = [], store = new RequestContextStore()): BuildAddDeps & { calls: string[] } {
    const calls: string[] = [];
    return {
      contexts: store,
      getCookies: async (url, storeId) => {
        calls.push(storeId ? `${storeId} ${url}` : url);
        return cookies;
      },
      userAgent: 'TestUA/1',
      calls,
    };
  }

  it('uses recorded headers and does not read cookies when context exists', async () => {
    const store = new RequestContextStore();
    store.recordHeaders('https://a/f.zip', 'GET', [
      ['Cookie', 'sid=1'],
      ['User-Agent', 'RealUA'],
      ['Host', 'a'],
    ]);
    const d = deps([{ name: 'other', value: 'x' }], store);
    const r = await buildAddFields({ url: 'https://a/f.zip', filename: 'dir/f.zip', referrer: 'https://a/page?q=1', size: 10 }, d);
    expect(d.calls).toEqual([]);
    // The browser sent no Referer (e.g. Referrer-Policy: no-referrer): none is made up.
    expect(r).toEqual({
      ok: true,
      value: {
        url: 'https://a/f.zip',
        method: 'GET',
        headers: [
          ['Cookie', 'sid=1'],
          ['User-Agent', 'RealUA'],
        ],
        filename: 'f.zip',
        size: 10,
      },
    });
  });

  it('forwards the Referer the browser actually sent', async () => {
    const store = new RequestContextStore();
    store.recordHeaders('https://cdn/f.zip', 'GET', [['Referer', 'https://a/']]);
    const r = await buildAddFields({ url: 'https://cdn/f.zip', referrer: 'https://a/inbox?token=SECRET' }, deps([], store));
    expect(r.ok && r.value.headers).toEqual([
      ['Referer', 'https://a/'],
      ['User-Agent', 'TestUA/1'],
    ]);
    expect(r.ok && r.value.referrer).toBe('https://a/');
  });

  it('without a recorded request, derives the Referer as strict-origin-when-cross-origin', async () => {
    const page = 'https://mail.example.com/inbox?token=SECRET#m1';
    const cross = await buildAddFields({ url: 'https://cdn.other.net/a.zip', referrer: page }, deps());
    expect(cross.ok && cross.value.headers).toContainEqual(['Referer', 'https://mail.example.com/']);
    expect(cross.ok && cross.value.referrer).toBe('https://mail.example.com/');
    const down = await buildAddFields({ url: 'http://cdn.other.net/a.zip', referrer: page }, deps());
    expect(down.ok && down.value.headers.some(([n]) => n === 'Referer')).toBe(false);
    expect(down.ok && down.value.referrer).toBeUndefined();
    const same = await buildAddFields({ url: 'https://mail.example.com/a.zip', referrer: page }, deps());
    expect(same.ok && same.value.referrer).toBe('https://mail.example.com/inbox?token=SECRET');
  });

  it('defaultPolicyReferer follows strict-origin-when-cross-origin', () => {
    expect(defaultPolicyReferer('https://u:p@a.example/x?y#z', 'https://a.example/f')).toBe('https://a.example/x?y');
    expect(defaultPolicyReferer('http://a.example/x', 'https://b.example/f')).toBe('http://a.example/');
    expect(defaultPolicyReferer('https://a.example/x', 'http://a.example/f')).toBeUndefined();
    expect(defaultPolicyReferer('http://localhost:8080/x', 'http://b.example/f')).toBeUndefined();
    expect(defaultPolicyReferer('about:blank', 'https://a.example/f')).toBeUndefined();
    expect(defaultPolicyReferer(undefined, 'https://a.example/f')).toBeUndefined();
  });

  it('reads cookies and contexts of the target cookie store only', async () => {
    const store = new RequestContextStore();
    store.recordHeaders('https://a/f.zip', 'GET', [['Cookie', 'home=1']]);
    const d = deps([{ name: 'work', value: '1' }], store);
    const r = await buildAddFields({ url: 'https://a/f.zip', storeId: 'firefox-container-1' }, d);
    expect(d.calls).toEqual(['firefox-container-1 https://a/f.zip']);
    expect(r.ok && r.value.headers).toContainEqual(['Cookie', 'work=1']);
  });

  it('falls back to cookies.getAll for that URL only', async () => {
    const d = deps([
      { name: 'a', value: '1' },
      { name: 'b', value: '2' },
    ]);
    const r = await buildAddFields({ url: 'https://a/f.zip', size: 0 }, d);
    expect(d.calls).toEqual(['https://a/f.zip']);
    expect(r.ok && r.value.headers).toEqual([
      ['Cookie', 'a=1; b=2'],
      ['User-Agent', 'TestUA/1'],
    ]);
    expect(r.ok && r.value.size).toBeUndefined();
  });

  it('uses the final URL context when the original was not seen', async () => {
    const store = new RequestContextStore();
    store.recordHeaders('https://cdn/f.zip', 'GET', [['Cookie', 'c=1']]);
    const r = await buildAddFields({ url: 'https://a/get', finalUrl: 'https://cdn/f.zip' }, deps([], store));
    expect(r.ok && r.value.url).toBe('https://cdn/f.zip');
  });

  // A Cloudflare challenge ends with a form POST to the page's URL, answered by a redirect to
  // the file: the browser fetched the file with a GET of the final URL.
  function postThenRedirect(c = clock(), finalTab = 4, finalMethod = 'GET') {
    const store = new RequestContextStore({ now: c.now });
    store.recordBody('https://site/f', encodeRequestBody({ formData: { tk: ['once'] } }));
    store.recordHeaders('https://site/f', 'POST', [['Content-Type', 'application/x-www-form-urlencoded']], undefined, 4);
    c.advance(5);
    store.recordHeaders('https://cdn/f.zip', finalMethod, [['Cookie', 'cdn=1']], undefined, finalTab);
    return store;
  }

  it('a POST answered by a redirect uses the GET that returned the file', async () => {
    const store = postThenRedirect();
    const target = { url: 'https://site/f', finalUrl: 'https://cdn/f.zip' };
    expect(findContext(store, target)?.method).toBe('GET');
    const r = await buildAddFields(target, deps([], store));
    expect(r).toEqual({
      ok: true,
      value: { url: 'https://cdn/f.zip', method: 'GET', headers: [['Cookie', 'cdn=1'], ['User-Agent', 'TestUA/1']] },
    });
  });

  it('a POST the redirect kept (307/308), one without a redirect, or a final GET of another tab stays a POST', () => {
    const target = { url: 'https://site/f', finalUrl: 'https://cdn/f.zip' };
    expect(findContext(postThenRedirect(clock(), 4, 'POST'), target)?.method).toBe('POST');
    expect(findContext(postThenRedirect(clock(), 9), target)?.method).toBe('POST');
    expect(findContext(postThenRedirect(), { url: 'https://site/f' })?.method).toBe('POST');
    // A GET of the final URL from before the POST is another request.
    const c = clock();
    const store = new RequestContextStore({ now: c.now });
    store.recordHeaders('https://cdn/f.zip', 'GET', [], undefined, 4);
    c.advance(5);
    store.recordHeaders('https://site/f', 'POST', [], undefined, 4);
    expect(findContext(store, target)?.method).toBe('POST');
  });

  it('prefers the original request when it was a GET', () => {
    const store = new RequestContextStore();
    store.recordHeaders('https://site/f', 'GET', [['Cookie', 'site=1']]);
    store.recordHeaders('https://cdn/f.zip', 'GET', [['Cookie', 'cdn=1']]);
    expect(findContext(store, { url: 'https://site/f', finalUrl: 'https://cdn/f.zip' })?.url).toBe('https://site/f');
  });

  it('forwards POST bodies and declines when the body is unknown', async () => {
    const store = new RequestContextStore();
    store.recordBody('https://a/post', encodeRequestBody({ raw: [{ bytes: new TextEncoder().encode('x=1').buffer }] }));
    store.recordHeaders('https://a/post', 'POST', [['Content-Type', 'application/octet-stream']]);
    const ok = await buildAddFields({ url: 'https://a/post' }, deps([], store));
    expect(ok.ok && ok.value.method).toBe('POST');
    expect(ok.ok && ok.value.body).toBe(btoa('x=1'));

    const s2 = new RequestContextStore();
    s2.recordHeaders('https://a/post', 'POST', []);
    const no = await buildAddFields({ url: 'https://a/post' }, deps([], s2));
    expect(no.ok).toBe(false);
    expect(!no.ok && no.error.code).toBe('unsupported');
  });
});
