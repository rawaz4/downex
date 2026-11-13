// Options page (BROWSER-SPEC §3, §4): capture rules editor saved to storage.local, and the
// video options (SITE-MEDIA-SPEC §3.8, §6.3; VIDEO-DETECT-SPEC §5.4): the switches are saved at
// once, the hidden sites when the field is left. Drawn on the Studio tokens (DESIGN-SYSTEM.md
// §4.12): the brand mark in the rail, the size unit as the extension's select menu.

import { browser } from 'wxt/browser';
import { mountBrandMarks } from '../../assets/brand';
import {
  formatHiddenSites,
  mergeExternalRules,
  parseRulesForm,
  parseVideoOptions,
  rulesToForm,
  VIDEO_OPTIONS_KEY,
  type RulesField,
  type RulesForm,
  type RulesFormField,
  type VideoOptions,
} from '../../lib/options-form';
import { errorMessage } from '../../lib/protocol';
import { RULES_STORAGE_KEY, defaultRules, parseRules, type CaptureRules } from '../../lib/rules';
import { STORE_OPTION_HINTS, storeBuild } from '../../lib/store-build';
import { hiddenSitesField, hiddenSitesResult } from '../../lib/ui-format';
import { SelectMenu } from '../popup/select-menu';

function byId<T extends HTMLElement>(id: string, type: new () => T): T {
  const el = document.getElementById(id);
  if (!(el instanceof type)) throw new Error(`missing #${id}`);
  return el;
}

const form = byId('rules-form', HTMLFormElement);
const enabledEl = byId('enabled', HTMLInputElement);
const enabledHintEl = byId('enabled-hint', HTMLSpanElement);
const extensionsEl = byId('extensions', HTMLTextAreaElement);
const extensionsCountEl = byId('extensions-count', HTMLSpanElement);
const minSizeEl = byId('min-size', HTMLInputElement);
const hostsEl = byId('excluded-hosts', HTMLTextAreaElement);
const resetBtn = byId('reset', HTMLButtonElement);
const statusEl = byId('form-status', HTMLParagraphElement);
const videoButtonEl = byId('video-button', HTMLInputElement);
const videoSignInEl = byId('video-sign-in', HTMLInputElement);
const videoPrefetchEl = byId('video-prefetch', HTMLInputElement);
const videoStatusEl = byId('video-status', HTMLParagraphElement);
const hiddenSitesEl = byId('hidden-sites', HTMLTextAreaElement);
const hiddenSitesErrorEl = byId('hidden-sites-error', HTMLParagraphElement);

const FIELDS: Readonly<Record<RulesField, { input: HTMLInputElement | HTMLTextAreaElement; error: HTMLElement }>> = {
  extensions: { input: extensionsEl, error: byId('extensions-error', HTMLParagraphElement) },
  minSize: { input: minSizeEl, error: byId('min-size-error', HTMLParagraphElement) },
  excludedHosts: { input: hostsEl, error: byId('excluded-hosts-error', HTMLParagraphElement) },
};

/** Fields with unsaved edits: external rule changes (the popup) are merged around them. */
const edited = new Set<RulesFormField>();

mountBrandMarks(document);
// The store build's hints do not name the site it leaves out (lib/store-build.ts).
if (storeBuild()) for (const [id, text] of Object.entries(STORE_OPTION_HINTS)) byId(id, HTMLSpanElement).textContent = text;

/** The minimum size's unit: a select menu (no native <select>), part of the "minSize" field. */
const unitMenu = new SelectMenu({
  id: 'min-size-unit',
  ariaLabelledby: 'min-size-unit-label',
  ariaDescribedby: 'min-size-hint',
  className: 'unit-trigger',
  onChange: () => {
    edited.add('minSize');
    setStatus('');
    renderFormNotes();
  },
});
unitMenu.setOptions(
  [
    { value: 'B', label: 'bytes' },
    { value: 'KB', label: 'KB' },
    { value: 'MB', label: 'MB' },
    { value: 'GB', label: 'GB' },
  ],
  'MB',
);
byId('min-size-unit-slot', HTMLSpanElement).append(unitMenu.root);

function fieldOf(target: EventTarget | null): RulesFormField | undefined {
  if (target === enabledEl) return 'enabled';
  if (target === extensionsEl) return 'extensions';
  if (target === minSizeEl) return 'minSize';
  if (target === hostsEl) return 'excludedHosts';
  return undefined;
}

function readForm(): RulesForm {
  return {
    enabled: enabledEl.checked,
    extensions: extensionsEl.value,
    minSize: minSizeEl.value,
    minSizeUnit: unitMenu.value,
    excludedHosts: hostsEl.value,
  };
}

