<script lang="ts">
  // Settings → Appearance (DESIGN-SYSTEM.md §4.9): the theme and the list density. Both are a
  // choice of this viewer, kept in this computer's storage by their stores and applied at once:
  // they are not part of the settings that Save sends to downexd.
  import { theme as themeStore, type ThemeChoice } from '../stores/theme.svelte';
  import { density as densityStore, type DensityChoice } from '../stores/density.svelte';
  import FileIcon from './FileIcon.svelte';
  import Icon from './Icon.svelte';
  import SegmentedControl, { type SegmentOption } from './SegmentedControl.svelte';

  interface Props {
    /** The stores to change (tests give their own). */
    theme?: { choice: ThemeChoice; set(choice: ThemeChoice): void };
    density?: { choice: DensityChoice; set(choice: DensityChoice): void };
  }

  let { theme = themeStore, density = densityStore }: Props = $props();

  const uid = $props.id();

  const THEMES: readonly SegmentOption<ThemeChoice>[] = [
    { value: 'system', label: 'System', icon: 'system' },
    { value: 'light', label: 'Light', icon: 'sun' },
    { value: 'dark', label: 'Dark', icon: 'moon' },
  ];
  const THEME_HINTS: Record<ThemeChoice, string> = {
    system: 'Follows the light or dark setting of your system.',
    light: 'Always light.',
    dark: 'Always dark.',
  };

  const DENSITIES: readonly SegmentOption<DensityChoice>[] = [
    { value: 'comfortable', label: 'Comfortable' },
    { value: 'compact', label: 'Compact' },
  ];
  const DENSITY_HINTS: Record<DensityChoice, string> = {
    comfortable: 'Two lines per download: name, then site and category.',
    compact: 'One line per download, like a classic download manager.',
  };

  /** Sample rows of the preview: what each density shows of a download. */
  const SAMPLES = [
    { kind: 'video', name: 'conference-keynote.mp4', sub: 'youtube.com · Video', size: '3.4 GB' },
    { kind: 'archives', name: 'photo-archive-277.7z', sub: 'mirror.example.org · Archives', size: '2.5 GB' },
  ] as const;

  // The stores are the truth: the controls show and change them.
  const themeChoice = $derived(theme.choice);
  const densityChoice = $derived(density.choice);
</script>

<section class="s-section" aria-labelledby="{uid}-title">
  <div class="s-head">
    <h3 class="s-title" id="{uid}-title">Appearance</h3>
  </div>
  <div class="s-card">
    <div class="s-row">
      <div class="s-text">
        <span class="s-label">Theme</span>
        <p class="s-hint">{THEME_HINTS[themeChoice]}</p>
      </div>
      <div class="s-control">
        <SegmentedControl value={themeChoice} options={THEMES} legend="Theme" onchange={(v) => theme.set(v)} />
      </div>
    </div>
    <div class="s-row">
      <div class="s-text">
        <span class="s-label">List density</span>
        <p class="s-hint">{DENSITY_HINTS[densityChoice]}</p>
      </div>
      <div class="s-control">
        <SegmentedControl value={densityChoice} options={DENSITIES} legend="List density" onchange={(v) => density.set(v)} />
      </div>
      <div class="preview" class:compact={densityChoice === 'compact'} aria-hidden="true">
        {#each SAMPLES as s (s.name)}
          <div class="sample">
            <FileIcon kind={s.kind} size={densityChoice === 'compact' ? 16 : 26} />
            <span class="sample-text">
              <span class="sample-name">{s.name}</span>
              {#if densityChoice === 'comfortable'}<span class="sample-sub">{s.sub}</span>{/if}
            </span>
            <span class="sample-size tnum">{s.size}</span>
          </div>
        {/each}
      </div>
    </div>
  </div>
</section>

<p class="s-note"><Icon name="check" size={14} />Saved on this computer right away.</p>

<style>
  .preview {
    grid-column: 1 / -1;
    display: grid;
    margin-block: 6px 4px;
    border: 1px solid var(--line);
    border-radius: var(--r-md);
    background: var(--surface);
    overflow: hidden;
  }
  .sample {
    display: flex;
    align-items: center;
    gap: 11px;
    block-size: 46px;
    padding-inline: 12px;
    transition: block-size var(--dur-3) var(--ease-out);
  }
  .sample + .sample {
    border-block-start: 1px solid var(--line-soft);
  }
  .compact .sample {
    gap: 8px;
    block-size: 24px;
  }
  .sample-text {
    display: grid;
    flex: 1 1 auto;
    min-inline-size: 0;
  }
  .sample-name {
    overflow: hidden;
    color: var(--ink);
    font-size: var(--type-body);
    font-weight: 500;
    line-height: 1.3;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .compact .sample-name {
    font-weight: 400;
  }
  .sample-sub {
    color: var(--faint);
    font-size: var(--type-meta);
    line-height: 1.3;
  }
  .sample-size {
    color: var(--faint);
    font-size: var(--type-body);
  }
  @media (prefers-reduced-motion: reduce) {
    .sample {
      transition: none;
    }
  }
</style>
