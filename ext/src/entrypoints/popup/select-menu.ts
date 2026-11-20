// The extension's custom select (docs/DESIGN-SYSTEM.md §3.5, §4.11) in vanilla TypeScript, on the
// WAI-ARIA APG "select-only combobox": a <button role="combobox"> that carries `value` and
// `data-value` (so it takes a <label for> and reads like a field), and a listbox that is always in
// the DOM, hidden while closed and fixed-positioned under the trigger (over it when there is no room
// below). The list lives at the end of <body>, so no card, animation or scrolling area around the
// trigger can cut or cover it. The focus stays on the trigger; aria-activedescendant names the
// active option. Used by the popup's quality pickers and the options page's size unit.
//
// Closed: Enter, Space, Down, Up or Alt+Down open on the chosen option, Home and End on the first and
// last; typing chooses without opening. Open: Up/Down, Home/End, PageUp/PageDown (10), typing moves;
// Enter, Space or Alt+Up choose and close; Esc closes without a change (and goes no further); Tab
// chooses the active option and moves on; a click outside closes.
//
// A list that has no options yet can be asked for (`onRequest`, e.g. a video's qualities, which
// Downex gets only when the user asks): opening it calls `onRequest` instead, and the list opens when
// the options arrive while the trigger still has the focus.

export interface SelectMenuOption {
  readonly value: string;
  /** Shown in bold in the trigger. */
  readonly label: string;
  /** Secondary text after the label ("H.264"). */
  readonly detail?: string;
  /** A figure at the end of the option ("514 MB"); the trigger shows it after the detail. */
  readonly meta?: string;
  readonly title?: string;
  readonly disabled?: boolean;
  /** A short badge after the label in the list ("Recommended"). */
  readonly badge?: string;
}

export interface SelectMenuInit {
  /** The trigger's id (for a <label for>); the list and options derive theirs from it. */
  id: string;
  ariaLabel?: string;
  ariaLabelledby?: string;
  ariaDescribedby?: string;
  /** The trigger's text while no option is chosen ("Default quality"). */
  placeholder?: string;
  /** Extra classes of the trigger. */
  className?: string;
  onChange?: (value: string) => void;
  /** Asked to open while there are no options: load them. */
  onRequest?: () => void;
  doc?: Document;
}

/** How long typed letters make one search. */
export const TYPEAHEAD_MS = 500;
/** Options PageUp and PageDown move by. */
export const PAGE = 10;
/** Room kept between the list and the window's edges. */
const EDGE = 8;

const SVG_NS = 'http://www.w3.org/2000/svg';

