import { describe, expect, it } from 'vitest';
import { CaptureTrace, TRACE_LIMIT, TRACE_STORAGE_KEY, parseTrace, traceUrl } from './capture-trace';

function memoryArea() {
  const data: Record<string, unknown> = {};
  return {
    data,
    get: async (key: string) => ({ [key]: data[key] }),
    set: async (items: Record<string, unknown>) => {
      Object.assign(data, items);
    },
  };
}

describe('capture log', () => {
  it('keeps origin and path only, so one-time tokens are not stored', () => {
    expect(traceUrl('https://testfile.org/1.3GBiconpng?__cf_chl_tk=secret#x')).toBe('https://testfile.org/1.3GBiconpng?…');
    expect(traceUrl('https://cdn.example/f.zip')).toBe('https://cdn.example/f.zip');
    expect(traceUrl('blob:https://site.example/1234')).toBe('blob:…');
    expect(traceUrl('not a url')).toBe('not a url');
  });

  it('stores entries newest first, bounded, with serialised writes', async () => {
    const area = memoryArea();
    const trace = new CaptureTrace(area);
    await Promise.all(
      Array.from({ length: TRACE_LIMIT + 3 }, (_, i) =>
        trace.add({ ts: i, step: 'created', url: `https://a.example/${i}?q=1`, detail: `d${i}`, downloadId: i }),
      ),
    );
    const list = parseTrace(area.data[TRACE_STORAGE_KEY]);
    expect(list).toHaveLength(TRACE_LIMIT);
    expect(list[0]).toEqual({ ts: TRACE_LIMIT + 2, step: 'created', url: `https://a.example/${TRACE_LIMIT + 2}?…`, detail: `d${TRACE_LIMIT + 2}`, downloadId: TRACE_LIMIT + 2 });
  });

  it('drops malformed stored entries', () => {
    expect(parseTrace([{ ts: 1, step: 'decision', url: 'u', detail: 'd' }, { ts: 'x' }, { ts: 1, step: 'bogus', url: 'u', detail: 'd' }, null])).toEqual([
      { ts: 1, step: 'decision', url: 'u', detail: 'd' },
    ]);
    expect(parseTrace('nope')).toEqual([]);
  });
});
