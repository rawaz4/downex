import { describe, expect, it } from 'vitest';
import { DEFAULT_CATEGORIES } from './api';
import {
  CategoryIndex,
  categoryKind,
  classifyName,
  displayName,
  extensionOf,
  kindOf,
  matchesStatus,
  statusBucket,
} from './categories';
import { makeDownload } from './testing';

describe('extensionOf', () => {
  it.each([
    ['movie.MP4', 'mp4'],
    ['Show #37 (1h41m01s-1h57m22s).mp4', 'mp4'],
    ['game#1.exe', 'exe'],
    ['what?.pdf', 'pdf'],
    ['archive.tar.gz', 'gz'],
    ['.bashrc', ''],
    ['file.', ''],
    ['noext', ''],
    ['https://example.com/a/b.pdf?x=1.zip#frag.exe', 'pdf'],
    ['C:\\Users\\you\\setup.EXE', 'exe'],
    ['/home/you/dir.v2/readme', ''],
  ])('%s → %s', (name, ext) => {
    expect(extensionOf(name)).toBe(ext);
  });
});

describe('classifyName', () => {
  it.each([
    ['clip.mkv', 'video'],
    ['song.FLAC', 'audio'],
    ['backup.7z', 'archives'],
    ['dataset.tar.xz', 'archives'],
    ['report.pdf', 'documents'],
    ['sheet.xlsx', 'documents'],
    ['setup.msi', 'programs'],
    ['tool_1.0_amd64.deb', 'programs'],
    ['editor.AppImage', 'programs'],
    ['firmware.bin', 'other'],
    ['README', 'other'],
  ])('%s → %s', (name, category) => {
    expect(classifyName(name)).toBe(category);
  });
});

describe('displayName and kindOf', () => {
  it('prefers the filename', () => {
    const d = makeDownload({ id: 1, filename: 'talk.webm', url: 'https://example.com/x.zip' });
    expect(displayName(d)).toBe('talk.webm');
    expect(kindOf(d)).toBe('video');
  });

  it('falls back to the decoded last path segment of the final URL, then the URL', () => {
    const d = makeDownload({
      id: 2,
      filename: '',
      url: 'https://example.com/get?id=1',
      finalUrl: 'https://cdn.example.com/files/my%20notes.pdf',
    });
    expect(displayName(d)).toBe('my notes.pdf');
    expect(kindOf(d)).toBe('documents');
    const bare = makeDownload({ id: 3, filename: '', url: 'https://example.com/', finalUrl: '' });
    expect(displayName(bare)).toBe('https://example.com/');
    expect(kindOf(bare)).toBe('other');
  });
});

describe('status buckets', () => {
  it('maps daemon states to the sidebar filters', () => {
    expect(statusBucket('awaiting_confirmation')).toBe('awaiting');
    expect(statusBucket('downloading')).toBe('active');
    expect(statusBucket('probing')).toBe('active');
    expect(statusBucket('queued')).toBe('queued');
    expect(statusBucket('paused')).toBe('paused');
    expect(statusBucket('canceled')).toBe('paused');
    expect(statusBucket('completed')).toBe('completed');
    expect(statusBucket('failed')).toBe('failed');
    expect(statusBucket('unspecified')).toBeNull();
  });

  it('matches "any" for every state', () => {
    expect(matchesStatus('unspecified', 'any')).toBe(true);
    expect(matchesStatus('failed', 'active')).toBe(false);
    expect(matchesStatus('probing', 'active')).toBe(true);
  });
});

describe('CategoryIndex', () => {
  const index = new CategoryIndex([
    ...DEFAULT_CATEGORIES,
    { name: 'Books', extensions: ['epub', 'mobi'], dir: '' },
    { name: 'Video', extensions: ['dup'], dir: '' }, // a duplicate keeps the first
    { name: 'Comics', extensions: ['CBZ', '.cbr'], dir: '' },
  ]);

  it('lists the configured categories in order, without duplicates', () => {
    expect(index.entries.map((e) => [e.key, e.kind])).toEqual([
      ['cat:Video', 'video'],
      ['cat:Audio', 'audio'],
      ['cat:Archives', 'archives'],
      ['cat:Documents', 'documents'],
      ['cat:Programs', 'programs'],
      ['cat:Books', 'documents'],
      ['cat:Comics', 'other'],
    ]);
    expect(index.has('cat:Books')).toBe(true);
    expect(index.has('cat:Gone')).toBe(false);
    expect(index.has('all') && index.has('other')).toBe(true);
    expect(index.labelOf('cat:Books')).toBe('Books');
    expect(index.labelOf('other')).toBe('Other');
  });

  it('uses the category the daemon chose while it exists, else the extension, else other', () => {
    expect(index.bucketOf(makeDownload({ id: 1, filename: 'a.zip', category: 'Books' }))).toBe('cat:Books');
    // from before categories, or a renamed category: grouped by extension (first category wins)
    expect(index.bucketOf(makeDownload({ id: 2, filename: 'a.mobi', category: '' }))).toBe('cat:Books');
    expect(index.bucketOf(makeDownload({ id: 3, filename: 'a.epub', category: 'Ebooks' }))).toBe('cat:Documents');
    expect(index.bucketOf(makeDownload({ id: 4, filename: 'x.cbr' }))).toBe('cat:Comics');
    expect(index.bucketOf(makeDownload({ id: 5, filename: 'x.dup' }))).toBe('other');
    expect(index.bucketOf(makeDownload({ id: 6, filename: '', url: 'https://example.com/v/clip.MKV?x=1' }))).toBe('cat:Video');
    expect(new CategoryIndex([]).bucketOf(makeDownload({ id: 7, filename: 'a.mp4', category: 'Video' }))).toBe('other');
  });

  it('gives a category the kind of its name or of most of its extensions', () => {
    expect(categoryKind({ name: 'Films', extensions: ['mp4', 'mkv', 'pdf'] })).toBe('video');
    expect(categoryKind({ name: 'audio', extensions: [] })).toBe('audio');
    expect(categoryKind({ name: 'Misc', extensions: ['xyz'] })).toBe('other');
  });
});