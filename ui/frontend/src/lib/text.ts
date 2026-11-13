// Display safety for strings that come from the network or the daemon (file names,
// URLs, paths, error text). Raw values stay untouched for actions; these helpers are
// for what the user reads.

/**
 * Explicit bidirectional formatting characters: embeddings/overrides U+202A–U+202E,
 * isolates U+2066–U+2069, the marks U+200E/U+200F and the Arabic letter mark U+061C.
 * In a file name they can reorder what is shown ("invoice<U+202E>fdp.exe" reads as
 * "invoiceexe.pdf") and so disguise the real extension.
 */
const BIDI_CONTROLS = /[‪-‮⁦-⁩‎‏؜]/g;
const HAS_BIDI_CONTROLS = /[‪-‮⁦-⁩‎‏؜]/;

export function hasBidiControls(s: string): boolean {
  return HAS_BIDI_CONTROLS.test(s);
}

/** Replaces each bidi control with a visible "⟨U+202E⟩" marker. */
export function escapeBidi(s: string): string {
  if (!HAS_BIDI_CONTROLS.test(s)) return s;
  return s.replace(
    BIDI_CONTROLS,
    (c) => `⟨U+${(c.codePointAt(0) ?? 0).toString(16).toUpperCase().padStart(4, '0')}⟩`,
  );
}

const FSI = '⁨';
const PDI = '⁩';

/**
 * Wraps an inserted value in first-strong isolate … pop directional isolate, so a
 * right-to-left name inside an English sentence (or the reverse) keeps its own order
 * without reordering the words around it. The value is escaped first, so it cannot
 * close the isolate early.
 */
export function isolate(s: string): string {
  return `${FSI}${escapeBidi(s)}${PDI}`;
}

/**
 * Splits a file name into its stem and a trailing Latin extension (".mp4", ".iso"), so a
 * right-to-left name can show its extension in its own left-to-right isolate (".mp4", not
 * "mp4."). Without such an extension the whole name is the stem.
 */
export function splitExtension(name: string): { stem: string; ext: string } {
  const m = /^(.+)(\.[A-Za-z0-9]{1,10})$/s.exec(name);
  return m?.[1] && m[2] ? { stem: m[1], ext: m[2] } : { stem: name, ext: '' };
}
