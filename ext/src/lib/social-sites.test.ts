import { describe, expect, it } from 'vitest';
import { compactSite, curatedPage, messagesPage, parseSiteVideo, signInScope, siteOfAddress, tilePost, videoTile } from './site-video';

// Social sites' stories, embeds, compact frames and grid tiles (VIDEO-DETECT-SPEC §3.8, amended
// 2026-11-18). Every address below is made up in the shapes the sites show.

describe('stories and embeds (§3.8, 2026-11-18)', () => {
  type Row = [url: string, key: string, kind: string, pageUrl: string];
  const rows: Row[] = [
    // Instagram stories and highlights: on a click only, with the sign-in when the site asks.
    ['https://www.instagram.com/stories/some.user/3300000000000000000/', 'instagram:story:3300000000000000000', 'post', 'https://www.instagram.com/stories/some.user/3300000000000000000/'],
    ['https://instagram.com/stories/some_user/33?igsh=x', 'instagram:story:33', 'post', 'https://www.instagram.com/stories/some_user/33/'],
    ['https://www.instagram.com/stories/highlights/18090000000000000/', 'instagram:highlight:18090000000000000', 'post', 'https://www.instagram.com/stories/highlights/18090000000000000/'],
    // Embeds: the post's own key and canonical address.
    ['https://www.instagram.com/p/AbCdEf12345/embed/', 'instagram:AbCdEf12345', 'post', 'https://www.instagram.com/p/AbCdEf12345/'],
    ['https://www.instagram.com/p/AbCdEf12345/embed/captioned/?cr=1', 'instagram:AbCdEf12345', 'post', 'https://www.instagram.com/p/AbCdEf12345/'],
    ['https://www.instagram.com/reel/AbCdEf12345/embed', 'instagram:AbCdEf12345', 'embed', 'https://www.instagram.com/reel/AbCdEf12345/'],
    ['https://www.instagram.com/some.user/reel/AbCdEf12345/embed/captioned', 'instagram:AbCdEf12345', 'embed', 'https://www.instagram.com/reel/AbCdEf12345/'],
    ['https://www.instagram.com/tv/AbCdEf12345/embed/', 'instagram:AbCdEf12345', 'embed', 'https://www.instagram.com/tv/AbCdEf12345/'],
    ['https://platform.twitter.com/embed/Tweet.html?dnt=true&id=1790000000000000001&theme=dark', 'x:1790000000000000001', 'post', 'https://x.com/i/status/1790000000000000001'],
    [
      `https://www.facebook.com/plugins/video.php?height=314&href=${encodeURIComponent('https://www.facebook.com/SomePage/videos/1234567890/')}&show_text=false`,
      'facebook:1234567890',
      'embed',
      'https://www.facebook.com/watch/?v=1234567890',
    ],
    [
      `https://www.facebook.com/plugins/video.php?href=${encodeURIComponent('https://www.facebook.com/reel/1234567890')}`,
      'facebook:1234567890',
      'embed',
      'https://www.facebook.com/reel/1234567890',
    ],
    [
      `https://www.facebook.com/plugins/post.php?href=${encodeURIComponent('https://www.facebook.com/SomePage/posts/pfbid0AbCdEfGhIjKlMn')}&width=500`,
      'facebook:post:pfbid0AbCdEfGhIjKlMn',
      'post',
      'https://www.facebook.com/SomePage/posts/pfbid0AbCdEfGhIjKlMn',
    ],
    ['https://embed.reddit.com/r/videos/comments/abc123/a_title/?embed=true', 'reddit:abc123', 'post', 'https://www.reddit.com/r/videos/comments/abc123/'],
    ['https://assets.pinterest.com/ext/embed.html?id=123456789012345678', 'pinterest:123456789012345678', 'post', 'https://www.pinterest.com/pin/123456789012345678/'],
    ['https://embed.bsky.app/embed/did:plc:abcdefgh12345678/app.bsky.feed.post/3kabcdefgh2xy', 'bluesky:did:plc:abcdefgh12345678/3kabcdefgh2xy', 'post', 'https://bsky.app/profile/did:plc:abcdefgh12345678/post/3kabcdefgh2xy'],
  ];

  it.each(rows)('%s is %s', (url, key, kind, pageUrl) => {
    const v = parseSiteVideo(url);
    expect(v).toMatchObject({ key, kind, pageUrl, lookAhead: false, resolver: 'yt-dlp' });
    // The canonical address names the same video, and the sign-in scope holds it.
    expect(parseSiteVideo(v?.pageUrl)?.key).toBe(key);
    expect(signInScope(key, pageUrl)?.exact).toBe(false);
  });

  it.each([
    'https://www.instagram.com/stories/some.user/',
    'https://www.instagram.com/stories/some.user/not-an-id/',
    'https://www.instagram.com/stories/highlights/',
    'https://www.instagram.com/stories/some.user/33/extra/',
    'https://www.instagram.com/p/AbCdEf12345/embed/other/',
    'https://www.instagram.com/embed/',
    'https://platform.twitter.com/embed/Tweet.html',
    'https://platform.twitter.com/embed/Tweet.html?id=12&id=13',
    'https://platform.twitter.com/embed/Tweet.html?id=not-a-number',
    'https://platform.twitter.com/widgets/tweet_button.html?id=12',
    'https://www.facebook.com/plugins/video.php',
    `https://www.facebook.com/plugins/video.php?href=${encodeURIComponent('https://evil.example/SomePage/videos/1234567890/')}`,
    `https://www.facebook.com/plugins/video.php?href=${encodeURIComponent('https://fb.watch/AbCd12/')}`,
    `https://www.facebook.com/plugins/video.php?href=${encodeURIComponent(`https://www.facebook.com/plugins/video.php?href=${encodeURIComponent('https://www.facebook.com/reel/1')}`)}`,
    `https://www.facebook.com/plugins/like.php?href=${encodeURIComponent('https://www.facebook.com/reel/1')}`,
    'https://embed.reddit.com/r/videos/s/AbCdE1',
    'https://embed.reddit.com/r/videos/',
    'https://assets.pinterest.com/ext/embed.html',
    'https://assets.pinterest.com/js/pinit.js?id=12',
    'https://embed.bsky.app/embed/someone.bsky.social/app.bsky.feed.like/3kabcdefgh2xy',
    'https://bsky.app/embed/someone.bsky.social/app.bsky.feed.post/3kabcdefgh2xy',
    // Private messages are never a video or a feed (§5.1 "DMs").
    'https://www.instagram.com/direct/t/123456789/',
    'https://x.com/messages/123-456',
    'https://www.facebook.com/messages/t/123',
  ])('%s is not a video page', (url) => {
    expect(parseSiteVideo(url)).toBeUndefined();
  });

  it('keeps private messages out of the feeds', () => {
    for (const url of ['https://www.instagram.com/direct/inbox/', 'https://www.instagram.com/direct/t/123/', 'https://x.com/messages', 'https://www.facebook.com/messages/t/123']) {
      expect(curatedPage(url), url).toBeUndefined();
    }
  });

  it('names the embed hosts’ sites', () => {
    expect(siteOfAddress('https://platform.twitter.com/embed/Tweet.html?id=1')).toBe('x');
    expect(siteOfAddress('https://embed.reddit.com/r/a/comments/b/')).toBe('reddit');
    expect(siteOfAddress('https://assets.pinterest.com/ext/embed.html?id=1')).toBe('pinterest');
    expect(siteOfAddress('https://embed.bsky.app/embed/a/app.bsky.feed.post/b')).toBe('bluesky');
  });
});

