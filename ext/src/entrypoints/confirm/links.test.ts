import { describe, expect, it } from 'vitest';
import { linkRow } from './links';

describe('linkRow', () => {
  it('names a link by its file, its server and its kind', () => {
    expect(linkRow('https://www.example.com/files/part-01.zip')).toEqual({ name: 'part-01.zip', host: 'example.com', kind: 'archive' });
    expect(linkRow('https://cdn.example.org/a/My%20Talk.mp4?x=1')).toEqual({ name: 'My Talk.mp4', host: 'cdn.example.org', kind: 'video' });
    expect(linkRow('https://example.net/docs/')).toEqual({ name: 'docs', host: 'example.net', kind: 'other' });
  });

  it('keeps an undecodable name as written, and says nothing of an address that does not parse', () => {
    expect(linkRow('https://example.com/100%.pdf').name).toBe('100%.pdf');
    expect(linkRow('not a url')).toEqual({ name: '', host: '', kind: 'other' });
  });
});
