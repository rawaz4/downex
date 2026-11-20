import { describe, expect, it } from 'vitest';
import {
  addressSlide,
  curatedPage,
  genericPageUrl,
  genericVideoKey,
  httpAddress,
  inSignInScope,
  parseSiteVideo,
  sameOrigin,
  SIGN_IN_DOMAINS,
  signInScope,
  siteOfAddress,
  siteOfHost,
  siteOfKey,
  SITE_NAMES,
  ytDlpReads,
} from './site-video';

const ID = 'aqz-KE-bpKQ';
const WATCH = `https://www.youtube.com/watch?v=${ID}`;

describe('parseSiteVideo', () => {
  it.each([
    [`https://www.youtube.com/watch?v=${ID}`, 'watch'],
    [`https://www.youtube.com/watch?v=${ID}&list=PL123&index=4&t=42s&si=abc&pp=xyz`, 'watch'],
    [`https://m.youtube.com/watch?v=${ID}&feature=share`, 'watch'],
    [`https://music.youtube.com/watch?v=${ID}&list=RDAMVM`, 'watch'],
    [`https://youtube.com/watch?v=${ID}`, 'watch'],
    [`http://www.youtube.com/watch?v=${ID}`, 'watch'],
    [`https://www.youtube.com/watch?feature=share&v=${ID}#t=10`, 'watch'],
    [`https://www.youtube.com/shorts/${ID}`, 'shorts'],
    [`https://m.youtube.com/shorts/${ID}?si=x`, 'shorts'],
    [`https://www.youtube.com/embed/${ID}?autoplay=1`, 'embed'],
    [`https://www.youtube.com/live/${ID}?si=x`, 'live'],
    [`https://www.youtube.com/v/${ID}`, 'embed'],
    [`https://www.youtube-nocookie.com/embed/${ID}?rel=0`, 'embed'],
    [`https://youtube-nocookie.com/embed/${ID}`, 'embed'],
    [`https://youtu.be/${ID}?t=5&si=share`, 'watch'],
    [`https://WWW.YouTube.com/watch?v=${ID}`, 'watch'],
  ])('%s is a %s video with the canonical address', (url, kind) => {
    const lookAhead = kind === 'watch' || kind === 'shorts';
    expect(parseSiteVideo(url)).toEqual({ site: 'youtube', id: ID, kind, key: `youtube:${ID}`, pageUrl: WATCH, lookAhead, resolver: 'yt-dlp' });
  });

  it('keeps a clip at its own address, without the fragment', () => {
    const clip = 'UgkxW7Hy2v8bKXl5pz7bK3s5mE0aT1T9hF2d';
    expect(parseSiteVideo(`https://www.youtube.com/clip/${clip}?si=x#frag`)).toEqual({
      site: 'youtube',
      id: clip,
      kind: 'clip',
      key: `youtube:${clip}`,
      pageUrl: `https://www.youtube.com/clip/${clip}`,
      lookAhead: false,
      resolver: 'yt-dlp',
    });
  });

  it.each([
    undefined,
    '',
    'not a url',
    'https://www.youtube.com/',
    'https://www.youtube.com/watch',
    `https://www.youtube.com/watch?v=${ID}x`,
    'https://www.youtube.com/watch?v=short',
    `https://www.youtube.com/watch?v=${ID}&v=${ID}`,
    'https://www.youtube.com/watch?v=dQw4w9WgXc!',
    `https://www.youtube.com/channel/UC${ID}`,
    'https://www.youtube.com/playlist?list=PL0123456789',
    `https://www.youtube.com/live_chat?v=${ID}`,
    `https://www.youtube.com/embed/${ID}/extra`,
    `https://www.youtube.com/shorts/`,
    `https://www.youtube.com/clip/short`,
    `https://youtu.be/${ID}/x`,
    `https://youtu.be/`,
    `https://www.youtube-nocookie.com/watch?v=${ID}`,
    `https://evil.example/watch?v=${ID}`,
    `https://www.youtube.com.evil.example/watch?v=${ID}`,
    `https://user:pw@www.youtube.com/watch?v=${ID}`,
    `ftp://www.youtube.com/watch?v=${ID}`,
    `javascript:alert('https://www.youtube.com/watch?v=${ID}')`,
  ])('%s is not a video page', (url) => {
    expect(parseSiteVideo(url)).toBeUndefined();
  });
});

