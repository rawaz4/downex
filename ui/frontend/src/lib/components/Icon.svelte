<script lang="ts" module>
  // Original line icons drawn for Downex on a 24×24 grid (stroke = currentColor, 1.6 wide,
  // round caps and joins; `resume`, `resume-all` and `play` are filled). DESIGN-SYSTEM.md §2.6.1.
  const PAGE = 'M6.5 3.5h7l4 4v12.25a.75.75 0 0 1-.75.75H6.5a.75.75 0 0 1-.75-.75V4.25a.75.75 0 0 1 .75-.75zM13.5 3.5v4h4';

  /** An eight-tooth gear with its hub, computed once. */
  function gear(): string {
    const teeth = 8;
    const outer = 9.2;
    const inner = 7.3;
    const pts: string[] = [];
    for (let k = 0; k < teeth; k++) {
      const a = (k / teeth) * Math.PI * 2;
      const w = (Math.PI * 2) / teeth;
      const seq: [number, number][] = [
        [a - w * 0.5, inner],
        [a - w * 0.22, inner],
        [a - w * 0.14, outer],
        [a + w * 0.14, outer],
        [a + w * 0.22, inner],
      ];
      for (const [angle, r] of seq) {
        pts.push(`${pts.length === 0 ? 'M' : 'L'}${(12 + r * Math.cos(angle)).toFixed(2)} ${(12 + r * Math.sin(angle)).toFixed(2)}`);
      }
    }
    return `${pts.join('')}ZM12 9a3 3 0 1 1 0 6 3 3 0 0 1 0-6z`;
  }

  export const ICONS = {
    /** Retired from brand use (BrandMark is the logo); kept until the legacy cleanup. */
    logo: 'M6.5 5.5l5.5 5.5 5.5-5.5M6.5 11.5l5.5 5.5 5.5-5.5M5 20.5h14',
    add: 'M12 5v14M5 12h14',
    resume: 'M8.5 5.75v12.5a.5.5 0 0 0 .77.42l9.4-6.25a.5.5 0 0 0 0-.84l-9.4-6.25a.5.5 0 0 0-.77.42z',
    pause: 'M8.5 5.5v13M15.5 5.5v13',
    'pause-all': 'M6 6.5v11M10.5 6.5v11M15 6.5v11M19.5 6.5v11',
    'resume-all': 'M4.5 6.75v10.5a.5.5 0 0 0 .78.42L12 13.1V17.25a.5.5 0 0 0 .78.42l7.5-5.25a.5.5 0 0 0 0-.84l-7.5-5.25a.5.5 0 0 0-.78.42v4.15L5.28 6.33a.5.5 0 0 0-.78.42z',
    confirm: 'M5 12.5l4.5 4.5L19 7.5',
    remove:
      'M4.5 7h15M9.5 7V4.75h5V7M6.5 7l.9 12.1a1.5 1.5 0 0 0 1.5 1.4h6.2a1.5 1.5 0 0 0 1.5-1.4L17.5 7M10.25 10.5v6.5M13.75 10.5v6.5',
    /** Sliders (manage queues, options). The gear is `gear`. */
    settings:
      'M4 7.5h8M16.5 7.5H20M4 16.5h3.5M12 16.5h8M14.25 5.25a2.25 2.25 0 1 1 0 4.5 2.25 2.25 0 0 1 0-4.5zM9.75 14.25a2.25 2.25 0 1 1 0 4.5 2.25 2.25 0 0 1 0-4.5z',
    sun: 'M12 8a4 4 0 1 1 0 8 4 4 0 0 1 0-8zM12 2.75v2M12 19.25v2M2.75 12h2M19.25 12h2M5.45 5.45l1.4 1.4M17.15 17.15l1.4 1.4M5.45 18.55l1.4-1.4M17.15 6.85l1.4-1.4',
    moon: 'M19.5 14.25A7.75 7.75 0 0 1 9.75 4.5a7.75 7.75 0 1 0 9.75 9.75z',
    /** The theme that follows the system: a display on a stand (a ring with a bar read as "!" or "i"). */
    system: 'M4.75 4.75h14.5a1 1 0 0 1 1 1v9.5a1 1 0 0 1-1 1H4.75a1 1 0 0 1-1-1v-9.5a1 1 0 0 1 1-1zM12 16.25v3.5M8.5 19.75h7',
    search: 'M10.75 4.5a6.25 6.25 0 1 1 0 12.5 6.25 6.25 0 0 1 0-12.5zM15.25 15.25l4.25 4.25',
    folder:
      'M3.75 7.25a1.5 1.5 0 0 1 1.5-1.5h3.9l1.9 2h7.7a1.5 1.5 0 0 1 1.5 1.5v8.5a1.5 1.5 0 0 1-1.5 1.5H5.25a1.5 1.5 0 0 1-1.5-1.5z',
    open: 'M13.5 4.5h6v6M19.5 4.5l-8 8M17.5 13.5v5a1.5 1.5 0 0 1-1.5 1.5H6a1.5 1.5 0 0 1-1.5-1.5V8A1.5 1.5 0 0 1 6 6.5h5',
    copy: 'M9 8.5h9.5a1 1 0 0 1 1 1V19a1 1 0 0 1-1 1H9a1 1 0 0 1-1-1V9.5a1 1 0 0 1 1-1zM15.5 8.5V5a1 1 0 0 0-1-1H5a1 1 0 0 0-1 1v9.5a1 1 0 0 0 1 1h3',
    restart: 'M4.75 12a7.25 7.25 0 1 0 2.2-5.2M4.5 4.5v4h4',
    close: 'M6.5 6.5l11 11M17.5 6.5l-11 11',
    alert: 'M12 4.25l8.5 15H3.5zM12 10v4.25M12 16.75v.25',
    link: 'M10 14l4-4M8.5 11.5l-2 2a3 3 0 0 0 4.25 4.25l2-2M15.5 12.5l2-2a3 3 0 0 0-4.25-4.25l-2 2',
    /** A download's progress window: a small window with a part-filled bar. */
    progress: 'M4.75 5.25h14.5a1 1 0 0 1 1 1v11.5a1 1 0 0 1-1 1H4.75a1 1 0 0 1-1-1V6.25a1 1 0 0 1 1-1zM3.75 9h16.5M7 13.75h10M7 13.75h5.5v.01',
    /** A streaming-media download: a screen of fragments being played. */
    stream: 'M4.75 5.5h14.5a1 1 0 0 1 1 1v9a1 1 0 0 1-1 1H4.75a1 1 0 0 1-1-1v-9a1 1 0 0 1 1-1zM10.25 8.5v5l4.25-2.5zM4.5 19.5h3M10.5 19.5h3M16.5 19.5h3',
    /** Retired outline file glyphs: FileIcon draws the file kinds now. */
    video: `${PAGE}M10.25 11.5v5l4-2.5z`,
    audio: `${PAGE}M13.75 10.25v5.5M13.75 10.25l-3 1M9.5 15.75a1.25 1.25 0 1 0 2.5 0 1.25 1.25 0 0 0-2.5 0zM12.5 15.75a1.25 1.25 0 1 0 2.5 0 1.25 1.25 0 0 0-2.5 0z`,
    archives: `${PAGE}M11.25 8.5h1.5M11.25 11h1.5M11.25 13.5h1.5M10.75 16h2.5v2.25h-2.5z`,
    documents: `${PAGE}M8.75 12h6.5M8.75 14.75h6.5M8.75 17.5h4`,
    programs: `${PAGE}M9 12.5l2 1.75L9 16M12 17h3`,
    other: `${PAGE}M12 14.5v.25`,

    // ---- Studio additions (§2.6.1) ----
    /** All downloads. */
    tray: 'M4.5 13.5v4a2 2 0 0 0 2 2h11a2 2 0 0 0 2-2v-4M12 4.5v10M7.5 10l4.5 4.5 4.5-4.5',
    queue: 'M4.5 6.5h15M4.5 12h15M4.5 17.5h9',
    clock: 'M12 4.5a7.5 7.5 0 1 1 0 15 7.5 7.5 0 0 1 0-15zM12 8v4l2.8 1.8',
    /** Speed limit. */
    gauge: 'M4.6 17a8 8 0 1 1 14.8 0M12 15.5l3.6-4.4M12 15.5h.01',
    /** When done. */
    power: 'M12 4v7M7.4 6.9a7 7 0 1 0 9.2 0',
    /** Free space. */
    drive: 'M4 13.5l2.2-7.1A1.5 1.5 0 0 1 7.6 5.4h8.8a1.5 1.5 0 0 1 1.4 1l2.2 7.1M4 13.5h16v4a1.5 1.5 0 0 1-1.5 1.5h-13A1.5 1.5 0 0 1 4 17.5zM16.5 16.25h.01',
    /** A site or an address. */
    globe: 'M12 4a8 8 0 1 1 0 16 8 8 0 0 1 0-16zM4.5 12h15M12 4c2.2 2.3 3.2 5 3.2 8s-1 5.7-3.2 8c-2.2-2.3-3.2-5-3.2-8s1-5.7 3.2-8z',
    history: 'M4.75 12a7.25 7.25 0 1 0 2.2-5.2M4.5 4.5v4h4M12 8.5V12l2.5 1.5',
    filter: 'M4.5 6h15M7.5 12h9M10.5 18h3',
    /** Three dots (drawn 2.6 wide). */
    more: 'M6 12h.01M12 12h.01M18 12h.01',
    'chevron-down': 'M7 10l5 5 5-5',
    'chevron-up': 'M7 14l5-5 5 5',
    'chevron-right': 'M10 7l5 5-5 5',
    'chevron-left': 'M14 7l-5 5 5 5',
    check: 'M5 12.5l4.5 4.5L19 7.5',
    external: 'M13.5 4.5h6v6M19.5 4.5l-8 8M17.5 13.5v5a1.5 1.5 0 0 1-1.5 1.5H6a1.5 1.5 0 0 1-1.5-1.5V8A1.5 1.5 0 0 1 6 6.5h5',
    paste: 'M9 4h6v3H9zM15 5.5h2A1.5 1.5 0 0 1 18.5 7v11.5A1.5 1.5 0 0 1 17 20H7a1.5 1.5 0 0 1-1.5-1.5V7A1.5 1.5 0 0 1 7 5.5h2',
    refresh: 'M19.25 12a7.25 7.25 0 1 1-2.2-5.2M19.5 4.5v4h-4',
    /** No cookies sent. */
    shield: 'M12 3.5l7 2.6v5.4c0 4.3-2.9 7.6-7 9-4.1-1.4-7-4.7-7-9V6.1z',
    play: 'M8 5.9v12.2a.7.7 0 0 0 1.08.59l9.2-6.1a.7.7 0 0 0 0-1.18l-9.2-6.1A.7.7 0 0 0 8 5.9z',
    /** List density (Appearance). */
    density: 'M4.5 6h15M4.5 10h15M4.5 14h15M4.5 18h15',
    /** The details pane: a window with a lower panel. */
    'sidebar-panel': 'M4.75 5.25h14.5a1 1 0 0 1 1 1v11.5a1 1 0 0 1-1 1H4.75a1 1 0 0 1-1-1V6.25a1 1 0 0 1 1-1zM3.75 13.5h16.5',
    /** Window controls: drawn at 20 px, the glyph spans 10 px of the 46×44 button. */
    'win-minimise': 'M6 12h12',
    'win-maximise': 'M6.5 6.5h11v11h-11z',
    'win-restore': 'M8.5 8.5h9v9h-9zM6.5 15.5v-9h9',
    'win-close': 'M6.5 6.5l11 11M17.5 6.5l-11 11',
    /** Settings (title bar, General page). */
    gear: gear(),
    'arrow-down': 'M12 5v13.5M6.5 13L12 18.5l5.5-5.5',
    /** Integration (browser and relay). */
    plug: 'M9 3.5v4M15 3.5v4M7 7.5h10v3a5 5 0 0 1-10 0zM12 15.5v5',
    info: 'M12 4a8 8 0 1 1 0 16 8 8 0 0 1 0-16zM12 11v5M12 8v.01',
    /** The audio track of a video. */
    headphones: 'M4.5 15v-3a7.5 7.5 0 0 1 15 0v3M4.5 14.5h2.5a1 1 0 0 1 1 1V19a1 1 0 0 1-1 1H5.5a1 1 0 0 1-1-1zM19.5 14.5H17a1 1 0 0 0-1 1V19a1 1 0 0 0 1 1h1a1 1 0 0 0 1-1z',
    calendar: 'M5.5 6.5h13a1 1 0 0 1 1 1v11a1 1 0 0 1-1 1h-13a1 1 0 0 1-1-1v-11a1 1 0 0 1 1-1zM4.5 10.5h15M8.5 4.5v4M15.5 4.5v4',
    'sort-down': 'M12 6v12M7.5 13.5L12 18l4.5-4.5',
    'sort-up': 'M12 18V6M7.5 10.5L12 6l4.5 4.5',
  } as const;

  export type IconName = keyof typeof ICONS;

  const FILLED: ReadonlySet<IconName> = new Set<IconName>(['resume', 'resume-all', 'play']);

  /** Icons drawn wider or thinner than 1.6 (in grid units). */
  const STROKE: Partial<Record<IconName, number>> = {
    more: 2.6,
    'win-minimise': 1.2,
    'win-maximise': 1.2,
    'win-restore': 1.2,
    'win-close': 1.3,
  };
</script>

<script lang="ts">
  interface Props {
    name: IconName;
    size?: number;
    /** Accessible label; when omitted the icon is decorative. */
    label?: string;
  }

  let { name, size = 18, label }: Props = $props();
  const filled = $derived(FILLED.has(name));
</script>

<svg
  class="icon"
  width={size}
  height={size}
  viewBox="0 0 24 24"
  fill={filled ? 'currentColor' : 'none'}
  stroke="currentColor"
  stroke-width={STROKE[name] ?? 1.6}
  stroke-linecap="round"
  stroke-linejoin="round"
  role={label ? 'img' : undefined}
  aria-label={label}
  aria-hidden={label ? undefined : 'true'}
  focusable="false"
>
  <path d={ICONS[name]} />
</svg>

<style>
  .icon {
    flex: none;
    display: block;
  }
</style>
