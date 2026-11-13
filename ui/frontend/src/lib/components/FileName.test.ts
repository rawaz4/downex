import { render } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import FileName from './FileName.svelte';

describe('FileName', () => {
  it('gives the extension of a right-to-left name its own left-to-right isolate', () => {
    const { container } = render(FileName, { props: { name: 'وزە بۆ کردنی کارێک ؟.mp4' } });
    const ext = container.querySelector('bdi');
    expect(ext?.getAttribute('dir')).toBe('ltr');
    expect(ext?.textContent).toBe('.mp4');
    // The stem stays plain text, so the row's dir="auto" still finds its first strong letter.
    expect(ext?.previousSibling?.nodeType).toBe(Node.TEXT_NODE);
    expect(ext?.previousSibling?.textContent).toBe('وزە بۆ کردنی کارێک ؟');
    expect(container.textContent).toBe('وزە بۆ کردنی کارێک ؟.mp4');
  });

  it('leaves other names as one plain text node', () => {
    for (const name of [
      'Win.10.Aio.19045.7807.Multi.iso',
      'ڕاپۆرت',
      'README',
      // A Latin word before the extension: it already keeps ".mp4" next to it.
      'سلێمانی تا بەغداد بۆ بینینی The Odyssey و IMAX.mp4',
    ]) {
      const { container, unmount } = render(FileName, { props: { name } });
      expect(container.querySelector('bdi')).toBeNull();
      expect(container.textContent).toBe(name);
      unmount();
    }
  });
});
