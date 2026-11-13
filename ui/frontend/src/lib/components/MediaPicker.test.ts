import { fireEvent, render, screen, within } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { defaultPick } from '../media';
import { chooseOption, makeMediaInfo, makeSiteMediaInfo, makeTrack, optionLabels, optionValues } from '../testing';
import MediaPicker from './MediaPicker.svelte';

const group = () => screen.getByRole('radiogroup', { name: 'Video quality' });
const radios = () => within(group()).getAllByRole('radio') as HTMLInputElement[];
const radio = (value: string) => radios().find((r) => r.value === value)!;
const checked = () => within(group()).getByRole('radio', { checked: true }) as HTMLInputElement;
const card = (value: string) => radio(value).closest('label') as HTMLLabelElement;

describe('MediaPicker: quality cards (DESIGN-SYSTEM.md §3.19)', () => {
  it('shows one card per class in downexd’s order: the class, its standard suffix, the codec and the size', () => {
    const info = makeSiteMediaInfo();
    render(MediaPicker, { props: { info, pick: defaultPick(info) } });
    expect(screen.getByRole('group', { name: 'Quality' })).toBeTruthy();
    expect(radios().map((r) => r.value)).toEqual(['299', '136', '397', '18']);
    const first = card('299');
    expect(first.querySelector('.cls')?.textContent).toBe('1080p');
    expect(first.querySelector('.suffix')?.textContent).toBe('(Full HD) 60fps');
    expect(first.querySelector('.codec')?.textContent).toBe('H.264');
    expect(first.querySelector('.size')?.textContent).toBe('482 MB');
    // a class without a suffix, an AV1 one, a size the site estimated
    expect(card('397').querySelector('.cls')?.textContent).toBe('480p');
    expect(card('397').querySelector('.suffix')).toBeNull();
    expect(card('397').querySelector('.codec')?.textContent).toBe('AV1');
    expect(card('397').querySelector('.size')?.textContent).toBe('~168 MB');
    // The tooltip names the whole quality (its suffix is cut in a narrow column), then its picture;
    // the hint row explains the order.
    expect(first.title).toBe('1080p (Full HD) 60fps · 1920×1080');
    expect(card('397').title).toBe('480p · 854×480');
    expect(screen.getByText('One MP4 per resolution, best first')).toBeTruthy();
  });

  it('marks the chosen card in Mint with a tick, and follows a click', async () => {
    const info = makeSiteMediaInfo();
    render(MediaPicker, { props: { info, pick: defaultPick(info) } });
    expect(checked().value).toBe('299');
    expect(card('299').classList.contains('on')).toBe(true);
    expect(card('299').querySelector('.tick')).toBeTruthy();
    expect(card('136').querySelector('.tick')).toBeNull();
    await fireEvent.click(radio('136'));
    expect(checked().value).toBe('136');
    expect(card('136').classList.contains('on')).toBe(true);
    expect(card('299').classList.contains('on')).toBe(false);
    expect(document.querySelectorAll('.qcard .tick')).toHaveLength(1);
  });

  it('is one native radio group for the keyboard: one name, every card a real radio, Space chooses', async () => {
    const info = makeSiteMediaInfo();
    render(MediaPicker, { props: { info, pick: defaultPick(info) } });
    // The browser moves between radios of one name with the arrow keys and tabs to the checked one.
    expect(new Set(radios().map((r) => r.name)).size).toBe(1);
    expect(radios().every((r) => r.type === 'radio' && r.tabIndex === 0)).toBe(true);
    // Visually hidden, never removed: the label is the card.
    expect(radios().every((r) => r.classList.contains('sr-only'))).toBe(true);
    const target = radio('397');
    target.focus();
    expect(document.activeElement).toBe(target);
    // Space on a focused radio is a click (what the browser dispatches).
    await fireEvent.click(target);
    expect(checked()).toBe(target);
  });

  it('names each card as its quality, codecs and size, and offers no audio-only card for a page', () => {
    const info = makeSiteMediaInfo();
    render(MediaPicker, { props: { info, pick: defaultPick(info) } });
    expect(radios().map((r) => r.getAttribute('aria-label'))).toEqual([
      '1080p (Full HD) 60fps · H.264 · 482 MB',
      '720p (HD) · H.264 · 126 MB',
      '480p · AV1 · ~168 MB',
      '360p · H.264 + AAC · ~45.0 MB',
    ]);
    expect(screen.queryByRole('radio', { name: 'No video (audio only)' })).toBeNull();
  });

  it('offers "No video (audio only)" as the last card only where the daemon picked no video', () => {
    const info = makeMediaInfo({ selectedVideo: '', selectedAudio: 'r1' });
    render(MediaPicker, { props: { info, pick: defaultPick(info) } });
    expect(radios().map((r) => r.value)).toEqual(['v1', 'v2', 'v0', '']);
    expect(checked().getAttribute('aria-label')).toBe('No video (audio only)');
    expect(card('').querySelector('.cls')?.textContent).toBe('Audio only');
  });

  it('turns every card off while the dialog is busy', () => {
    const info = makeSiteMediaInfo();
    render(MediaPicker, { props: { info, pick: defaultPick(info), disabled: true } });
    expect(radios().every((r) => r.disabled)).toBe(true);
    expect(group().getAttribute('aria-disabled')).toBe('true');
  });

  it('explains a mix of H.264 with newer codecs, and says nothing when all are H.264', () => {
    const info = makeSiteMediaInfo();
    render(MediaPicker, { props: { info, pick: defaultPick(info) } });
    const note = screen.getByText(/; H\.264 plays everywhere\.$/).closest<HTMLElement>('.note-line')!;
    expect(note.id).not.toBe('');
    expect(group().getAttribute('aria-describedby')).toContain(note.id);
    // one line in a narrow quality column: cut with an ellipsis there, whole in its tooltip
    expect(note.title).toBe('AV1 is smaller; H.264 plays everywhere.');
    document.body.innerHTML = '';
    const plain = makeMediaInfo();
    render(MediaPicker, { props: { info: plain, pick: defaultPick(plain) } });
    expect(screen.queryByText(/plays everywhere/)).toBeNull();
  });
});

