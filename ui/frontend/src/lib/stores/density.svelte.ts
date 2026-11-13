// List density (DESIGN-SYSTEM.md §2.3): Comfortable (two lines per download, the default) or
// Compact (one dense line per download). A per-viewer choice stored in localStorage['downex.density']
// and applied as <html data-density>, like the theme store. Storage failures are ignored: the
// choice then lasts for the session.

export type DensityChoice = 'comfortable' | 'compact';

export const DENSITY_KEY = 'downex.density';
export const DENSITIES: readonly DensityChoice[] = ['comfortable', 'compact'];

/** What the store reads and writes; null when there is none. */
export type DensityStorage = Pick<Storage, 'getItem' | 'setItem'> | null;

function browserStorage(): DensityStorage {
  try {
    return typeof localStorage === 'undefined' ? null : localStorage;
  } catch {
    return null;
  }
}

export function parseDensity(v: unknown): DensityChoice {
  return v === 'compact' ? 'compact' : 'comfortable';
}

export class Density {
  choice = $state<DensityChoice>('comfortable');
  readonly #storage: () => DensityStorage;

  constructor(storage: () => DensityStorage = browserStorage) {
    this.#storage = storage;
    this.choice = this.#read();
    this.#apply();
  }

  /** Compact rows are shown. */
  get compact(): boolean {
    return this.choice === 'compact';
  }

  set(choice: DensityChoice): void {
    this.choice = parseDensity(choice);
    try {
      this.#storage()?.setItem(DENSITY_KEY, this.choice);
    } catch {
      // storage unavailable: the choice lasts for this session only
    }
    this.#apply();
  }

  toggle(): void {
    this.set(this.choice === 'compact' ? 'comfortable' : 'compact');
  }

  #read(): DensityChoice {
    try {
      return parseDensity(this.#storage()?.getItem(DENSITY_KEY));
    } catch {
      return 'comfortable';
    }
  }

  #apply(): void {
    if (typeof document === 'undefined') return;
    document.documentElement.dataset.density = this.choice;
  }
}

export const density = new Density();
