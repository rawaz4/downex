import { afterEach, describe, expect, it } from 'vitest';
import { DENSITY_KEY, Density, density, parseDensity, type DensityStorage } from './density.svelte';

function memory(initial: Record<string, string> = {}): DensityStorage & { data: Map<string, string> } {
  const data = new Map(Object.entries(initial));
  return {
    data,
    getItem: (k) => data.get(k) ?? null,
    setItem: (k, v) => void data.set(k, String(v)),
  };
}

const broken: DensityStorage = {
  getItem: () => {
    throw new Error('SecurityError: storage is disabled');
  },
  setItem: () => {
    throw new Error('QuotaExceededError');
  },
};

afterEach(() => {
  delete document.documentElement.dataset.density;
  try {
    localStorage.removeItem(DENSITY_KEY);
  } catch {
    // jsdom always has storage
  }
});

describe('density store', () => {
  it('starts Comfortable and applies html[data-density]', () => {
    const d = new Density(() => memory());
    expect(d.choice).toBe('comfortable');
    expect(d.compact).toBe(false);
    expect(document.documentElement.dataset.density).toBe('comfortable');
  });

  it('persists the choice per viewer and reads it back', () => {
    const store = memory();
    const d = new Density(() => store);
    d.set('compact');
    expect(store.data.get(DENSITY_KEY)).toBe('compact');
    expect(document.documentElement.dataset.density).toBe('compact');
    expect(new Density(() => store).choice).toBe('compact');
    d.toggle();
    expect(d.choice).toBe('comfortable');
    expect(store.data.get(DENSITY_KEY)).toBe('comfortable');
    expect(document.documentElement.dataset.density).toBe('comfortable');
  });

  it('reads anything unknown as Comfortable', () => {
    expect(new Density(() => memory({ [DENSITY_KEY]: 'cosy' })).choice).toBe('comfortable');
    expect(parseDensity('compact')).toBe('compact');
    expect(parseDensity(null)).toBe('comfortable');
  });

  it('keeps working for the session when storage fails or is missing', () => {
    const d = new Density(() => broken);
    expect(d.choice).toBe('comfortable');
    expect(() => d.set('compact')).not.toThrow();
    expect(d.choice).toBe('compact');
    expect(document.documentElement.dataset.density).toBe('compact');
    const none = new Density(() => null);
    none.toggle();
    expect(none.choice).toBe('compact');
  });

  it('uses localStorage by default', () => {
    density.set('compact');
    expect(localStorage.getItem(DENSITY_KEY)).toBe('compact');
    density.set('comfortable');
    expect(localStorage.getItem(DENSITY_KEY)).toBe('comfortable');
  });
});
