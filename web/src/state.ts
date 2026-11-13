export interface Layers {
  nodes: boolean;
  links: boolean;
  linkColor: 'snr' | 'residual';
  spofs: boolean;
  placement: boolean;
  predicted: boolean;
  coverage: boolean;
  coverageMargin: number; // dB above the decode floor that counts as covered
}

export interface View {
  is3D: boolean;
  exaggeration: number;
}

export type Selection = { kind: 'link'; a: string; b: string } | { kind: 'node'; id: string } | null;

export const initialLayers: Layers = {
  nodes: true,
  links: true,
  linkColor: 'snr',
  spofs: true,
  placement: true,
  predicted: false,
  coverage: false,
  coverageMargin: 5, // placement's LINK_MARGIN_DB: the fade margin it asks of a new link
};