describe('curated video sites (VIDEO-DETECT-SPEC §3.8)', () => {
  type Row = [url: string, key: string, kind: string, pageUrl: string, lookAhead: boolean];
  const rows: Row[] = [
    // Vimeo
    ['https://vimeo.com/76979871', 'vimeo:76979871', 'watch', 'https://player.vimeo.com/video/76979871', true],
    ['https://www.vimeo.com/76979871/', 'vimeo:76979871', 'watch', 'https://player.vimeo.com/video/76979871', true],
    ['https://vimeo.com/76979871/8272103f6e?share=copy', 'vimeo:76979871', 'watch', 'https://player.vimeo.com/video/76979871?h=8272103f6e', true],
    ['https://vimeo.com/channels/staffpicks/76979871', 'vimeo:76979871', 'watch', 'https://player.vimeo.com/video/76979871', true],
    ['https://vimeo.com/groups/shortfilms/videos/76979871', 'vimeo:76979871', 'watch', 'https://player.vimeo.com/video/76979871', true],
    ['https://vimeo.com/showcase/9876543/video/76979871', 'vimeo:76979871', 'watch', 'https://player.vimeo.com/video/76979871', true],
    ['https://player.vimeo.com/video/76979871', 'vimeo:76979871', 'embed', 'https://player.vimeo.com/video/76979871', false],
    [
      'https://player.vimeo.com/video/76979871?h=8272103f6e&badge=0&autopause=0',
      'vimeo:76979871',
      'embed',
      'https://player.vimeo.com/video/76979871?h=8272103f6e',
      false,
    ],
    ['https://player.vimeo.com/video/76979871?h=NOT-A-HASH', 'vimeo:76979871', 'embed', 'https://player.vimeo.com/video/76979871', false],
    // X
    ['https://x.com/someone/status/1790000000000000001', 'x:1790000000000000001', 'watch', 'https://x.com/i/status/1790000000000000001', true],
    ['https://twitter.com/someone/status/1790000000000000001?s=20', 'x:1790000000000000001', 'watch', 'https://x.com/i/status/1790000000000000001', true],
    ['https://mobile.twitter.com/someone/status/17/video/1', 'x:17', 'watch', 'https://x.com/i/status/17', true],
    ['https://www.x.com/i/status/42', 'x:42', 'watch', 'https://x.com/i/status/42', true],
    ['https://mobile.x.com/i/web/status/42', 'x:42', 'watch', 'https://x.com/i/status/42', true],
    ['https://www.twitter.com/i/status/42/', 'x:42', 'watch', 'https://x.com/i/status/42', true],
    ['https://x.com/i/broadcasts/1AbCdEfGhIjKl', 'x:broadcast:1AbCdEfGhIjKl', 'watch', 'https://x.com/i/broadcasts/1AbCdEfGhIjKl', false],
    // Facebook
    ['https://www.facebook.com/watch?v=1234567890', 'facebook:1234567890', 'watch', 'https://www.facebook.com/watch/?v=1234567890', false],
    ['https://facebook.com/watch/?v=1234567890&ref=sharing', 'facebook:1234567890', 'watch', 'https://www.facebook.com/watch/?v=1234567890', false],
    ['https://m.facebook.com/videos/1234567890/', 'facebook:1234567890', 'watch', 'https://www.facebook.com/watch/?v=1234567890', false],
    ['https://web.facebook.com/SomePage/videos/1234567890', 'facebook:1234567890', 'watch', 'https://www.facebook.com/watch/?v=1234567890', false],
    ['https://www.facebook.com/Some.Page/videos/a-title-slug/1234567890/', 'facebook:1234567890', 'watch', 'https://www.facebook.com/watch/?v=1234567890', false],
    ['https://www.facebook.com/reel/987654321', 'facebook:987654321', 'watch', 'https://www.facebook.com/reel/987654321', false],
    ['https://www.facebook.com/watch/live/?v=1234567890&ref=x', 'facebook:1234567890', 'watch', 'https://www.facebook.com/watch/?v=1234567890', false],
    ['https://m.facebook.com/video.php?v=1234567890', 'facebook:1234567890', 'watch', 'https://www.facebook.com/watch/?v=1234567890', false],
    ['https://www.facebook.com/SomePage/posts/1234567890', 'facebook:post:1234567890', 'post', 'https://www.facebook.com/SomePage/posts/1234567890', false],
    [
      'https://www.facebook.com/Some.Page/posts/pfbid0AbCdEfGhIjKlMn?ref=share',
      'facebook:post:pfbid0AbCdEfGhIjKlMn',
      'post',
      'https://www.facebook.com/Some.Page/posts/pfbid0AbCdEfGhIjKlMn',
      false,
    ],
    ['https://www.facebook.com/groups/12345/permalink/678/', 'facebook:post:678', 'post', 'https://www.facebook.com/groups/12345/posts/678', false],
    ['https://web.facebook.com/groups/some.group/posts/678', 'facebook:post:678', 'post', 'https://www.facebook.com/groups/some.group/posts/678', false],
    [
      'https://www.facebook.com/story.php?story_fbid=pfbid0AbCdEfGhIjKlMn&id=100000000000001&mibextid=x',
      'facebook:post:pfbid0AbCdEfGhIjKlMn',
      'post',
      'https://www.facebook.com/permalink.php?story_fbid=pfbid0AbCdEfGhIjKlMn&id=100000000000001',
      false,
    ],
    ['https://fb.watch/aBc_-12/', 'facebook:aBc_-12', 'watch', 'https://fb.watch/aBc_-12/', false],
    // Instagram
    ['https://www.instagram.com/p/C1a2B3c4D5e/', 'instagram:C1a2B3c4D5e', 'watch', 'https://www.instagram.com/p/C1a2B3c4D5e/', false],
    ['https://instagram.com/some.user/p/C1a2B3c4D5e?igsh=x', 'instagram:C1a2B3c4D5e', 'watch', 'https://www.instagram.com/p/C1a2B3c4D5e/', false],
    ['https://www.instagram.com/reel/C1a2B3c4D5e/', 'instagram:C1a2B3c4D5e', 'watch', 'https://www.instagram.com/reel/C1a2B3c4D5e/', false],
    ['https://www.instagram.com/reels/C1a2B3c4D5e/', 'instagram:C1a2B3c4D5e', 'watch', 'https://www.instagram.com/reel/C1a2B3c4D5e/', false],
    ['https://www.instagram.com/tv/C1a2B3c4D5e', 'instagram:C1a2B3c4D5e', 'watch', 'https://www.instagram.com/tv/C1a2B3c4D5e/', false],
    ['https://www.instagram.com/some.user/reel/C1a2B3c4D5e/', 'instagram:C1a2B3c4D5e', 'watch', 'https://www.instagram.com/reel/C1a2B3c4D5e/', false],
    ['https://www.instagram.com/some_user/tv/C1a2B3c4D5e/', 'instagram:C1a2B3c4D5e', 'watch', 'https://www.instagram.com/tv/C1a2B3c4D5e/', false],
    // TikTok
    ['https://www.tiktok.com/@some.user_1/video/7300000000000000001?lang=en', 'tiktok:7300000000000000001', 'watch', 'https://www.tiktok.com/@some.user_1/video/7300000000000000001', true],
    ['https://m.tiktok.com/@u/video/7300000000000000001/', 'tiktok:7300000000000000001', 'watch', 'https://www.tiktok.com/@u/video/7300000000000000001', true],
    ['https://www.tiktok.com/embed/v2/7300000000000000001', 'tiktok:7300000000000000001', 'embed', 'https://www.tiktok.com/embed/v2/7300000000000000001', false],
    ['https://tiktok.com/embed/7300000000000000001', 'tiktok:7300000000000000001', 'embed', 'https://www.tiktok.com/embed/v2/7300000000000000001', false],
    ['https://www.tiktok.com/@some-user/video/7300000000000000001', 'tiktok:7300000000000000001', 'watch', 'https://www.tiktok.com/@some-user/video/7300000000000000001', true],
    ['https://vm.tiktok.com/ZMabc123/', 'tiktok:link:ZMabc123', 'link', 'https://vm.tiktok.com/ZMabc123/', false],
    ['https://vm.tiktok.com/AbCdE/', 'tiktok:link:AbCdE', 'link', 'https://vm.tiktok.com/AbCdE/', false],
    ['https://vt.tiktok.com/ZSabc123', 'tiktok:link:ZSabc123', 'link', 'https://vt.tiktok.com/ZSabc123/', false],
    ['https://www.tiktok.com/t/ZTabc123/?k=1', 'tiktok:link:ZTabc123', 'link', 'https://www.tiktok.com/t/ZTabc123/', false],
    // Dailymotion
    ['https://www.dailymotion.com/video/x8abc12', 'dailymotion:x8abc12', 'watch', 'https://www.dailymotion.com/video/x8abc12', true],
    ['https://dailymotion.com/video/x8abc12?playlist=x1', 'dailymotion:x8abc12', 'watch', 'https://www.dailymotion.com/video/x8abc12', true],
    ['https://www.dailymotion.com/embed/video/x8abc12?autoplay=1', 'dailymotion:x8abc12', 'embed', 'https://www.dailymotion.com/video/x8abc12', false],
    ['https://dai.ly/x8abc12', 'dailymotion:x8abc12', 'watch', 'https://www.dailymotion.com/video/x8abc12', true],
    ['https://geo.dailymotion.com/player.html?video=x8abc12', 'dailymotion:x8abc12', 'embed', 'https://www.dailymotion.com/video/x8abc12', false],
    ['https://geo.dailymotion.com/player/xabc1.html?video=x8abc12&mute=1', 'dailymotion:x8abc12', 'embed', 'https://www.dailymotion.com/video/x8abc12', false],
    // Reddit
    ['https://www.reddit.com/r/videos/comments/1abc2de/some_title/', 'reddit:1abc2de', 'watch', 'https://www.reddit.com/r/videos/comments/1abc2de/', false],
    ['https://old.reddit.com/r/videos/comments/1abc2de', 'reddit:1abc2de', 'watch', 'https://www.reddit.com/r/videos/comments/1abc2de/', false],
    ['https://new.reddit.com/r/Some_Sub/comments/1abc2de/title/comment9/?context=3', 'reddit:1abc2de', 'watch', 'https://www.reddit.com/r/Some_Sub/comments/1abc2de/', false],
    ['https://reddit.com/r/videos/comments/1abc2de/', 'reddit:1abc2de', 'watch', 'https://www.reddit.com/r/videos/comments/1abc2de/', false],
    ['https://www.reddit.com/r/videos/s/AbC12dEf', 'reddit:link:AbC12dEf', 'link', 'https://www.reddit.com/r/videos/s/AbC12dEf', false],
    ['https://v.redd.it/abc123xyz', 'reddit:v:abc123xyz', 'watch', 'https://v.redd.it/abc123xyz', false],
    // Twitch
    ['https://www.twitch.tv/videos/2000000001', 'twitch:2000000001', 'watch', 'https://www.twitch.tv/videos/2000000001', true],
    ['https://m.twitch.tv/videos/2000000001?t=1h2m', 'twitch:2000000001', 'watch', 'https://www.twitch.tv/videos/2000000001', true],
    ['https://www.twitch.tv/some_channel/clip/FunnyClipSlug-AbC_12', 'twitch:clip:FunnyClipSlug-AbC_12', 'clip', 'https://clips.twitch.tv/FunnyClipSlug-AbC_12', true],
    ['https://clips.twitch.tv/FunnyClipSlug-AbC_12', 'twitch:clip:FunnyClipSlug-AbC_12', 'clip', 'https://clips.twitch.tv/FunnyClipSlug-AbC_12', true],
    ['https://m.twitch.tv/clip/FunnyClipSlug-AbC_12', 'twitch:clip:FunnyClipSlug-AbC_12', 'clip', 'https://clips.twitch.tv/FunnyClipSlug-AbC_12', true],
    ['https://player.twitch.tv/?video=v2000000001&parent=blog.example', 'twitch:2000000001', 'embed', 'https://www.twitch.tv/videos/2000000001', false],
    // Streamable
    ['https://streamable.com/abc123', 'streamable:abc123', 'watch', 'https://streamable.com/abc123', true],
    ['https://www.streamable.com/abc123/', 'streamable:abc123', 'watch', 'https://streamable.com/abc123', true],
    ['https://streamable.com/e/abc123?autoplay=1', 'streamable:abc123', 'embed', 'https://streamable.com/abc123', false],
    // Sites added on 2026-11-18 (never looked ahead).
    ['https://bsky.app/profile/news.example/post/3abcdefghij2k', 'bluesky:news.example/3abcdefghij2k', 'post', 'https://bsky.app/profile/news.example/post/3abcdefghij2k', false],
    [
      'https://bsky.app/profile/did:plc:abc123xyz/post/3abcdefghij2k?x=1',
      'bluesky:did:plc:abc123xyz/3abcdefghij2k',
      'post',
      'https://bsky.app/profile/did:plc:abc123xyz/post/3abcdefghij2k',
      false,
    ],
    ['https://www.pinterest.com/pin/123456789012345678/', 'pinterest:123456789012345678', 'post', 'https://www.pinterest.com/pin/123456789012345678/', false],
    ['https://pinterest.com/pin/123456789012345678', 'pinterest:123456789012345678', 'post', 'https://www.pinterest.com/pin/123456789012345678/', false],
    ['https://pin.it/1AbCdEf', 'pinterest:link:1AbCdEf', 'link', 'https://pin.it/1AbCdEf', false],
    ['https://pin.it/AbCd', 'pinterest:link:AbCd', 'link', 'https://pin.it/AbCd', false],
    [
      'https://www.linkedin.com/posts/some-person_topic-activity-7000000000000000000-AbCd?utm_source=share',
      'linkedin:some-person_topic-activity-7000000000000000000-AbCd',
      'post',
      'https://www.linkedin.com/posts/some-person_topic-activity-7000000000000000000-AbCd',
      false,
    ],
    [
      'https://www.linkedin.com/feed/update/urn:li:activity:7000000000000000000/',
      'linkedin:activity:7000000000000000000',
      'post',
      'https://www.linkedin.com/feed/update/urn:li:activity:7000000000000000000/',
      false,
    ],
    [
      'https://kick.com/some_channel/videos/0a1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d',
      'kick:0a1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d',
      'watch',
      'https://kick.com/some_channel/videos/0a1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d',
      false,
    ],
    ['https://kick.com/some_channel?clip=clip_01ABCDEFGHJK', 'kick:clip:clip_01ABCDEFGHJK', 'clip', 'https://kick.com/some_channel?clip=clip_01ABCDEFGHJK', false],
    ['https://www.kick.com/some_channel/clips/clip_01ABCDEFGHJK', 'kick:clip:clip_01ABCDEFGHJK', 'clip', 'https://kick.com/some_channel?clip=clip_01ABCDEFGHJK', false],
    ['https://www.bilibili.com/video/BV1ab411c7de/?spm_id_from=x', 'bilibili:BV1ab411c7de', 'watch', 'https://www.bilibili.com/video/BV1ab411c7de/', false],
    ['https://m.bilibili.com/video/BV1ab411c7de?p=1', 'bilibili:BV1ab411c7de', 'watch', 'https://www.bilibili.com/video/BV1ab411c7de/', false],
    ['https://www.bilibili.com/video/BV1ab411c7de?p=3', 'bilibili:BV1ab411c7de:p3', 'watch', 'https://www.bilibili.com/video/BV1ab411c7de/?p=3', false],
    ['https://bilibili.com/video/av170001', 'bilibili:av170001', 'watch', 'https://www.bilibili.com/video/av170001/', false],
    ['https://b23.tv/AbC12d', 'bilibili:link:AbC12d', 'link', 'https://b23.tv/AbC12d', false],
    ['https://b23.tv/AbCd', 'bilibili:link:AbCd', 'link', 'https://b23.tv/AbCd', false],
    ['https://vk.com/video-12345_67890', 'vk:-12345_67890', 'watch', 'https://vk.com/video-12345_67890', false],
    ['https://vkvideo.ru/video12345_67890?list=x', 'vk:12345_67890', 'watch', 'https://vk.com/video12345_67890', false],
    ['https://odysee.com/@Some.Channel:a/a-video-name:9', 'odysee:@Some.Channel:a/a-video-name:9', 'watch', 'https://odysee.com/@Some.Channel:a/a-video-name:9', false],
    ['https://www.bitchute.com/video/AbCdEf12_-/', 'bitchute:AbCdEf12_-', 'watch', 'https://www.bitchute.com/video/AbCdEf12_-/', false],
    ['https://bitchute.com/embed/AbCdEf12_-', 'bitchute:AbCdEf12_-', 'embed', 'https://www.bitchute.com/video/AbCdEf12_-/', false],
    ['https://9gag.com/gag/aB12cDe', 'ninegag:aB12cDe', 'post', 'https://9gag.com/gag/aB12cDe', false],
    ['https://imgur.com/A61bCd1', 'imgur:A61bCd1', 'post', 'https://imgur.com/A61bCd1', false],
    ['https://imgur.com/gallery/a-funny-title-AbC12dE', 'imgur:gallery:a-funny-title-AbC12dE', 'post', 'https://imgur.com/gallery/a-funny-title-AbC12dE', false],
    ['https://t.me/some_channel/613?single', 'telegram:some_channel/613', 'post', 'https://t.me/some_channel/613', false],
    ['https://some-blog.tumblr.com/post/123456789/a-slug', 'tumblr:some-blog/123456789', 'post', 'https://some-blog.tumblr.com/post/123456789', false],
    ['https://www.tumblr.com/some-blog/123456789', 'tumblr:some-blog/123456789', 'post', 'https://some-blog.tumblr.com/post/123456789', false],
    ['https://rumble.com/v3abc12-a-video-title.html?mref=x', 'rumble:v3abc12', 'watch', 'https://rumble.com/v3abc12-a-video-title.html', false],
    ['https://rumble.com/embed/v3abc12/?pub=4', 'rumble:v3abc12', 'embed', 'https://rumble.com/embed/v3abc12/', false],
    ['https://www.threads.net/@some.user/post/AbC12dEfGh', 'threads:AbC12dEfGh', 'post', 'https://www.threads.com/@some.user/post/AbC12dEfGh', false],
    ['https://www.snapchat.com/spotlight/AbCdEfGhIjKlMnOpQrStUv', 'snapchat:AbCdEfGhIjKlMnOpQrStUv', 'watch', 'https://www.snapchat.com/spotlight/AbCdEfGhIjKlMnOpQrStUv', false],
  ];

  it.each(rows)('%s is %s', (url, key, kind, pageUrl, lookAhead) => {
    const v = parseSiteVideo(url);
    expect(v).toMatchObject({ key, kind, pageUrl, lookAhead });
    expect(v?.key.startsWith(`${v.site}:`)).toBe(true);
    // The canonical address names the same video.
    expect(parseSiteVideo(v?.pageUrl)?.key).toBe(key);
  });

  it('never looks ahead on embeds, posts, share links, Facebook, Instagram, Reddit or the sites added later', () => {
    const lookAheadSites = new Set(['vimeo', 'x', 'tiktok', 'dailymotion', 'twitch', 'streamable']);
    for (const [url] of rows) {
      const v = parseSiteVideo(url);
      if (!v) continue;
      if (v.kind === 'embed' || v.kind === 'post' || v.kind === 'link' || !lookAheadSites.has(v.site)) expect(v.lookAhead, url).toBe(false);
    }
  });

  it('sends Vimeo pages as the player’s address, which yt-dlp reads without a sign-in', () => {
    expect(parseSiteVideo('https://vimeo.com/76979871')).toMatchObject({ kind: 'watch', pageUrl: 'https://player.vimeo.com/video/76979871' });
    expect(parseSiteVideo('https://vimeo.com/76979871/8272103f6e')?.pageUrl).toBe('https://player.vimeo.com/video/76979871?h=8272103f6e');
  });

  it('reads Threads, Snapchat, Rumble and Kick VODs through the network only, every other site with yt-dlp', () => {
    const network = (v: { site: string; kind: string }) =>
      v.site === 'threads' || v.site === 'snapchat' || v.site === 'rumble' || (v.site === 'kick' && v.kind === 'watch');
    for (const [url] of rows) {
      const v = parseSiteVideo(url)!;
      expect(v.resolver, url).toBe(network(v) ? 'network' : 'yt-dlp');
      expect(ytDlpReads(url)).toBe(v.resolver === 'yt-dlp');
    }
    expect(ytDlpReads('https://blog.example/video')).toBe(true);
    expect(ytDlpReads(undefined)).toBe(true);
  });

  it.each([
    // Wrong hosts.
    'https://vimeo.co/76979871',
    'https://vimeo.com.evil.example/76979871',
    'https://notx.com/someone/status/42',
    'https://x.co/someone/status/42',
    'https://facebook.example/watch?v=1',
    'https://l.facebook.com/videos/1',
    'https://instagr.am/p/C1a2B3c4D5e/',
    'https://vx.tiktok.com/ZMabc123/',
    'https://dailymotion.example/video/x8abc12',
    'https://reddit.example/r/videos/comments/1abc2de/',
    'https://i.redd.it/abc123xyz.jpg',
    'https://bsky.example/profile/a.example/post/3abcdefghij2k',
    'https://pinterest.co.uk/pin/123',
    'https://kick.example/c/videos/0a1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d',
    'https://a.b.tumblr.com/post/123',
    'https://threads.example/@u/post/AbC12dEfGh',
    'https://twitch.example/videos/2000000001',
    'https://streamable.example/abc123',
    // Bad ids.
    'https://vimeo.com/abc',
    'https://vimeo.com/1234567890123',
    'https://vimeo.com/76979871/NOTHEX',
    'https://player.vimeo.com/video/abc',
    'https://x.com/someone/status/abc',
    'https://x.com/someone/status/123456789012345678901',
    'https://www.facebook.com/watch?v=abc',
    'https://www.facebook.com/watch?v=1&v=2',
    'https://www.facebook.com/reel/abc',
    'https://fb.watch/ab/',
    'https://www.instagram.com/p/abc/',
    'https://www.tiktok.com/@user/video/abc',
    'https://www.tiktok.com/user/video/7300000000000000001',
    'https://www.dailymotion.com/video/x8',
    'https://www.dailymotion.com/video/x8abc12_some-title',
    'https://www.reddit.com/r/v/comments/1abc2de/',
    'https://www.reddit.com/r/videos/comments/ABC/',
    'https://www.twitch.tv/videos/abc',
    'https://clips.twitch.tv/abc',
    'https://player.twitch.tv/?video=2000000001',
    'https://player.twitch.tv/?video=v1&video=v2',
    'https://streamable.com/ab',
    'https://streamable.com/ABC123',
    'https://x.com/i/broadcasts/short',
    'https://www.facebook.com/SomePage/posts/abc',
    'https://www.facebook.com/permalink.php?story_fbid=1',
    'https://www.facebook.com/permalink.php?story_fbid=1&id=2&id=3',
    'https://www.facebook.com/story.php?story_fbid=1&id=abc',
    'https://www.facebook.com/video.php?v=abc',
    'https://www.tiktok.com/@user/video/abc',
    'https://www.tiktok.com/t/ab/',
    'https://vm.tiktok.com/ZM_abc/',
    // TikTok and Reddit share codes have 5 to 16 characters (pin.it and b23.tv 4 to 16).
    'https://vm.tiktok.com/AbCd/',
    'https://www.tiktok.com/t/AbCd/',
    'https://www.reddit.com/r/videos/s/AbCd',
    'https://pin.it/AbC',
    'https://b23.tv/AbC',
    'https://geo.dailymotion.com/player.html',
    'https://geo.dailymotion.com/player.html?video=x8',
    'https://www.reddit.com/r/videos/s/a',
    'https://v.redd.it/ABC',
    'https://www.twitch.tv/clip/FunnyClipSlug',
    'https://bsky.app/profile/a.example/post/SHORT',
    'https://www.pinterest.com/pin/abc/',
    'https://www.linkedin.com/feed/update/urn:li:share:7000/',
    'https://kick.com/some_channel/videos/not-a-uuid',
    'https://kick.com/some_channel?clip=x',
    'https://www.bilibili.com/video/BV1short',
    'https://www.bilibili.com/video/BV1ab411c7de/extra',
    'https://vk.com/videos12345',
    'https://vk.com/video12345',
    'https://odysee.com/@channel',
    'https://odysee.com/$/settings/x',
    'https://www.bitchute.com/video/ab/',
    'https://9gag.com/gag/ab',
    'https://imgur.com/upload',
    'https://imgur.com/signin',
    'https://t.me/s/some_channel',
    'https://t.me/some_channel/abc',
    'https://t.me/joinchat/AbCdEf',
    'https://www.tumblr.com/some-blog',
    'https://www.tumblr.com/post/123',
    'https://www.tumblr.com/tagged/123',
    'https://rumble.com/videos',
    'https://rumble.com/vids',
    'https://rumble.com/c/SomeChannel',
    'https://www.threads.com/@user',
    'https://www.threads.com/@user/post/ab',
    'https://www.snapchat.com/spotlight/short',
    'https://www.snapchat.com/add/someone',
    // Extra segments where none are allowed.
    'https://vimeo.com/76979871/8272103f6e/extra',
    'https://player.vimeo.com/video/76979871/extra',
    'https://x.com/someone/status/42/likes',
    'https://x.com/someone/status/42/photo/1',
    'https://www.facebook.com/videos/1234567890/extra/more',
    'https://www.instagram.com/p/C1a2B3c4D5e/extra',
    'https://www.tiktok.com/@u/video/7300000000000000001/extra',
    'https://www.dailymotion.com/video/x8abc12/extra',
    'https://www.twitch.tv/videos/2000000001/extra',
    'https://streamable.com/e/abc123/extra',
    // Channel, feed, timeline, profile and search pages.
    'https://vimeo.com/',
    'https://vimeo.com/channels/staffpicks',
    'https://x.com/',
    'https://x.com/home',
    'https://x.com/someone',
    'https://x.com/search?q=video',
    'https://twitter.com/i/status',
    'https://www.facebook.com/',
    'https://www.facebook.com/watch/',
    'https://www.facebook.com/SomePage',
    'https://www.facebook.com/SomePage/videos',
    'https://www.instagram.com/',
    'https://www.instagram.com/some.user/',
    'https://www.instagram.com/reels/',
    'https://www.instagram.com/explore/',
    'https://www.tiktok.com/@someone/photo/7300000000000000001',
    'https://www.twitch.tv/some_channel?clip=FunnyClipSlug',
    'https://www.facebook.com/groups/12345',
    'https://bsky.app/',
    'https://bsky.app/profile/a.example',
    'https://www.pinterest.com/someone/',
    'https://www.linkedin.com/feed/',
    'https://kick.com/some_channel',
    'https://www.bilibili.com/',
    'https://vk.com/feed',
    'https://9gag.com/',
    'https://imgur.com/',
    'https://t.me/some_channel',
    'https://some-blog.tumblr.com/',
    'https://rumble.com/',
    'https://www.threads.com/',
    'https://www.snapchat.com/',
    'https://www.tiktok.com/',
    'https://www.tiktok.com/foryou',
    'https://www.tiktok.com/@someone',
    'https://www.dailymotion.com/',
    'https://www.reddit.com/',
    'https://www.reddit.com/r/videos/',
    'https://www.reddit.com/user/someone/',
    'https://www.twitch.tv/',
    'https://www.twitch.tv/some_channel',
    'https://www.twitch.tv/some_channel/videos',
    'https://player.twitch.tv/?channel=some_channel',
    'https://streamable.com/',
    'https://streamable.com/login',
    'https://streamable.com/upload',
    // User information and other schemes.
    'https://user:pw@vimeo.com/76979871',
    'https://user@www.reddit.com/r/videos/comments/1abc2de/',
    'ftp://vimeo.com/76979871',
    'javascript:alert("https://vimeo.com/76979871")',
    'data:text/html,https://streamable.com/abc123',
    'blob:https://vimeo.com/76979871',
  ])('%s is not a video page', (url) => {
    expect(parseSiteVideo(url)).toBeUndefined();
  });

  it('names every site and reads the site of a key', () => {
    expect(Object.values(SITE_NAMES)).toEqual([
      'YouTube',
      'Vimeo',
      'X',
      'Facebook',
      'Instagram',
      'TikTok',
      'Dailymotion',
      'Reddit',
      'Twitch',
      'Streamable',
      'Bluesky',
      'Pinterest',
      'LinkedIn',
      'Kick',
      'Bilibili',
      'VK',
      'Odysee',
      'BitChute',
      '9GAG',
      'Imgur',
      'Telegram',
      'Tumblr',
      'Rumble',
      'Threads',
      'Snapchat',
      'SoundCloud',
      'Bandcamp',
      'Mixcloud',
    ]);
    expect(siteOfKey('soundcloud:a/b')).toBe('soundcloud');
    expect(siteOfKey('ninegag:aB12cDe')).toBe('ninegag');
    expect(siteOfKey('tiktok:link:ZMabc123')).toBe('tiktok');
    expect(siteOfKey('vimeo:1')).toBe('vimeo');
    expect(siteOfKey('twitch:clip:abcd')).toBe('twitch');
    expect(siteOfKey('page:https://a.example/')).toBeUndefined();
    expect(siteOfKey('constructor:1')).toBeUndefined();
    expect(siteOfKey('nothing')).toBeUndefined();
  });
});

