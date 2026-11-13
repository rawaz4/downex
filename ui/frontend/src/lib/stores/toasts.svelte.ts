// Transient notices (announced through a polite live region), DESIGN-SYSTEM.md §3.17: a done
// thing (a Mint check), a plain fact (info) or a failure (danger). A toast that goes fades out
// for EXIT_MS (none under reduced motion); while it does, it is hidden from assistive technology.

import { motionOk } from '../motion.svelte';

export type ToastTone = 'success' | 'info' | 'error';

export interface Toast {
  readonly id: number;
  readonly message: string;
  readonly tone: ToastTone;
  /** Fading out: no longer announced or focusable. */
  readonly leaving?: boolean;
}

const LIFETIME_MS = 5000;
const MAX_TOASTS = 4;
/** How long a toast takes to fade out (DESIGN-SYSTEM.md §2.7: exit 140 ms). */
export const EXIT_MS = 140;

class Toasts {
  items = $state.raw<readonly Toast[]>([]);
  #next = 1;

  /** Shows a plain fact ("Nothing to pause"). */
  show(message: string, tone: ToastTone = 'info'): void {
    const toast: Toast = { id: this.#next++, message, tone };
    const kept = this.items.filter((t) => !t.leaving);
    const dropped = kept.length - (MAX_TOASTS - 1);
    this.items = [...this.items.filter((t) => t.leaving || dropped <= 0 || kept.indexOf(t) >= dropped), toast];
    setTimeout(() => this.dismiss(toast.id), tone === 'error' ? LIFETIME_MS * 2 : LIFETIME_MS);
  }

  /** Shows something that was done ("Settings saved", "2 downloads paused"). */
  success(message: string): void {
    this.show(message, 'success');
  }

  error(message: string): void {
    this.show(message, 'error');
  }

  dismiss(id: number): void {
    const t = this.items.find((x) => x.id === id);
    if (!t || t.leaving) return;
    if (!motionOk()) {
      this.items = this.items.filter((x) => x.id !== id);
      return;
    }
    this.items = this.items.map((x) => (x.id === id ? { ...x, leaving: true } : x));
    setTimeout(() => {
      this.items = this.items.filter((x) => x.id !== id);
    }, EXIT_MS);
  }
}

export const toasts = new Toasts();
