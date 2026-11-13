import { describe, expect, it } from 'vitest';
import { DEFAULT_SETTINGS } from './api';
import { awaitingInOrder, categoryFolder, joinDir, nextConfirmation } from './confirm';
import { makeDownload } from './testing';

const list = [
  makeDownload({ id: 1, state: 'queued', createdAt: 10 }),
  makeDownload({ id: 2, state: 'awaiting_confirmation', createdAt: 30 }),
  makeDownload({ id: 3, state: 'awaiting_confirmation', createdAt: 20 }),
  makeDownload({ id: 4, state: 'awaiting_confirmation', createdAt: 20 }),
];

describe('nextConfirmation', () => {
  it('takes the oldest waiting download (ties by id)', () => {
    expect(awaitingInOrder(list).map((d) => d.id)).toEqual([3, 4, 2]);
    expect(nextConfirmation(list, new Set())).toBe(3);
  });

  it('skips closed dialogs and prefers the one the user asked for', () => {
    expect(nextConfirmation(list, new Set([3]))).toBe(4);
    expect(nextConfirmation(list, new Set([2, 3, 4]))).toBeNull();
    expect(nextConfirmation(list, new Set([2, 3, 4]), 2)).toBe(2);
    expect(nextConfirmation(list, new Set(), 1)).toBe(3); // not waiting: ignored
    expect(nextConfirmation([], new Set(), 5)).toBeNull();
  });
});

describe('categoryFolder', () => {
  const settings = {
    ...DEFAULT_SETTINGS,
    defaultDir: 'C:\\Users\\you\\Downloads',
    categories: [
      { name: 'Video', extensions: ['mp4'], dir: '' },
      { name: 'Books', extensions: ['epub'], dir: 'D:\\Books' },
    ],
  };

  it('is the category folder, else <default>/<name>, else the default folder', () => {
    expect(categoryFolder(settings, 'Video')).toBe('C:\\Users\\you\\Downloads\\Video');
    expect(categoryFolder(settings, 'Books')).toBe('D:\\Books');
    expect(categoryFolder(settings, '')).toBe('C:\\Users\\you\\Downloads');
    expect(categoryFolder(settings, 'Gone')).toBe('C:\\Users\\you\\Downloads');
    expect(categoryFolder({ ...settings, useCategoryFolders: false }, 'Books')).toBe('C:\\Users\\you\\Downloads');
  });

  it('joins with the separator the folder uses', () => {
    expect(joinDir('/home/you/Downloads', 'Audio')).toBe('/home/you/Downloads/Audio');
    expect(joinDir('/home/you/Downloads/', 'Audio')).toBe('/home/you/Downloads/Audio');
    expect(joinDir('C:\\', 'Audio')).toBe('C:\\Audio');
    expect(joinDir('', 'Audio')).toBe('Audio');
  });
});
