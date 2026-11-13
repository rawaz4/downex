import { render } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import Icon, { ICONS } from './Icon.svelte';

describe('Icon', () => {
  it('draws "system" (the theme that follows the system) as a display, not a bar in a ring like "info"', () => {
    const ring = /a8 8 0 1 1/;
    expect(ICONS.info).toMatch(ring);
    expect(ICONS.system).not.toMatch(ring);
    // a screen (rounded rectangle) on a stand with a foot
    expect(ICONS.system).toMatch(/^M4\.75 4\.75h14\.5/);
    expect(ICONS.system).toContain('M12 16.25v3.5');
    expect(ICONS.system).toContain('M8.5 19.75h7');
    // and it is none of the other screens of the set
    for (const other of ['progress', 'stream'] as const) expect(ICONS.system).not.toBe(ICONS[other]);
  });

  it('is decorative unless labelled', () => {
    const { container } = render(Icon, { props: { name: 'system', size: 14 } });
    const svg = container.querySelector('svg');
    expect(svg?.getAttribute('aria-hidden')).toBe('true');
    expect(svg?.querySelector('path')?.getAttribute('d')).toBe(ICONS.system);
  });
});