/** Writes the values that differ (an unchanged field keeps its caret and selection). */
function writeForm(f: RulesForm): void {
  if (enabledEl.checked !== f.enabled) enabledEl.checked = f.enabled;
  if (extensionsEl.value !== f.extensions) extensionsEl.value = f.extensions;
  if (minSizeEl.value !== f.minSize) minSizeEl.value = f.minSize;
  if (unitMenu.value !== f.minSizeUnit) unitMenu.setValue(f.minSizeUnit);
  if (hostsEl.value !== f.excludedHosts) hostsEl.value = f.excludedHosts;
  renderFormNotes();
}

/** "Off: …" only while the switch is off, and the number of extensions in the field. */
function renderFormNotes(): void {
  enabledHintEl.dataset.state = enabledEl.checked ? 'on' : 'off';
  const n = new Set(extensionsEl.value.split(/[\s,]+/).filter(Boolean)).size;
  extensionsCountEl.textContent = n === 1 ? '1 extension' : `${n} extensions`;
}

function fillForm(rules: CaptureRules): void {
  writeForm(rulesToForm(rules));
  showErrors({});
  edited.clear();
}

function showErrors(errors: Partial<Record<RulesField, string>>): void {
  for (const [field, { input, error }] of Object.entries(FIELDS) as [RulesField, (typeof FIELDS)[RulesField]][]) {
    const message = errors[field];
    error.textContent = message ?? '';
    error.hidden = message === undefined;
    if (message === undefined) input.removeAttribute('aria-invalid');
    else input.setAttribute('aria-invalid', 'true');
  }
  // The unit is part of the size: a refused size marks both halves.
  unitMenu.setInvalid(errors.minSize !== undefined);
}

function setStatus(text: string, tone: 'ok' | 'error' | 'none' = 'none'): void {
  statusEl.textContent = text;
  statusEl.className = tone === 'none' ? 'muted' : `tone-${tone}`;
}

async function save(rules: CaptureRules, message: string): Promise<void> {
  try {
    await browser.storage.local.set({ [RULES_STORAGE_KEY]: rules });
    fillForm(rules);
    setStatus(message, 'ok');
  } catch (e) {
    setStatus(`Could not save: ${errorMessage(e)}`, 'error');
  }
}

form.addEventListener('input', (ev) => {
  const field = fieldOf(ev.target);
  if (field) edited.add(field);
  setStatus('');
  renderFormNotes();
});

form.addEventListener('submit', (ev) => {
  ev.preventDefault();
  const result = parseRulesForm(readForm());
  if (!result.ok) {
    showErrors(result.errors);
    setStatus('Fix the highlighted fields.', 'error');
    const first = (Object.keys(FIELDS) as RulesField[]).find((f) => result.errors[f] !== undefined);
    if (first) FIELDS[first].input.focus();
    return;
  }
  void save(result.rules, 'Saved.');
});

resetBtn.addEventListener('click', () => {
  void save(defaultRules(), 'Defaults restored.');
});

browser.storage.onChanged.addListener((changes, area) => {
  const change = changes[RULES_STORAGE_KEY];
  if (area !== 'local' || !change) return;
  const rules = parseRules(change.newValue);
  if (edited.size === 0) fillForm(rules);
  else writeForm(mergeExternalRules(readForm(), edited, parseRules(change.oldValue), rules));
});

void browser.storage.local
  .get(RULES_STORAGE_KEY)
  .then((got) => fillForm(parseRules(got[RULES_STORAGE_KEY])))
  .catch((e: unknown) => {
    fillForm(defaultRules());
    setStatus(`Could not load the saved rules: ${errorMessage(e)}`, 'error');
  });

// --- The rail marks the section in view ---------------------------------------------------

const railLinks = [...document.querySelectorAll<HTMLAnchorElement>('.rail nav a[href^="#"]')];
const sections = railLinks.map((a) => document.getElementById(a.hash.slice(1))).filter((el): el is HTMLElement => el !== null);
let railFrame = 0;

/** The last section whose top is above 40% of the window (the last one at the page's end). */
function markSection(): void {
  railFrame = 0;
  const atEnd = window.innerHeight + window.scrollY >= document.documentElement.scrollHeight - 2;
  const line = window.innerHeight * 0.4;
  const current = atEnd ? sections.at(-1) : (sections.filter((s) => s.getBoundingClientRect().top <= line).at(-1) ?? sections[0]);
  for (const a of railLinks) {
    if (current && a.hash === `#${current.id}`) a.setAttribute('aria-current', 'true');
    else a.removeAttribute('aria-current');
  }
}

