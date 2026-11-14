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

// A camera move the map should make; n changes on every request, so picking the same city twice flies again.
export interface FlyTo {
  lon: number;
  lat: number;
  zoom: number;
  n: number;
}

// The contiguous US, for the "whole US" pick and a map with no data yet.
export const US_VIEW = { lon: -98.6, lat: 39.5, zoom: 3.4 };

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
