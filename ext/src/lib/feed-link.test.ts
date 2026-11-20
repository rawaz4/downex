// @vitest-environment happy-dom

import { beforeEach, describe, expect, it } from 'vitest';
import { FEED_MAX_ANCESTORS, FEED_MAX_LINKS, feedPostLink } from './feed-link';

// The clicked feed video's own post (VIDEO-DETECT-SPEC §5.1 "Feed videos"). The markup below is
// made up for these tests; it only puts links at some distance from a video.

const FEED = 'https://www.instagram.com/';

function el(tag: string, attrs: Record<string, string> = {}, ...children: Node[]): HTMLElement {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v);
  e.append(...children);
  return e;
}

/** A post card: a header with the author and the post's time link, the media, a footer. */
function card(code: string, extra: Node[] = []): { card: HTMLElement; video: HTMLVideoElement } {
  const video = document.createElement('video');
  const media = el('div', { class: 'media' }, el('div', {}, el('div', {}, video)));
  const header = el('header', {}, el('a', { href: `${FEED}someone/` }, document.createTextNode('someone')), el('a', { href: `${FEED}p/${code}/` }, document.createTextNode('2h')));
  const footer = el('footer', {}, el('a', { href: `${FEED}p/${code}/liked_by/` }), ...extra);
  return { card: el('article', {}, header, media, footer), video };
}

beforeEach(() => {
  document.body.replaceChildren();
});

