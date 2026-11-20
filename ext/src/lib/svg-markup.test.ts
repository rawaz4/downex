// @vitest-environment happy-dom

import { describe, expect, it } from 'vitest';
import { setSvgMarkup } from './svg-markup';

const SVG_NS = 'http://www.w3.org/2000/svg';

describe('setSvgMarkup', () => {
  it('fills an svg with SVG children, replacing what it held', () => {
    const svg = document.createElementNS(SVG_NS, 'svg');
    svg.appendChild(document.createElementNS(SVG_NS, 'rect'));
    setSvgMarkup(svg, '<path d="m7 10 5 5 5-5"/><circle cx="8" cy="15" r="3.5"/>');
    expect(Array.from(svg.children, (c) => c.localName)).toEqual(['path', 'circle']);
    expect(svg.children[0]?.namespaceURI).toBe(SVG_NS);
    expect(svg.children[0]?.getAttribute('d')).toBe('m7 10 5 5 5-5');
  });

  it('leaves the svg empty for markup that is not well-formed', () => {
    const svg = document.createElementNS(SVG_NS, 'svg');
    svg.appendChild(document.createElementNS(SVG_NS, 'rect'));
    setSvgMarkup(svg, '<path d="x">');
    expect(svg.childNodes.length).toBe(0);
  });
});