/** A small deterministic generator for property tests (mulberry32). */
function random(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

describe('parseSiteVideo over random addresses', () => {
  const hosts = [
    'www.youtube.com',
    'youtu.be',
    'vimeo.com',
    'player.vimeo.com',
    'x.com',
    'twitter.com',
    'www.facebook.com',
    'fb.watch',
    'www.instagram.com',
    'www.tiktok.com',
    'www.dailymotion.com',
    'dai.ly',
    'www.reddit.com',
    'www.twitch.tv',
    'clips.twitch.tv',
    'player.twitch.tv',
    'streamable.com',
    'vm.tiktok.com',
    'v.redd.it',
    'geo.dailymotion.com',
    'bsky.app',
    'www.pinterest.com',
    'pin.it',
    'www.linkedin.com',
    'kick.com',
    'www.bilibili.com',
    'b23.tv',
    'vk.com',
    'odysee.com',
    'www.bitchute.com',
    '9gag.com',
    'imgur.com',
    't.me',
    'blog.tumblr.com',
    'www.tumblr.com',
    'rumble.com',
    'www.threads.com',
    'www.snapchat.com',
    'evil.example',
  ];
  const words = [
    'watch',
    'status',
    'i',
    'web',
    'video',
    'videos',
    'p',
    'reel',
    'reels',
    'tv',
    'embed',
    'v2',
    'r',
    'comments',
    'clip',
    'clips',
    'e',
    'shorts',
    'live',
    '@u',
    '..',
    '%2F',
    '',
    'posts',
    'post',
    'profile',
    'pin',
    'gag',
    'gallery',
    'spotlight',
    'broadcasts',
    's',
    't',
    'groups',
    'permalink.php',
    'video.php',
    'player.html',
    'BV1ab411c7de',
    'video-1_2',
    'v3abc12',
    'urn:li:activity:1',
  ];
  const chars = 'abcdefABCDEF0123456789_-.@%~:/?#&=';

  it('never throws, and every answer is canonical and keyed by its site', () => {
    const next = random(0x5eed);
    const pick = <T>(list: readonly T[]): T => list[Math.floor(next() * list.length)] as T;
    const token = () => {
      if (next() < 0.5) return pick(words);
      let s = '';
      const n = Math.floor(next() * 24);
      for (let i = 0; i < n; i++) s += pick(chars.split(''));
      return s;
    };
    let found = 0;
    for (let i = 0; i < 5000; i++) {
      const segs = Array.from({ length: Math.floor(next() * 6) }, token).join('/');
      const query = next() < 0.4 ? `?${pick(['v', 'h', 'video', 'x', 'clip', 'p', 'story_fbid'])}=${token()}` : '';
      const url = `${pick(['https', 'http', 'ftp'])}://${pick(hosts)}/${segs}${query}`;
      const v = parseSiteVideo(url);
      if (!v) continue;
      found++;
      expect(v.key.startsWith(`${v.site}:`)).toBe(true);
      expect(v.pageUrl.startsWith('https://')).toBe(true);
      expect(parseSiteVideo(v.pageUrl)?.key).toBe(v.key);
      if (v.kind === 'embed' || v.kind === 'post' || v.kind === 'link') expect(v.lookAhead).toBe(false);
    }
    expect(found).toBeGreaterThan(0);
  });
});

describe('generic keys and addresses', () => {
  it('keys any http(s) page by its address without the fragment', () => {
    expect(genericVideoKey('https://video.example/watch/12?x=1#comments')).toBe('page:https://video.example/watch/12?x=1');
    expect(genericPageUrl('page:https://video.example/watch/12?x=1')).toBe('https://video.example/watch/12?x=1');
    expect(genericPageUrl(`youtube:${ID}`)).toBeUndefined();
    expect(genericVideoKey('chrome://settings')).toBeUndefined();
    expect(genericVideoKey('nope')).toBeUndefined();
  });

  it('compares origins and keeps http(s) Referers only', () => {
    expect(sameOrigin(WATCH, 'https://www.youtube.com/shorts/x')).toBe(true);
    expect(sameOrigin(WATCH, 'https://m.youtube.com/')).toBe(false);
    expect(sameOrigin(WATCH, undefined)).toBe(false);
    expect(httpAddress('https://blog.example/post')).toBe('https://blog.example/post');
    expect(httpAddress('file:///x')).toBeUndefined();
    expect(httpAddress('about:blank')).toBeUndefined();
  });
});

describe('siteOfHost and curatedPage (VIDEO-DETECT-SPEC §3.8 "Hosts", §5.1 "Feed videos")', () => {
  it.each([
    ['www.instagram.com', 'instagram'],
    ['www.tiktok.com', 'tiktok'],
    ['vm.tiktok.com', 'tiktok'],
    ['x.com', 'x'],
    ['mobile.twitter.com', 'x'],
    ['web.facebook.com', 'facebook'],
    ['fb.watch', 'facebook'],
    ['www.pinterest.com', 'pinterest'],
    ['someone.tumblr.com', 'tumblr'],
    ['www.threads.com', 'threads'],
    ['www.youtube.com', 'youtube'],
    ['instagram.com.evil.example', undefined],
    ['a.b.tumblr.com', undefined],
    ['www.example.com', undefined],
  ])('%s → %s', (host, site) => {
    expect(siteOfHost(host)).toBe(site);
  });

  it('names the site of every curated video address (one owner of the host column)', () => {
    for (const url of [
      'https://www.instagram.com/reel/CODEaaa111/',
      'https://www.tiktok.com/@some-one/video/7312345678901234567',
      'https://x.com/someone/status/1790000000000000000',
      'https://www.facebook.com/reel/1234567890123',
      'https://player.vimeo.com/video/76979871',
      'https://www.pinterest.com/pin/123456789012345678/',
    ]) {
      const v = parseSiteVideo(url);
      expect(v).toBeDefined();
      expect(siteOfAddress(url)).toBe(v?.site);
    }
    expect(siteOfAddress('ftp://www.instagram.com/')).toBeUndefined();
    expect(siteOfAddress('not a url')).toBeUndefined();
    expect(siteOfAddress(undefined)).toBeUndefined();
  });

  it.each([
    ['https://www.instagram.com/', 'feed'],
    ['https://www.instagram.com/someone/', 'feed'],
    ['https://www.tiktok.com/foryou', 'feed'],
    ['https://x.com/home', 'feed'],
    ['https://www.facebook.com/watch/', 'feed'],
    ['https://www.threads.com/', 'network'],
    ['https://www.snapchat.com/', 'network'],
    ['https://rumble.com/', 'network'],
    ['https://kick.com/', 'feed'],
    ['https://kick.com/someone', undefined],
    ['https://www.tiktok.com/@someone', 'feed'],
    ['https://www.tiktok.com/@someone/live', undefined],
    ['https://www.reddit.com/r/videos/new/', 'feed'],
    ['https://www.reddit.com/comments/abc123/', undefined],
    ['https://www.facebook.com/somepage/videos', 'feed'],
    ['https://www.facebook.com/permalink.php?story_fbid=1&id=2', undefined],
    ['https://www.bilibili.com/bangumi/play/ep100001', undefined],
    ['https://imgur.com/a/AbC12', undefined],
    ['https://www.threads.com/@someone', 'network'],
    ['https://www.instagram.com/reel/CODEaaa111/', undefined],
    ['https://www.example.com/', undefined],
    [undefined, undefined],
  ])('curatedPage(%s) is %s', (url, page) => {
    expect(curatedPage(url)).toBe(page);
  });
});

describe('addressSlide (SITE-MEDIA-SPEC §3.12: the item the user looks at)', () => {
  it('reads the slide an Instagram post address shows, 1 first', () => {
    expect(addressSlide('https://www.instagram.com/p/CODEaaa111/?img_index=3')).toBe(3);
    expect(addressSlide('https://www.instagram.com/someone/p/CODEaaa111/?img_index=1')).toBe(1);
    expect(addressSlide('https://www.instagram.com/reel/CODEaaa111/?img_index=2&utm_source=x')).toBe(2);
  });

  it('reads nothing elsewhere or from anything but a small whole number', () => {
    for (const raw of [
      undefined,
      'https://www.instagram.com/p/CODEaaa111/',
      'https://www.instagram.com/?img_index=2',
      'https://www.instagram.com/p/CODEaaa111/?img_index=0',
      'https://www.instagram.com/p/CODEaaa111/?img_index=02',
      'https://www.instagram.com/p/CODEaaa111/?img_index=1.5',
      'https://www.instagram.com/p/CODEaaa111/?img_index=1000',
      'https://www.instagram.com/p/CODEaaa111/?img_index=2&img_index=3',
      'https://www.example.com/p/CODEaaa111/?img_index=2',
    ]) {
      expect(addressSlide(raw), raw).toBeUndefined();
    }
  });
});

describe('the sign-in scope (SITE-MEDIA-SPEC §6.3)', () => {
  it('takes a curated site’s fixed domain when it holds the page, a generic page’s exact host', () => {
    expect(signInScope('instagram:AbCdEfGh', 'https://www.instagram.com/reel/AbCdEfGh/')).toEqual({ domain: 'instagram.com', exact: false });
    expect(signInScope('youtube:aqz-KE-bpKQ', 'https://www.youtube.com/watch?v=aqz-KE-bpKQ')).toEqual({ domain: 'youtube.com', exact: false });
    expect(signInScope('x:1', 'https://x.com/i/status/1')).toEqual({ domain: 'x.com', exact: false });
    expect(signInScope('vimeo:1', 'https://player.vimeo.com/video/1')).toEqual({ domain: 'vimeo.com', exact: false });
    expect(signInScope('twitch:clip:Slug', 'https://clips.twitch.tv/Slug')).toEqual({ domain: 'twitch.tv', exact: false });
    expect(signInScope('tumblr:blog/1', 'https://blog.tumblr.com/post/1')).toEqual({ domain: 'tumblr.com', exact: false });
    // Share links on another domain get none.
    expect(signInScope('facebook:AbCd1234', 'https://fb.watch/AbCd1234/')).toBeUndefined();
    expect(signInScope('pinterest:link:AbCd', 'https://pin.it/AbCd')).toBeUndefined();
    expect(signInScope('reddit:v:abcde', 'https://v.redd.it/abcde')).toBeUndefined();
    // A key whose site does not hold the page (never built so, but never trusted either).
    expect(signInScope('instagram:AbCdEfGh', 'https://www.facebook.com/reel/1')).toBeUndefined();
    expect(signInScope('instagram:AbCdEfGh', 'https://instagram.com.evil.example/reel/AbCdEfGh/')).toBeUndefined();
    expect(signInScope('page:https://video.blog.example/v/1', 'https://video.blog.example/v/1')).toEqual({ domain: 'video.blog.example', exact: true });
    // A curated site's own page without a known video address keeps the site's domain.
    expect(signInScope('page:https://www.instagram.com/stories/someone/123/', 'https://www.instagram.com/stories/someone/123/')).toEqual({
      domain: 'instagram.com',
      exact: false,
    });
    // fb.watch is Facebook's host but not on facebook.com: its exact host only.
    expect(signInScope('page:https://fb.watch/x/', 'https://fb.watch/x/')).toEqual({ domain: 'fb.watch', exact: true });
    expect(signInScope('page:https://VIDEO.example:8443/v', 'https://VIDEO.example:8443/v')).toEqual({ domain: 'video.example', exact: true });
    expect(signInScope('unknown:1', 'https://www.instagram.com/reel/AbCdEfGh/')).toBeUndefined();
    expect(signInScope('page:ftp://a.example/', 'ftp://a.example/')).toBeUndefined();
    expect(signInScope('instagram:x', 'not a url')).toBeUndefined();
  });

  it('keeps cookies of the domain and its subdomains, or of the exact host only', () => {
    const site = { domain: 'instagram.com', exact: false };
    for (const d of ['instagram.com', '.instagram.com', 'www.instagram.com', '.i.instagram.com', 'WWW.Instagram.COM']) {
      expect(inSignInScope(d, site), d).toBe(true);
    }
    for (const d of ['facebook.com', '.com', 'xinstagram.com', 'instagram.com.evil.example', 'stagram.com', '']) {
      expect(inSignInScope(d, site), d).toBe(false);
    }
    const host = { domain: 'video.blog.example', exact: true };
    expect(inSignInScope('video.blog.example', host)).toBe(true);
    expect(inSignInScope('.video.blog.example', host)).toBe(true);
    expect(inSignInScope('.blog.example', host)).toBe(false);
    expect(inSignInScope('sub.video.blog.example', host)).toBe(false);
  });

  it('names a sign-in domain for every site, each a lowercase name with a dot', () => {
    for (const [site, domains] of Object.entries(SIGN_IN_DOMAINS)) {
      expect(domains.length, site).toBeGreaterThan(0);
      for (const d of domains) expect(d, site).toMatch(/^[a-z0-9-]+(\.[a-z0-9-]+)+$/);
    }
  });
});