describe('feedPostLink', () => {
  it('takes the post link of the card around the clicked video, without its fragment', () => {
    const a = card('CODEaaa111');
    const b = card('CODEbbb222');
    document.body.append(el('main', {}, a.card, b.card));
    document.querySelector(`header a[href="${FEED}p/CODEaaa111/"]`)?.setAttribute('href', `${FEED}p/CODEaaa111/#comments`);
    expect(feedPostLink(a.video, FEED)).toBe('https://www.instagram.com/p/CODEaaa111/');
    expect(feedPostLink(b.video, FEED)).toBe('https://www.instagram.com/p/CODEbbb222/');
  });

  it('takes a link that wraps the video itself first', () => {
    const video = document.createElement('video');
    const wrap = el('a', { href: `${FEED}reel/REELccc333/` }, el('div', {}, video));
    document.body.append(el('div', {}, wrap, el('a', { href: `${FEED}p/OTHERddd44/` })));
    expect(feedPostLink(video, FEED)).toBe('https://www.instagram.com/reel/REELccc333/');
  });

  it('takes nothing when the nearest links name several videos (a list of posts)', () => {
    const video = document.createElement('video');
    document.body.append(el('section', {}, el('div', {}, video), el('a', { href: `${FEED}p/AAAAaaa111/` }), el('a', { href: `${FEED}p/BBBBbbb222/` })));
    expect(feedPostLink(video, FEED)).toBeUndefined();
  });

  it('takes nothing on a video address: the page is the video', () => {
    const a = card('CODEaaa111');
    document.body.append(a.card);
    expect(feedPostLink(a.video, 'https://www.instagram.com/reel/PAGEeee555/')).toBeUndefined();
  });

  it('takes nothing in private messages: no post is looked for there (§5.1 "DMs")', () => {
    const video = document.createElement('video');
    document.body.append(el('div', {}, el('a', { href: `${FEED}reel/AbCdEf12345/` }, el('div', {}, video))));
    expect(feedPostLink(video, FEED)).toBe('https://www.instagram.com/reel/AbCdEf12345/');
    expect(feedPostLink(video, 'https://www.instagram.com/direct/t/1234567890/')).toBeUndefined();
    const x = document.createElement('video');
    document.body.append(el('div', {}, el('a', { href: 'https://x.com/someone/status/1234567890' }, el('div', {}, x))));
    expect(feedPostLink(x, 'https://x.com/home')).toBe('https://x.com/someone/status/1234567890');
    expect(feedPostLink(x, 'https://x.com/i/chat/1234567890-9876543210')).toBeUndefined();
    expect(feedPostLink(x, 'https://x.com/messages/123-456')).toBeUndefined();
  });

  it('counts only curated videos yt-dlp reads, on the frame’s own origin', () => {
    const video = document.createElement('video');
    document.body.append(
      el(
        'div',
        {},
        el('div', {}, video),
        el('a', { href: 'https://evil.example/p/CODEaaa111/' }),
        el('a', { href: 'https://instagram.com/p/CODEbbb222/' }),
        el('a', { href: `${FEED}explore/` }),
        el('a', { href: 'javascript:void 0' }),
      ),
    );
    expect(feedPostLink(video, FEED)).toBeUndefined();
    // A network site's post (Threads) is not taken either.
    document.body.replaceChildren();
    const t = document.createElement('video');
    document.body.append(el('div', {}, el('div', {}, t), el('a', { href: 'https://www.threads.com/@someone/post/ABCdef123' })));
    expect(feedPostLink(t, 'https://www.threads.com/')).toBeUndefined();
  });

  it('finds TikTok, X and Facebook posts the same way', () => {
    const cases: [string, string, string][] = [
      ['https://www.tiktok.com/foryou', '/@some-one/video/7312345678901234567', 'https://www.tiktok.com/@some-one/video/7312345678901234567'],
      ['https://x.com/home', '/someone/status/1790000000000000000', 'https://x.com/someone/status/1790000000000000000'],
      ['https://www.facebook.com/', '/reel/1234567890123', 'https://www.facebook.com/reel/1234567890123'],
    ];
    for (const [frame, href, want] of cases) {
      document.body.replaceChildren();
      const video = document.createElement('video');
      document.body.append(el('article', {}, el('div', {}, video), el('a', { href: new URL(href, frame).href })));
      expect(feedPostLink(video, frame)).toBe(want);
    }
  });

  it(`looks at most ${FEED_MAX_ANCESTORS} ancestors up`, () => {
    const video = document.createElement('video');
    let inner: HTMLElement = el('div', {}, video);
    // The video's parent is ancestor 1; the link sits beside ancestor FEED_MAX_ANCESTORS.
    for (let i = 1; i < FEED_MAX_ANCESTORS; i++) inner = el('div', {}, inner);
    document.body.append(el('div', {}, inner, el('a', { href: `${FEED}p/FARaway111/` })));
    expect(feedPostLink(video, FEED)).toBeUndefined();
    document.body.replaceChildren(el('div', {}, inner.firstElementChild!, el('a', { href: `${FEED}p/FARaway111/` })));
    expect(feedPostLink(video, FEED)).toBe('https://www.instagram.com/p/FARaway111/');
  });

  it(`takes nothing from an ancestor with more than ${FEED_MAX_LINKS} links`, () => {
    const video = document.createElement('video');
    const others = Array.from({ length: FEED_MAX_LINKS }, (_, i) => el('a', { href: `${FEED}someone${i}/` }));
    document.body.append(el('div', {}, el('div', {}, video), ...others, el('a', { href: `${FEED}p/LATEaaa111/` })));
    expect(feedPostLink(video, FEED)).toBeUndefined();
    // Not judged from the first links only: a post link among them is not taken either, though
    // another post's link lies past them.
    document.body.replaceChildren();
    const clicked = document.createElement('video');
    const nav = Array.from({ length: FEED_MAX_LINKS - 1 }, (_, i) => el('a', { href: `${FEED}someone${i}/` }));
    document.body.append(el('div', {}, ...nav, el('a', { href: `${FEED}p/ALICEaa111/` }), el('div', {}, clicked), el('a', { href: `${FEED}p/BOBbbbb222/` })));
    expect(feedPostLink(clicked, FEED)).toBeUndefined();
  });

  it('stops at an ancestor that holds another video: its links may be a neighbouring post’s', () => {
    const tiktok = 'https://www.tiktok.com/foryou';
    const bob = document.createElement('video');
    const alice = document.createElement('video');
    document.body.append(
      el(
        'main',
        {},
        el(
          'div',
          {},
          el('article', {}, el('div', {}, bob), el('a', { href: 'https://www.tiktok.com/@bob' })),
          el('article', {}, el('div', {}, alice), el('a', { href: 'https://www.tiktok.com/@alice/video/1111111111' })),
        ),
      ),
    );
    expect(feedPostLink(bob, tiktok)).toBeUndefined();
    expect(feedPostLink(alice, tiktok)).toBe('https://www.tiktok.com/@alice/video/1111111111');
  });

  it('reaches a post link 12 levels above the video (a public Facebook page’s videos)', () => {
    const frame = 'https://www.facebook.com/somepage/videos';
    const video = document.createElement('video');
    let inner: HTMLElement = el('div', {}, video);
    for (let i = 1; i < 12; i++) inner = el('div', {}, inner);
    document.body.append(el('div', {}, inner, el('a', { href: 'https://www.facebook.com/100000000000001/videos/123456789012345' })));
    expect(feedPostLink(video, frame)).toBe('https://www.facebook.com/100000000000001/videos/123456789012345');
  });

  it('goes from a shadow root to its host', () => {
    const video = document.createElement('video');
    const host = el('div');
    host.attachShadow({ mode: 'open' }).append(el('div', {}, video));
    document.body.append(el('article', {}, host, el('a', { href: `${FEED}p/SHADOWa111/` })));
    expect(feedPostLink(video, FEED)).toBe('https://www.instagram.com/p/SHADOWa111/');
  });
});

