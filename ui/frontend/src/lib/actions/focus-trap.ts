// Modal focus management: moves focus into the node, keeps Tab/Shift+Tab inside it,
// closes on Escape, and returns focus to where it was when the node goes away.
//
// Regions marked with TRAP_EXEMPT (the countdown of a post-queue action, the window controls
// of a frameless window) stay within reach while a modal is open: focus may move there, Tab
// goes on from the node's last control to theirs and back to the node's first, and a modal
// that focuses itself (it opened by itself) leaves the focus in such a region where it already
// is, unless the region is only Tab-reachable (`data-focus-trap-exempt="tab"`: the window
// controls, which must not swallow the keys meant for a dialog that opened by itself).

/** Marks a region outside every modal that keeps the keyboard while one is open. */
export const TRAP_EXEMPT = '[data-focus-trap-exempt]';
/** An exempt region that Tab reaches but that does not keep the focus from a self-focusing modal. */
const TAB_ONLY = '[data-focus-trap-exempt="tab"]';

const FOCUSABLE = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled]):not([type="hidden"])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
  '[contenteditable="true"]',
].join(',');

export function focusableIn(root: HTMLElement): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
    (el) => !el.closest('[hidden],[inert]'),
  );
}

/**
 * Whether focus can be returned to `el`: still in the document, not disabled, not
 * inside an inert or hidden subtree. (A button that became disabled while the dialog
 * was open, such as Remove after its rows are gone, would silently drop focus to
 * <body>.)
 */
export function canRestoreFocus(el: HTMLElement | null | undefined): el is HTMLElement {
  if (!el || !el.isConnected) return false;
  if (el.matches(':disabled')) return false;
  if (el.closest('[inert],[hidden]')) return false;
  if (el.getAttribute('aria-hidden') === 'true' || el.closest('[aria-hidden="true"]')) return false;
  // Not rendered (display: none here or on an ancestor, or visibility: hidden).
  if (typeof el.checkVisibility === 'function') return el.checkVisibility({ visibilityProperty: true });
  const style = el.ownerDocument.defaultView?.getComputedStyle(el);
  return !style || (style.display !== 'none' && style.visibility !== 'hidden');
}

export interface TrapOptions {
  /** Called on Escape. */
  onEscape?: () => void;
  /** Where focus goes on close when the previously focused element no longer exists. */
  fallback?: () => HTMLElement | null | undefined;
  /**
   * Focus the node itself at first (it needs a tabindex), not its first focusable element;
   * focus already in an exempt region stays there.
   */
  focusSelf?: boolean;
}

/** `target` lies in an exempt region outside `node`. */
function inExempt(node: HTMLElement, target: EventTarget | null): boolean {
  if (!(target instanceof Element) || node.contains(target)) return false;
  const region = target.closest(TRAP_EXEMPT);
  return region !== null && !region.closest('[inert],[hidden]');
}

/** The focusable elements of the exempt regions outside `node`, in document order. */
function exemptItems(node: HTMLElement): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>(TRAP_EXEMPT))
    .filter((r) => !node.contains(r) && !r.closest('[inert],[hidden]'))
    .flatMap((r) => focusableIn(r));
}

export function trapFocus(node: HTMLElement, options: TrapOptions = {}) {
  let opts = options;
  const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;

  const keeps = inExempt(node, previous) && previous?.closest(TRAP_EXEMPT)?.matches(TAB_ONLY) === false;
  if (!(options.focusSelf && keeps)) {
    const initial = options.focusSelf
      ? node
      : (node.querySelector<HTMLElement>('[data-autofocus]:not([disabled])') ?? focusableIn(node)[0] ?? node);
    initial.focus();
  }

  function onKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape' && opts.onEscape) {
      e.preventDefault();
      e.stopPropagation();
      opts.onEscape();
      return;
    }
    if (e.key !== 'Tab') return;
    const items = focusableIn(node);
    const first = items[0];
    const last = items[items.length - 1];
    const exempt = exemptItems(node);
    if (!first || !last) {
      e.preventDefault();
      (exempt[0] ?? node).focus();
      return;
    }
    const active = document.activeElement;
    if (e.shiftKey && (active === first || active === node || !node.contains(active))) {
      e.preventDefault();
      (exempt[exempt.length - 1] ?? last).focus();
    } else if (!e.shiftKey && (active === last || !node.contains(active))) {
      e.preventDefault();
      (exempt[0] ?? first).focus();
    }
  }

  /** Tab in an exempt region: from its last control to the node's first, and back. */
  function onDocumentKeydown(e: KeyboardEvent) {
    if (e.key !== 'Tab' || e.defaultPrevented || !inExempt(node, e.target)) return;
    const exempt = exemptItems(node);
    const items = focusableIn(node);
    if (e.shiftKey && e.target === exempt[0]) {
      e.preventDefault();
      (items[items.length - 1] ?? node).focus();
    } else if (!e.shiftKey && e.target === exempt[exempt.length - 1]) {
      e.preventDefault();
      (items[0] ?? node).focus();
    }
  }

  function onFocusIn(e: FocusEvent) {
    if (e.target instanceof Node && !node.contains(e.target) && !inExempt(node, e.target)) {
      (focusableIn(node)[0] ?? node).focus();
    }
  }

  node.addEventListener('keydown', onKeydown);
  document.addEventListener('keydown', onDocumentKeydown);
  document.addEventListener('focusin', onFocusIn);

  return {
    update(next: TrapOptions = {}) {
      opts = next;
    },
    destroy() {
      node.removeEventListener('keydown', onKeydown);
      document.removeEventListener('keydown', onDocumentKeydown);
      document.removeEventListener('focusin', onFocusIn);
      const fallback = opts.fallback;
      // After the node is gone (so focusin no longer bounces back into it) and after the
      // page has updated, so a disabled or still-inert opener is seen as such.
      queueMicrotask(() => {
        const target = canRestoreFocus(previous) ? previous : fallback?.();
        target?.focus();
      });
    },
  };
}