describe('compact sites (§3.8 "Compact sites")', () => {
  it('are the social sites and their embeds, not the video platforms or other pages', () => {
    for (const url of [
      'https://www.tiktok.com/foryou',
      'https://www.instagram.com/',
      'https://www.facebook.com/watch/',
      'https://x.com/home',
      'https://www.reddit.com/r/videos/',
      'https://www.pinterest.com/pin/1/',
      'https://www.threads.com/@a/post/AbCdE1',
      'https://www.snapchat.com/spotlight/AbCdEfGhIjKl',
      'https://www.linkedin.com/feed/',
      'https://bsky.app/',
      'https://some-blog.tumblr.com/post/1',
      'https://9gag.com/',
      'https://imgur.com/',
      'https://vk.com/feed',
      'https://t.me/some_channel/1',
      'https://platform.twitter.com/embed/Tweet.html?id=1',
      'https://www.tiktok.com/embed/v2/7300000000000000001',
    ]) {
      expect(compactSite(url), url).toBe(true);
    }
    for (const url of [
      'https://www.youtube.com/watch?v=aqz-KE-bpKQ',
      'https://vimeo.com/76979871',
      'https://www.dailymotion.com/video/x8abc12',
      'https://www.twitch.tv/videos/1',
      'https://kick.com/a/videos/1',
      'https://www.bilibili.com/video/BV1xx411c7mD/',
      'https://streamable.com/abc123',
      'https://rumble.com/v3abc12-a.html',
      'https://news.example.com/',
      'not an address',
      undefined,
    ]) {
      expect(compactSite(url), String(url)).toBe(false);
    }
  });
});