describe('feedPostLink over carousels (VIDEO-DETECT-SPEC §5.1 "Carousels")', () => {
  const at = (v: Element, left: number, top: number, width = 470, height = 587) => {
    v.getBoundingClientRect = () => ({ left, top, width, height, right: left + width, bottom: top + height, x: left, y: top, toJSON: () => ({}) }) as DOMRect;
  };

  /** A post card whose media is a carousel of `n` video slides in one row. */
  function carousel(code: string, n: number): { card: HTMLElement; slides: HTMLVideoElement[] } {
    const slides = Array.from({ length: n }, () => document.createElement('video'));
    const list = el('ul', {}, ...slides.map((v) => el('li', {}, el('div', {}, v))));
    const header = el('header', {}, el('a', { href: `${FEED}someone/` }), el('a', { href: `${FEED}p/${code}/` }));
    return { card: el('article', {}, header, el('div', { class: 'media' }, list)), slides };
  }

  it('walks past the hidden slides of the clicked video’s own carousel to its post', () => {
    const c = carousel('CAROUSEL01', 3);
    document.body.append(el('main', {}, c.card));
    c.slides.forEach((v, i) => at(v, 300 + 470 * (i - 1), 120));
    const shown = (v: Element) => v === c.slides[1];
    expect(feedPostLink(c.slides[1]!, FEED, { shown })).toBe('https://www.instagram.com/p/CAROUSEL01/');
    // Without knowing which slides are hidden, the walk stops as before.
    expect(feedPostLink(c.slides[1]!, FEED)).toBeUndefined();
  });

  it('still stops at a neighbouring card’s video, and at a shown video beside the clicked one', () => {
    const top = carousel('CAROUSEL01', 2);
    const next = card('CODEbbb222');
    document.body.append(el('main', {}, top.card, next.card));
    at(top.slides[0]!, 300, 120);
    at(top.slides[1]!, 770, 120);
    // The next card's video is lower down (another top): not a slide, though not shown.
    at(next.video, 300, 900);
    const hidden = (v: Element) => v === top.slides[0];
    // The clicked card has no own post link here: the walk must not take the next card's.
    top.card.querySelector('header')!.remove();
    expect(feedPostLink(top.slides[0]!, FEED, { shown: hidden })).toBeUndefined();
    // Two shown videos side by side (a grid, a masonry): not a carousel.
    const row = carousel('ROWGRID001', 2);
    document.body.replaceChildren(el('main', {}, row.card));
    at(row.slides[0]!, 0, 0);
    at(row.slides[1]!, 470, 0);
    expect(feedPostLink(row.slides[0]!, FEED, { shown: () => true })).toBeUndefined();
    // A hidden slide of another size or with no size is no slide either.
    at(row.slides[1]!, 470, 0, 470, 300);
    expect(feedPostLink(row.slides[0]!, FEED, { shown: (v) => v === row.slides[0] })).toBeUndefined();
    at(row.slides[1]!, 470, 0, 0, 0);
    expect(feedPostLink(row.slides[0]!, FEED, { shown: (v) => v === row.slides[0] })).toBeUndefined();
  });
});