function icon(doc: Document, d: string): SVGSVGElement {
  const svg = doc.createElementNS(SVG_NS, 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('class', 'icon');
  svg.setAttribute('aria-hidden', 'true');
  const path = doc.createElementNS(SVG_NS, 'path');
  path.setAttribute('d', d);
  svg.append(path);
  return svg;
}

function signature(options: readonly SelectMenuOption[]): string {
  return JSON.stringify(options.map((o) => [o.value, o.label, o.detail ?? '', o.meta ?? '', o.title ?? '', o.disabled === true, o.badge ?? '']));
}

export class SelectMenu {
  /** The wrapper of the trigger (the list is at the end of <body>). */
  readonly root: HTMLSpanElement;
  readonly trigger: HTMLButtonElement;
  readonly list: HTMLDivElement;

  readonly #doc: Document;
  readonly #init: SelectMenuInit;
  readonly #label: HTMLSpanElement;
  readonly #detail: HTMLSpanElement;
  #options: SelectMenuOption[] = [];
  #items: HTMLDivElement[] = [];
  #signature = '';
  #value = '';
  #active = -1;
  #open = false;
  /** The user asked to open while the options were being loaded. */
  #pending = false;
  #typed = '';
  #typedAt = 0;

  constructor(init: SelectMenuInit) {
    this.#init = init;
    const doc = init.doc ?? document;
    this.#doc = doc;
    this.root = doc.createElement('span');
    this.root.className = 'sm-root';

    const t = doc.createElement('button');
    t.type = 'button';
    t.id = init.id;
    t.className = `sm-trigger${init.className ? ` ${init.className}` : ''}`;
    t.setAttribute('role', 'combobox');
    t.setAttribute('aria-haspopup', 'listbox');
    t.setAttribute('aria-expanded', 'false');
    t.setAttribute('aria-controls', `${init.id}-list`);
    if (init.ariaLabel) t.setAttribute('aria-label', init.ariaLabel);
    if (init.ariaLabelledby) t.setAttribute('aria-labelledby', init.ariaLabelledby);
    if (init.ariaDescribedby) t.setAttribute('aria-describedby', init.ariaDescribedby);
    const value = doc.createElement('span');
    value.className = 'sm-value';
    this.#label = doc.createElement('span');
    this.#label.className = 'sm-label';
    this.#detail = doc.createElement('span');
    this.#detail.className = 'sm-detail';
    value.append(this.#label, this.#detail);
    const end = doc.createElement('span');
    end.className = 'sm-end';
    end.setAttribute('aria-hidden', 'true');
    const spinner = doc.createElement('span');
    spinner.className = 'spinner';
    end.append(icon(doc, 'm7 10 5 5 5-5'), spinner);
    t.append(value, end);
    this.trigger = t;

    const list = doc.createElement('div');
    list.id = `${init.id}-list`;
    list.className = 'sm-list';
    list.setAttribute('role', 'listbox');
    list.tabIndex = -1;
    list.hidden = true;
    if (init.ariaLabelledby) list.setAttribute('aria-labelledby', init.ariaLabelledby);
    else if (init.ariaLabel) list.setAttribute('aria-label', init.ariaLabel);
    this.list = list;

    this.root.append(t);
    (doc.body ?? doc.documentElement).append(list);
    this.#drawTrigger();

    t.addEventListener('click', () => {
      // The keyboard's active option gets a focus ring (`keys`), the pointer's only the tint.
      list.classList.remove('keys');
      if (this.#open) this.close();
      else this.#openOrRequest('selected');
    });
    t.addEventListener('keydown', (ev) => {
      list.classList.add('keys');
      this.#onKey(ev);
    });
    t.addEventListener('blur', (ev) => {
      // Focus moved elsewhere (not into the list, which never takes it): close without a change.
      if (this.#open && !this.#owns(ev.relatedTarget)) this.close(false);
      this.#pending = false;
    });
    // A press on an option keeps the focus on the trigger; the click chooses it.
    list.addEventListener('mousedown', (ev) => ev.preventDefault());
    list.addEventListener('click', (ev) => {
      const i = this.#indexOf(ev.target);
      if (i < 0 || this.#options[i]?.disabled) return;
      this.#choose(i);
      this.close();
    });
    list.addEventListener('pointermove', (ev) => {
      list.classList.remove('keys');
      const i = this.#indexOf(ev.target);
      if (i >= 0 && i !== this.#active && !this.#options[i]?.disabled) this.#setActive(i, false);
    });
  }

  get value(): string {
    return this.#value;
  }

  get options(): readonly SelectMenuOption[] {
    return this.#options;
  }

  get isOpen(): boolean {
    return this.#open;
  }

  /** Sets the options and the chosen value (no change event). Opens a list that was asked for. */
  setOptions(options: readonly SelectMenuOption[], value: string): void {
    const sig = signature(options);
    if (sig !== this.#signature) {
      const activeValue = this.#options[this.#active]?.value;
      this.#signature = sig;
      this.#options = [...options];
      this.#items = this.#options.map((o, i) => this.#item(o, i));
      this.list.replaceChildren(...this.#items);
      this.#active = activeValue === undefined ? -1 : this.#options.findIndex((o) => o.value === activeValue);
    }
    this.#value = value;
    this.#drawTrigger();
    this.#drawSelection();
    if (this.#open) {
      if (this.#options.length === 0) this.close(false);
      else {
        if (this.#active < 0) this.#setActive(this.#start('selected'), true);
        this.#place();
      }
    } else if (this.#pending && this.#options.length > 0) {
      this.#pending = false;
      if (this.#doc.activeElement === this.trigger && !this.trigger.disabled) this.open('selected');
    }
  }

  /** Chooses a value without a change event. */
  setValue(value: string): void {
    if (value === this.#value) return;
    this.#value = value;
    this.#drawTrigger();
    this.#drawSelection();
  }

  setDisabled(disabled: boolean): void {
    this.trigger.disabled = disabled;
    if (disabled && this.#open) this.close(false);
  }

  /** The options are being loaded: a spinner in place of the chevron. */
  setBusy(busy: boolean): void {
    if (busy) this.trigger.setAttribute('aria-busy', 'true');
    else this.trigger.removeAttribute('aria-busy');
  }

  setInvalid(invalid: boolean): void {
    if (invalid) this.trigger.setAttribute('aria-invalid', 'true');
    else this.trigger.removeAttribute('aria-invalid');
  }

  open(at: 'selected' | 'first' | 'last' = 'selected'): void {
    if (this.trigger.disabled || this.#options.length === 0) return;
    if (!this.#open) {
      this.#open = true;
      this.list.hidden = false;
      this.trigger.setAttribute('aria-expanded', 'true');
      this.#doc.addEventListener('pointerdown', this.#onOutside, true);
      this.#doc.defaultView?.addEventListener('resize', this.#onResize);
      this.#doc.addEventListener('scroll', this.#onScroll, true);
    }
    this.#setActive(this.#start(at), true);
    this.#place();
  }

  /** Closes the list; `refocus` keeps the focus on the trigger (it stays there anyway). */
  close(refocus = true): void {
    if (!this.#open) return;
    this.#open = false;
    this.list.hidden = true;
    this.trigger.setAttribute('aria-expanded', 'false');
    this.trigger.removeAttribute('aria-activedescendant');
    this.#doc.removeEventListener('pointerdown', this.#onOutside, true);
    this.#doc.defaultView?.removeEventListener('resize', this.#onResize);
    this.#doc.removeEventListener('scroll', this.#onScroll, true);
    if (refocus && this.#doc.activeElement !== this.trigger && this.root.isConnected) this.trigger.focus();
  }

  /** Closes, drops the document listeners and removes the list (the row is gone). */
  destroy(): void {
    this.close(false);
    this.#pending = false;
    this.list.remove();
  }

  #owns(target: EventTarget | null): boolean {
    const node = target as Node | null;
    return this.root.contains(node) || this.list.contains(node);
  }

  readonly #onOutside = (ev: Event): void => {
    if (!this.#owns(ev.target)) this.close(false);
  };

  // A toolbar popup grows and shrinks with its content: the list follows instead of closing.
  readonly #onResize = (): void => this.#place();

  readonly #onScroll = (ev: Event): void => {
    if (ev.target !== this.list) this.#place();
  };

  #openOrRequest(at: 'selected' | 'first' | 'last'): void {
    if (this.trigger.disabled) return;
    if (this.#options.length > 0) {
      this.open(at);
      return;
    }
    if (this.#init.onRequest) {
      this.#pending = true;
      this.#init.onRequest();
    }
  }

  #onKey(ev: KeyboardEvent): void {
    const key = ev.key;
    if (!this.#open) {
      switch (key) {
        case 'ArrowDown':
        case 'ArrowUp':
        case 'Enter':
        case ' ':
          ev.preventDefault();
          this.#openOrRequest('selected');
          return;
        case 'Home':
          ev.preventDefault();
          this.#openOrRequest('first');
          return;
        case 'End':
          ev.preventDefault();
          this.#openOrRequest('last');
          return;
      }
      if (this.#printable(ev)) {
        ev.preventDefault();
        const i = this.#search(key);
        if (i >= 0) this.#choose(i);
      }
      return;
    }
    switch (key) {
      case 'ArrowDown':
        ev.preventDefault();
        this.#move(1);
        return;
      case 'ArrowUp':
        ev.preventDefault();
        if (ev.altKey) {
          this.#chooseActive();
          this.close();
        } else this.#move(-1);
        return;
      case 'Home':
        ev.preventDefault();
        this.#setActive(this.#enabledFrom(0, 1), true);
        return;
      case 'End':
        ev.preventDefault();
        this.#setActive(this.#enabledFrom(this.#options.length - 1, -1), true);
        return;
      case 'PageDown':
        ev.preventDefault();
        this.#move(PAGE);
        return;
      case 'PageUp':
        ev.preventDefault();
        this.#move(-PAGE);
        return;
      case 'Enter':
        ev.preventDefault();
        this.#chooseActive();
        this.close();
        return;
      case ' ':
        // A space inside a typed search belongs to it ("4K (2160p)").
        if (this.#typed && Date.now() - this.#typedAt < TYPEAHEAD_MS) break;
        ev.preventDefault();
        this.#chooseActive();
        this.close();
        return;
      case 'Escape':
        ev.preventDefault();
        ev.stopPropagation();
        this.close();
        return;
      case 'Tab':
        // The focus moves on as usual; the active option is chosen first.
        this.#chooseActive();
        this.close(false);
        return;
    }
    if (this.#printable(ev)) {
      ev.preventDefault();
      const i = this.#search(key);
      if (i >= 0) this.#setActive(i, true);
    }
  }

  #printable(ev: KeyboardEvent): boolean {
    return ev.key.length === 1 && !ev.ctrlKey && !ev.metaKey && !ev.altKey && (ev.key !== ' ' || this.#typed !== '');
  }

  /** The next option whose label starts with the typed text (one letter again: the next such). */
  #search(key: string): number {
    const now = Date.now();
    this.#typed = now - this.#typedAt < TYPEAHEAD_MS ? this.#typed + key : key;
    this.#typedAt = now;
    const typed = this.#typed.toLocaleLowerCase();
    const repeat = typed.length > 1 && [...typed].every((c) => c === typed[0]);
    const needle = repeat ? typed[0]! : typed;
    const n = this.#options.length;
    const from = this.#open ? this.#active : this.#options.findIndex((o) => o.value === this.#value);
    // One letter (or the same letter again) goes to the next match; a longer text may stay.
    const base = from < 0 ? 0 : from + (repeat || needle.length === 1 ? 1 : 0);
    for (let k = 0; k < n; k++) {
      const i = (base + k) % n;
      const o = this.#options[i];
      if (o && !o.disabled && o.label.toLocaleLowerCase().startsWith(needle)) return i;
    }
    return -1;
  }

  #start(at: 'selected' | 'first' | 'last'): number {
    if (at === 'first') return this.#enabledFrom(0, 1);
    if (at === 'last') return this.#enabledFrom(this.#options.length - 1, -1);
    const chosen = this.#options.findIndex((o) => o.value === this.#value && !o.disabled);
    return chosen >= 0 ? chosen : this.#enabledFrom(0, 1);
  }

  /** The first enabled option from `i` in direction `dir` (-1 when none). */
  #enabledFrom(i: number, dir: 1 | -1): number {
    for (let k = i; k >= 0 && k < this.#options.length; k += dir) if (!this.#options[k]?.disabled) return k;
    return -1;
  }

  #move(by: number): void {
    const n = this.#options.length;
    if (n === 0) return;
    const dir = by > 0 ? 1 : -1;
    const target = Math.min(n - 1, Math.max(0, (this.#active < 0 ? (dir > 0 ? -1 : n) : this.#active) + by));
    let i = this.#enabledFrom(target, dir);
    if (i < 0) i = this.#enabledFrom(target, dir > 0 ? -1 : 1);
    if (i >= 0) this.#setActive(i, true);
  }

  #setActive(i: number, scroll: boolean): void {
    this.#items[this.#active]?.classList.remove('active');
    this.#active = i;
    const item = this.#items[i];
    if (!item) {
      this.trigger.removeAttribute('aria-activedescendant');
      return;
    }
    item.classList.add('active');
    if (this.#open) this.trigger.setAttribute('aria-activedescendant', item.id);
    if (scroll && typeof item.scrollIntoView === 'function') item.scrollIntoView({ block: 'nearest' });
  }

  #chooseActive(): void {
    if (this.#active >= 0 && !this.#options[this.#active]?.disabled) this.#choose(this.#active);
  }

  #choose(i: number): void {
    const o = this.#options[i];
    if (!o || o.disabled) return;
    if (this.#open) this.#setActive(i, false);
    if (o.value === this.#value) return;
    this.#value = o.value;
    this.#drawTrigger();
    this.#drawSelection();
    this.#init.onChange?.(o.value);
  }

  #indexOf(target: EventTarget | null): number {
    const item = (target as Element | null)?.closest?.('[role="option"]');
    return item ? this.#items.indexOf(item as HTMLDivElement) : -1;
  }

  #item(o: SelectMenuOption, i: number): HTMLDivElement {
    const doc = this.#doc;
    const item = doc.createElement('div');
    item.id = `${this.#init.id}-opt-${i}`;
    item.className = 'sm-option';
    item.setAttribute('role', 'option');
    item.setAttribute('aria-selected', 'false');
    item.dataset.value = o.value;
    if (o.title) item.title = o.title;
    if (o.disabled) item.setAttribute('aria-disabled', 'true');
    const check = doc.createElement('span');
    check.className = 'sm-check';
    check.setAttribute('aria-hidden', 'true');
    check.append(icon(doc, 'M5 12.5l4.5 4.5L19 7.5'));
    const text = doc.createElement('span');
    text.className = 'sm-text';
    const label = doc.createElement('span');
    label.className = 'sm-olabel';
    label.textContent = o.label;
    text.append(label);
    if (o.badge) {
      const badge = doc.createElement('span');
      badge.className = 'sm-badge';
      badge.textContent = o.badge;
      label.append(' ', badge);
    }
    if (o.detail) {
      const detail = doc.createElement('span');
      detail.className = 'sm-odetail';
      detail.textContent = o.detail;
      text.append(detail);
    }
    item.append(check, text);
    if (o.meta) {
      const meta = doc.createElement('span');
      meta.className = 'sm-ometa';
      meta.textContent = o.meta;
      item.append(meta);
    }
    return item;
  }

  #drawTrigger(): void {
    const o = this.#options.find((x) => x.value === this.#value);
    this.trigger.value = this.#value;
    this.trigger.dataset.value = this.#value;
    this.trigger.dataset.empty = String(!o);
    const label = o ? o.label : (this.#init.placeholder ?? '');
    const detail = o ? [o.detail, o.meta].filter((p): p is string => !!p).join(' · ') : '';
    if (this.#label.textContent !== label) this.#label.textContent = label;
    if (this.#detail.textContent !== detail) this.#detail.textContent = detail;
    this.#detail.hidden = detail === '';
    if (o?.title) this.trigger.title = o.title;
    else this.trigger.removeAttribute('title');
  }

  #drawSelection(): void {
    for (const [i, item] of this.#items.entries()) item.setAttribute('aria-selected', String(this.#options[i]?.value === this.#value));
  }

  /** Under the trigger, or over it when the room below is short and there is more above. */
  #place(): void {
    if (!this.#open) return;
    const win = this.#doc.defaultView;
    if (!win) return;
    const r = this.trigger.getBoundingClientRect();
    const list = this.list;
    list.style.minWidth = `${Math.round(r.width)}px`;
    list.style.maxHeight = '';
    const height = list.offsetHeight;
    const width = list.offsetWidth;
    const below = win.innerHeight - r.bottom - EDGE;
    const above = r.top - EDGE;
    const up = height > below && above > below;
    const room = Math.max(0, (up ? above : below) - 4);
    const shown = Math.min(height, room);
    if (height > room) list.style.maxHeight = `${Math.floor(room)}px`;
    list.dataset.place = up ? 'above' : 'below';
    list.style.top = `${Math.round(up ? r.top - 4 - shown : r.bottom + 4)}px`;
    list.style.left = `${Math.round(Math.max(EDGE, Math.min(r.left, win.innerWidth - width - EDGE)))}px`;
  }
}
