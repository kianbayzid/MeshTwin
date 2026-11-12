// The one palette. CSS reads it as custom properties (applyPalette); map layers import it directly.
// Accents carry meaning: cyan = observed / live, magenta = predicted-only or surprising,
// lime = healthy / recommended, amber = warning / single point of failure.
export const palette = {
  void: '#05070a',
  panel: '#0b0f14',
  raised: '#121922',
  line: '#1e2833',
  ink: '#d8dfe7',
  inkDim: '#8d9aaa',
  neutral: '#5d6875',
  cyan: '#2ee6ff',
  cyanDim: '#1b6f80',
  magenta: '#ff5adf',
  lime: '#a8ff52',
  amber: '#ffb020',
} as const;

export type PaletteKey = keyof typeof palette;

const kebab = (s: string): string => s.replace(/[A-Z]/g, (c) => `-${c.toLowerCase()}`);

export function applyPalette(root: HTMLElement = document.documentElement): void {
  for (const [k, v] of Object.entries(palette)) root.style.setProperty(`--${kebab(k)}`, v);
}

// Text pairings the UI actually uses: [foreground, background]. scripts/check-contrast.ts enforces WCAG AA (4.5:1).
export const textPairs: ReadonlyArray<readonly [PaletteKey, PaletteKey]> = [
  ['ink', 'void'], ['ink', 'panel'], ['ink', 'raised'],
  ['inkDim', 'panel'], ['inkDim', 'raised'], ['inkDim', 'void'],
  ['cyan', 'panel'], ['magenta', 'panel'], ['lime', 'panel'], ['amber', 'panel'],
  ['cyan', 'raised'], ['magenta', 'raised'], ['lime', 'raised'], ['amber', 'raised'],
  ['void', 'lime'], ['void', 'amber'], ['void', 'cyan'],
];