for (const type of ['scroll', 'resize'] as const) {
  window.addEventListener(type, () => (railFrame ||= requestAnimationFrame(markSection)), { passive: true });
}
markSection();

// --- Video options: each switch is saved when it changes ---------------------------------

/** The hidden sites as stored, and whether the field has unsaved edits. */
let storedHiddenSites: string[] = [];
let hiddenSitesEdited = false;

function fillVideoOptions(o: VideoOptions): void {
  videoButtonEl.checked = o.videoButton;
  videoSignInEl.checked = o.useSignInForVideos;
  videoPrefetchEl.checked = o.videoPrefetch;
  // Entries added or removed elsewhere (a panel's "Don't show on", the toolbar menu) reach an
  // edited field too, so saving it does not undo them.
  const text = hiddenSitesField(hiddenSitesEl.value, hiddenSitesEdited, storedHiddenSites, o.hiddenSites);
  if (hiddenSitesEl.value !== text) hiddenSitesEl.value = text;
  storedHiddenSites = o.hiddenSites;
}

function showHiddenSitesError(message: string | undefined): void {
  hiddenSitesErrorEl.textContent = message ?? '';
  hiddenSitesErrorEl.hidden = message === undefined;
  if (message === undefined) hiddenSitesEl.removeAttribute('aria-invalid');
  else hiddenSitesEl.setAttribute('aria-invalid', 'true');
}

async function saveHiddenSites(): Promise<void> {
  if (!hiddenSitesEdited) return;
  const result = hiddenSitesResult(hiddenSitesEl.value);
  if (!result.ok) {
    showHiddenSitesError(result.error);
    setVideoStatus('Fix the highlighted field.', 'error');
    return;
  }
  showHiddenSitesError(undefined);
  try {
    // Read the latest options: a panel or the toolbar menu may have changed them meanwhile.
    const got = await browser.storage.local.get(VIDEO_OPTIONS_KEY);
    await browser.storage.local.set({ [VIDEO_OPTIONS_KEY]: { ...parseVideoOptions(got[VIDEO_OPTIONS_KEY]), hiddenSites: result.sites } });
    hiddenSitesEdited = false;
    storedHiddenSites = result.sites;
    hiddenSitesEl.value = formatHiddenSites(result.sites);
    setVideoStatus(result.note ? `Saved. ${result.note}` : 'Saved.', 'ok');
  } catch (e) {
    setVideoStatus(`Could not save: ${errorMessage(e)}`, 'error');
  }
}

hiddenSitesEl.addEventListener('input', () => {
  hiddenSitesEdited = true;
  showHiddenSitesError(undefined);
  setVideoStatus('The sites are saved when you leave the field.');
});
hiddenSitesEl.addEventListener('change', () => void saveHiddenSites());

function setVideoStatus(text: string, tone: 'ok' | 'error' | 'none' = 'none'): void {
  videoStatusEl.textContent = text;
  videoStatusEl.className = tone === 'none' ? 'muted' : `tone-${tone}`;
}

async function saveVideoOption(change: Partial<VideoOptions>, input: HTMLInputElement): Promise<void> {
  try {
    // Read the latest options: the overlay may have allowed the sign-in meanwhile.
    const got = await browser.storage.local.get(VIDEO_OPTIONS_KEY);
    await browser.storage.local.set({ [VIDEO_OPTIONS_KEY]: { ...parseVideoOptions(got[VIDEO_OPTIONS_KEY]), ...change } });
    setVideoStatus('Saved.', 'ok');
  } catch (e) {
    input.checked = !input.checked;
    setVideoStatus(`Could not save: ${errorMessage(e)}`, 'error');
  }
}

videoButtonEl.addEventListener('change', () => void saveVideoOption({ videoButton: videoButtonEl.checked }, videoButtonEl));
videoSignInEl.addEventListener('change', () => void saveVideoOption({ useSignInForVideos: videoSignInEl.checked }, videoSignInEl));
videoPrefetchEl.addEventListener('change', () => void saveVideoOption({ videoPrefetch: videoPrefetchEl.checked }, videoPrefetchEl));

browser.storage.onChanged.addListener((changes, area) => {
  const change = changes[VIDEO_OPTIONS_KEY];
  if (area === 'local' && change) fillVideoOptions(parseVideoOptions(change.newValue));
});

void browser.storage.local
  .get(VIDEO_OPTIONS_KEY)
  .then((got) => fillVideoOptions(parseVideoOptions(got[VIDEO_OPTIONS_KEY])))
  .catch((e: unknown) => {
    fillVideoOptions(parseVideoOptions(undefined));
    setVideoStatus(`Could not load the video options: ${errorMessage(e)}`, 'error');
  });