describe('MediaPicker: the audio track', () => {
  it('chooses among a page’s audio tracks with a custom select', async () => {
    const info = makeSiteMediaInfo();
    render(MediaPicker, { props: { info, pick: defaultPick(info), part: 'audio' } });
    // only the audio part
    expect(screen.queryByRole('radiogroup')).toBeNull();
    const audio = screen.getByRole('combobox', { name: 'Audio track' }) as HTMLButtonElement;
    expect(audio.value).toBe('140');
    expect(optionValues(audio)).toEqual(['140', '140-1']);
    expect(optionLabels(audio)).toEqual(['English original', 'Deutsch (German)']);
    await chooseOption(audio, '140-1');
    expect(audio.value).toBe('140-1');
  });

  it('shows the one track there is, or that the chosen quality has its own sound', async () => {
    const info = makeSiteMediaInfo();
    const one = { ...info, tracks: info.tracks.filter((t) => t.id !== '140-1') };
    render(MediaPicker, { props: { info: one, pick: defaultPick(one) } });
    expect(screen.queryByRole('combobox', { name: 'Audio track' })).toBeNull();
    expect(document.querySelector('.static')?.textContent?.trim()).toBe('English original · AAC 129 kbit/s');
    await fireEvent.click(radio('18'));
    expect(document.querySelector('.static')?.textContent?.trim()).toBe('This video quality includes its sound.');
  });

  it('renders only the cards in the video part', () => {
    const info = makeSiteMediaInfo();
    render(MediaPicker, { props: { info, pick: defaultPick(info), part: 'video' } });
    expect(radios()).toHaveLength(4);
    expect(screen.queryByRole('combobox', { name: 'Audio track' })).toBeNull();
  });

  it('says when a stream offers nothing to choose', () => {
    const info = makeMediaInfo({ tracks: [], selectedVideo: '', selectedAudio: '' });
    render(MediaPicker, { props: { info, pick: defaultPick(info) } });
    expect(screen.getByText('The stream offers no qualities to choose from.')).toBeTruthy();
  });
});

