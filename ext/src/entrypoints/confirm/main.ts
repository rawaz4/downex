// Confirmation for "Download all links with Downex" when a page has more than
// CONFIRM_THRESHOLD links (BROWSER-SPEC §4). Opened by the background with ?batch=<id>.

import { mountBrandMarks } from '../../assets/brand';
import { errorMessage } from '../../lib/protocol';
import type { LinksBatchResponse, LinksConfirmResponse } from '../../lib/messages';
import { ask } from '../../lib/runtime';
import { FILE_TILE_AUDIO_SMALL, FILE_TILES, SMALL_TILE_PX, TILE_VIEWBOX, tileKind } from '../popup/popup-view';
import { linkRow } from './links';
import { setSvgMarkup } from '../../lib/svg-markup';

const SVG_NS = 'http://www.w3.org/2000/svg';
const TILE_PX = 16;

/** One link: its file's tile, name and server; the full address in the tooltip. */
function linkItem(url: string): HTMLLIElement {
  const row = linkRow(url);
  const kind = tileKind(row.kind);
  const tile = document.createElementNS(SVG_NS, 'svg');
  tile.setAttribute('viewBox', TILE_VIEWBOX);
  tile.setAttribute('width', String(TILE_PX));
  tile.setAttribute('height', String(TILE_PX));
  tile.setAttribute('class', 'tile');
  tile.setAttribute('aria-hidden', 'true');
  tile.setAttribute('focusable', 'false');
  tile.style.setProperty('--hue', `var(--c-${kind})`);
  tile.style.setProperty('--deep', `var(--c-${kind}-deep)`);
  setSvgMarkup(tile, kind === 'audio' && TILE_PX <= SMALL_TILE_PX ? FILE_TILE_AUDIO_SMALL : FILE_TILES[kind]);
  const name = document.createElement('span');
  name.className = 'name';
  name.dir = 'auto';
  name.textContent = row.name || url;
  const host = document.createElement('span');
  host.className = 'host';
  host.textContent = row.host;
  const li = document.createElement('li');
  li.title = url;
  li.append(tile, name, host);
  return li;
}

function byId<T extends HTMLElement>(id: string, type: new () => T): T {
  const el = document.getElementById(id);
  if (!(el instanceof type)) throw new Error(`missing #${id}`);
  return el;
}

const summaryEl = byId('summary', HTMLParagraphElement);
const sourceEl = byId('source', HTMLParagraphElement);
const linksEl = byId('links', HTMLUListElement);
const acceptBtn = byId('accept', HTMLButtonElement);
const cancelBtn = byId('cancel', HTMLButtonElement);

mountBrandMarks(document);

const batchId = new URLSearchParams(location.search).get('batch') ?? '';
let answered = false;

function finish(text: string, tone: 'ok' | 'error' | 'none'): void {
  answered = true;
  summaryEl.textContent = text;
  summaryEl.className = tone === 'none' ? '' : `tone-${tone}`;
  acceptBtn.hidden = true;
  cancelBtn.textContent = 'Close';
  cancelBtn.disabled = false;
  cancelBtn.focus();
}

function describe(r: LinksConfirmResponse): [string, 'ok' | 'error' | 'none'] {
  switch (r.status) {
    case 'expired':
      return ['This request has expired. Choose "Download all links with Downex" again.', 'error'];
    case 'cancelled':
      return ['Cancelled.', 'none'];
    case 'done': {
      const parts = [`Sent ${r.sent} of ${r.sent + r.failed + r.skipped} links to Downex.`];
      if (r.failed > 0) parts.push(`${r.failed} failed.`);
      if (r.stoppedBy) parts.push(`Stopped: ${r.stoppedBy.message}`);
      return [parts.join(' '), r.failed > 0 || r.skipped > 0 ? 'error' : 'ok'];
    }
  }
}

async function answer(accept: boolean): Promise<void> {
  acceptBtn.disabled = true;
  cancelBtn.disabled = true;
  if (accept) summaryEl.textContent = 'Sending links to Downex…';
  try {
    const result = await ask({ type: 'downex:links-confirm', batchId, accept });
    if (!accept) {
      window.close();
      return;
    }
    finish(...describe(result));
  } catch (e) {
    finish(`Could not reach the extension: ${errorMessage(e)}`, 'error');
  }
}

async function load(): Promise<void> {
  let batch: LinksBatchResponse;
  try {
    batch = batchId ? await ask({ type: 'downex:links-batch', batchId }) : null;
  } catch (e) {
    finish(`Could not reach the extension: ${errorMessage(e)}`, 'error');
    return;
  }
  if (!batch) {
    finish('This request has expired. Choose "Download all links with Downex" again.', 'error');
    return;
  }
  const n = batch.links.length;
  summaryEl.textContent = `Send ${n} links to Downex?`;
  if (batch.referrer) sourceEl.textContent = `From ${batch.referrer}`;
  linksEl.replaceChildren(...batch.links.map(linkItem));
  linksEl.hidden = false;
  acceptBtn.textContent = `Download ${n} links`;
  acceptBtn.disabled = false;
  acceptBtn.focus();
}

acceptBtn.addEventListener('click', () => void answer(true));
cancelBtn.addEventListener('click', () => {
  if (answered) window.close();
  else void answer(false);
});
document.addEventListener('keydown', (ev) => {
  if (ev.key === 'Escape' && !cancelBtn.disabled) cancelBtn.click();
});

void load();
