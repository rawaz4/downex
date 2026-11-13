import { describe, expect, it } from 'vitest';
import { displayName, kindOf, isExecutableName, rawName } from './categories';
import { joinPath } from './display';
import { escapeBidi, hasBidiControls, isolate, splitExtension } from './text';
import { makeDownload } from './testing';

const RLO = '\u202E';

describe('bidi control characters in names (spoofed extensions)', () => {
  it('makes every bidi control visible', () => {
    expect(escapeBidi(`invoice${RLO}fdp.exe`)).toBe('invoice⟨U+202E⟩fdp.exe');
    const all = '\u202A\u202B\u202C\u202D\u202E\u2066\u2067\u2068\u2069\u200E\u200F\u061C';
    expect(escapeBidi(all)).toBe(
      '⟨U+202A⟩⟨U+202B⟩⟨U+202C⟩⟨U+202D⟩⟨U+202E⟩⟨U+2066⟩⟨U+2067⟩⟨U+2068⟩⟨U+2069⟩⟨U+200E⟩⟨U+200F⟩⟨U+061C⟩',
    );
    expect(hasBidiControls(all)).toBe(true);
    expect(escapeBidi('plain name.pdf')).toBe('plain name.pdf');
    expect(escapeBidi('تقرير.pdf')).toBe('تقرير.pdf');
  });

  it('shows the escaped name but keeps the raw one for classification and actions', () => {
    const d = makeDownload({ id: 1, filename: '', url: 'https://example.org/pub/invoice%E2%80%AEfdp.exe' });
    expect(rawName(d)).toBe(`invoice${RLO}fdp.exe`);
    expect(displayName(d)).toBe('invoice⟨U+202E⟩fdp.exe');
    expect(kindOf(d)).toBe('programs');
    expect(isExecutableName(rawName(d))).toBe(true);
    const named = makeDownload({ id: 2, filename: `photo${RLO}gpj.scr` });
    expect(displayName(named)).toBe('photo⟨U+202E⟩gpj.scr');
    expect(isExecutableName(named.filename)).toBe(true);
    expect(isExecutableName('report.pdf')).toBe(false);
  });

  it('escapes paths shown in the details panel', () => {
    expect(joinPath('/home/you/Downloads', `invoice${RLO}fdp.exe`)).toBe('/home/you/Downloads/invoice⟨U+202E⟩fdp.exe');
    expect(joinPath(`C:\\Users\\${RLO}x`, '')).toBe('C:\\Users\\⟨U+202E⟩x');
  });

  it('isolates inserted values so they cannot reorder the sentence around them', () => {
    expect(isolate('(final) report.pdf')).toBe('\u2068(final) report.pdf\u2069');
    // an embedded PDI cannot close the isolate early
    expect(isolate('a\u2069b')).toBe('\u2068a⟨U+2069⟩b\u2069');
  });
});

describe('splitExtension (the extension stays at the end of a right-to-left name)', () => {
  it('splits off a trailing Latin extension', () => {
    expect(splitExtension('وزە بۆ کردنی کارێک ؟.mp4')).toEqual({ stem: 'وزە بۆ کردنی کارێک ؟', ext: '.mp4' });
    expect(splitExtension('Win.10.Aio.19045.7807.Multi.iso')).toEqual({ stem: 'Win.10.Aio.19045.7807.Multi', ext: '.iso' });
    expect(splitExtension('archive.tar.gz')).toEqual({ stem: 'archive.tar', ext: '.gz' });
  });

  it('keeps names without such an extension whole', () => {
    for (const name of ['README', '.gitignore', 'notes.', 'ڕاپۆرت.پدف', 'file.name with space']) {
      expect(splitExtension(name)).toEqual({ stem: name, ext: '' });
    }
  });

  it('never loses or reorders characters', () => {
    for (const name of ['a.b', 'invoice⟨U+202E⟩fdp.exe', '2015_12_17 لە سوید.mp4']) {
      const { stem, ext } = splitExtension(name);
      expect(stem + ext).toBe(name);
    }
  });
});