describe('MediaPicker: texts', () => {
  const video = (id: string, codecs: string, height: number) =>
    makeTrack({ id, kind: 'video', width: Math.round((height * 16) / 9), height, codecs, bandwidth: 2_000_000 });
  const noteOf = (tracks: ReturnType<typeof video>[]) => {
    const info = makeMediaInfo({ tracks, selectedVideo: tracks[0]?.id ?? '', selectedAudio: '' });
    const r = render(MediaPicker, { props: { info, pick: defaultPick(info) } });
    const text = document.querySelector('.note-line')?.textContent?.trim() ?? '';
    r.unmount();
    return text;
  };

  it('names the codecs that are smaller than H.264, and says nothing without a mix', () => {
    expect(noteOf([video('a', 'avc1.640028', 1080), video('b', 'av01.0.08M.08', 2160)])).toBe(
      'AV1 is smaller; H.264 plays everywhere.',
    );
    expect(noteOf([video('a', 'avc1.640028', 1080), video('b', 'hvc1.1.6.L120', 1440), video('c', 'av01.0.08M.08', 2160)])).toBe(
      'AV1 and HEVC are smaller; H.264 plays everywhere.',
    );
    expect(noteOf([video('a', 'avc1.640028', 1080), video('b', 'avc1.4d401f', 720)])).toBe('');
    expect(noteOf([video('a', 'av01.0.08M.08', 1080)])).toBe('');
  });

  it('sizes a card by the file it makes, "~" when estimated, else its bitrate', () => {
    const sizeOf = (info: ReturnType<typeof makeMediaInfo>, id: string) => {
      const r = render(MediaPicker, { props: { info, pick: defaultPick(info) } });
      const text = card(id).querySelector('.size')?.textContent ?? '';
      r.unmount();
      return text;
    };
    // a page's declared sizes (the video with its audio), the site's estimate with "~"
    expect(sizeOf(makeSiteMediaInfo(), '299')).toBe('482 MB');
    expect(sizeOf(makeSiteMediaInfo(), '397')).toBe('~168 MB');
    // a manifest's: from the bandwidth and the length (5.128 Mbit/s × 10 min)
    expect(sizeOf(makeMediaInfo(), 'v1')).toBe('~385 MB');
    // nothing to estimate from: the bitrate
    expect(sizeOf(makeMediaInfo({ durationMs: 0 }), 'v1')).toBe('5.0 Mbit/s');
  });

  it('names audio tracks with their language and what they are', () => {
    const fr = makeTrack({ id: 'f', kind: 'audio', language: 'fr', bandwidth: 64_000, codecs: 'opus' });
    const bare = makeTrack({ id: 'x', kind: 'audio' });
    const site = makeSiteMediaInfo();
    render(MediaPicker, { props: { info: { ...site, tracks: [...site.tracks, fr, bare] }, pick: defaultPick(site), part: 'audio' } });
    const audio = screen.getByRole('combobox', { name: 'Audio track' }) as HTMLButtonElement;
    expect(optionLabels(audio)).toEqual(['English original', 'Deutsch (German)', 'French', 'Audio']);
    const details = [...document.querySelectorAll('[role=option]')].map((o) => o.querySelector('.detail')?.textContent ?? '');
    expect(details).toEqual(['AAC 129 kbit/s', 'AAC 129 kbit/s', 'Opus 64 kbit/s', '']);
    document.body.innerHTML = '';
    // a manifest's: the rate and the raw codecs
    const hls = makeMediaInfo();
    render(MediaPicker, { props: { info: hls, pick: defaultPick(hls), part: 'audio' } });
    const raw = [...document.querySelectorAll('[role=option]')].map((o) => o.querySelector('.detail')?.textContent ?? '');
    expect(raw).toEqual(['128 kbit/s · mp4a.40.2', '96 kbit/s · mp4a.40.2']);
  });
});