describe('grid tiles (§3.8 "Tiles")', () => {
  it('are links whose shape always names a video', () => {
    for (const [url, key] of [
      ['https://www.tiktok.com/@some-one/video/7300000000000000001', 'tiktok:7300000000000000001'],
      ['https://www.instagram.com/reel/AbCdEf12345/', 'instagram:AbCdEf12345'],
      ['https://www.instagram.com/some.user/reel/AbCdEf12345/', 'instagram:AbCdEf12345'],
      ['https://www.instagram.com/tv/AbCdEf12345/', 'instagram:AbCdEf12345'],
      ['https://x.com/someone/status/1790000000000000001/video/1', 'x:1790000000000000001'],
      ['https://www.facebook.com/reel/1234567890', 'facebook:1234567890'],
      ['https://www.facebook.com/SomePage/videos/1234567890/', 'facebook:1234567890'],
      ['https://www.facebook.com/watch/?v=1234567890', 'facebook:1234567890'],
      ['https://vk.com/video-12345_67890', 'vk:-12345_67890'],
    ]) {
      expect(videoTile(url)?.key, url).toBe(key);
    }
  });

  it('are not posts that may be photos or text, share links, network sites or other sites', () => {
    for (const url of [
      'https://www.instagram.com/p/AbCdEf12345/',
      'https://www.instagram.com/reel/AbCdEf12345/embed/',
      'https://x.com/someone/status/1790000000000000001',
      'https://x.com/someone/status/1790000000000000001/photo/1',
      'https://www.facebook.com/SomePage/posts/1234567890',
      'https://fb.watch/AbCd12/',
      'https://www.tiktok.com/@someone/photo/7300000000000000001',
      'https://vm.tiktok.com/ZMabc123/',
      'https://www.tiktok.com/embed/v2/7300000000000000001',
      'https://www.pinterest.com/pin/123/',
      'https://www.reddit.com/r/videos/comments/abc123/',
      'https://www.threads.com/@a/post/AbCdE1',
      'https://www.snapchat.com/spotlight/AbCdEfGhIjKl',
      'https://vimeo.com/76979871',
      'https://www.youtube.com/watch?v=aqz-KE-bpKQ',
      'https://www.instagram.com/stories/some.user/33/',
    ]) {
      expect(videoTile(url), url).toBeUndefined();
    }
  });
});

describe('private messages (§5.1 "DMs")', () => {
  it('are known by their first segment on the sites that have them, and nowhere else', () => {
    for (const url of [
      'https://x.com/messages/1-2',
      'https://x.com/i/chat',
      'https://x.com/i/chat/1234567890-9876543210',
      'https://X.com/I/Chat/1-2',
      'https://www.instagram.com/direct/t/1/',
      'https://www.facebook.com/messages/t/1',
      'https://www.linkedin.com/messaging/thread/1/',
      'https://www.reddit.com/chat/room/1',
    ]) {
      expect(messagesPage(url), url).toBe(true);
      expect(curatedPage(url), url).toBeUndefined();
    }
    for (const url of ['https://x.com/someone', 'https://x.com/i/bookmarks', 'https://x.com/chat', 'https://news.example.com/messages/', 'https://news.example.com/i/chat/1', 'https://www.instagram.com/p/AbCdEf12345/', undefined]) {
      expect(messagesPage(url), String(url)).toBe(false);
    }
  });
});

describe('grid tiles’ posts (§5.1 "Grid tiles")', () => {
  it('are a tile link’s address on the frame’s origin, on a page that is no video and no messages', () => {
    expect(tilePost('https://www.tiktok.com/@someone/video/7300000000000000002#x', 'https://www.tiktok.com/@someone')).toBe('https://www.tiktok.com/@someone/video/7300000000000000002');
    expect(tilePost('https://www.instagram.com/reel/AbCdEf12345/', 'https://www.instagram.com/some.user/')).toBe('https://www.instagram.com/reel/AbCdEf12345/');
    // The page is a video: its "You may like" and creator tiles are not the page's video.
    expect(tilePost('https://www.tiktok.com/@someone/video/7300000000000000002', 'https://www.tiktok.com/@someone/video/7300000000000000001')).toBeUndefined();
    expect(tilePost('https://www.instagram.com/reel/AbCdEf12345/', 'https://www.instagram.com/reel/PageReel123/')).toBeUndefined();
    // Private messages.
    expect(tilePost('https://www.instagram.com/reel/AbCdEf12345/', 'https://www.instagram.com/direct/t/1234567890/')).toBeUndefined();
    expect(tilePost('https://x.com/someone/status/1234567890/video/1', 'https://x.com/i/chat/1-2')).toBeUndefined();
    // Another origin, a photo post.
    expect(tilePost('https://www.instagram.com/reel/AbCdEf12345/', 'https://www.tiktok.com/@someone')).toBeUndefined();
    expect(tilePost('https://www.instagram.com/p/AbCdEf12345/', 'https://www.instagram.com/some.user/')).toBeUndefined();
  });
});
