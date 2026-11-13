<script lang="ts">
  // A file name as the user reads it. In a name with right-to-left letters the extension
  // gets its own left-to-right isolate, so a Kurdish or Arabic name ends in ".mp4" instead
  // of "mp4." (the dot otherwise takes the line's right-to-left direction). The stem stays
  // plain text so the caller's dir="auto" still sees its first strong letter and keeps the
  // name's start visible when it is cut. Other names stay one plain text node. The caller
  // escapes bidi controls first (displayName); textContent is always the full name.
  // Only a stem whose last letter is right-to-left needs it: after a Latin word
  // ("\u2026 IMAX.mp4") the extension already joins that word's run, and an isolate would
  // move it to the far left (".mp4IMAX").
  import { splitExtension } from '../text';

  let { name }: { name: string } = $props();
  // Hebrew, Arabic (with Kurdish and Persian letters), Syriac, Thaana, N'Ko and the
  // Hebrew/Arabic presentation forms.
  const RTL = /[\u0590-\u08FF\uFB1D-\uFDFF\uFE70-\uFEFF]/;
  const LETTER = /\p{L}/u;
  // Whether the last letter of s is a right-to-left one (digits and punctuation are skipped).
  function endsRightToLeft(s: string): boolean {
    const chars = Array.from(s);
    for (let i = chars.length - 1; i >= 0; i--) {
      const c = chars[i] ?? '';
      if (RTL.test(c)) return true;
      if (LETTER.test(c)) return false;
    }
    return false;
  }
  const parts = $derived.by(() => {
    if (!RTL.test(name)) return null;
    const p = splitExtension(name);
    return p.ext && endsRightToLeft(p.stem) ? p : null;
  });
</script>

{#if parts?.ext}{parts.stem}<bdi dir="ltr">{parts.ext}</bdi>{:else}{name}{/if}
