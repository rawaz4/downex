const SVG_NS = 'http://www.w3.org/2000/svg';

/**
 * Fills svg with the children that markup (the extension's own constant icon drawings, never
 * page content) describes. It parses them as SVG with DOMParser rather than as HTML markup,
 * so nothing is ever parsed as HTML and no script can run.
 */
export function setSvgMarkup(svg: SVGElement, markup: string): void {
  const doc = new DOMParser().parseFromString(`<svg xmlns="${SVG_NS}">${markup}</svg>`, 'image/svg+xml');
  const root = doc.documentElement;
  if (root.namespaceURI !== SVG_NS || root.getElementsByTagName('parsererror').length > 0) {
    svg.replaceChildren();
    return;
  }
  svg.replaceChildren(...Array.from(root.childNodes, (n) => svg.ownerDocument.importNode(n, true)));
}
