// Node shapes and rank badges drawn on a canvas, so the map needs no sprite or glyph server (works offline).
import type { Map as MlMap } from 'maplibre-gl';
import { palette } from '../palette';

type Draw = (c: CanvasRenderingContext2D, s: number) => void;

const shapes: Record<string, Draw> = {
  repeater: (c, s) => { c.moveTo(s / 2, 2); c.lineTo(s - 2, s / 2); c.lineTo(s / 2, s - 2); c.lineTo(2, s / 2); c.closePath(); },
  room: (c, s) => c.rect(4, 4, s - 8, s - 8),
  companion: (c, s) => c.arc(s / 2, s / 2, s / 2 - 6, 0, Math.PI * 2),
  sensor: (c, s) => { c.moveTo(s / 2, 3); c.lineTo(s - 3, s - 4); c.lineTo(3, s - 4); c.closePath(); },
};
shapes.unknown = shapes.sensor!;

const RATIO = 2;

function paint(size: number, draw: (c: CanvasRenderingContext2D) => void): ImageData {
  const canvas = document.createElement('canvas');
  canvas.width = canvas.height = size * RATIO;
  const c = canvas.getContext('2d')!;
  c.scale(RATIO, RATIO);
  draw(c);
  return c.getImageData(0, 0, canvas.width, canvas.height);
}

export async function addIcons(map: MlMap, maxRank: number): Promise<void> {
  for (const [role, draw] of Object.entries(shapes)) {
    for (const obs of [false, true]) {
      const fill = obs ? palette.cyan : role === 'companion' ? palette.inkDim : palette.ink;
      const img = paint(20, (c) => {
        c.beginPath();
        draw(c, 20);
        c.fillStyle = fill;
        c.fill();
        c.lineWidth = 2;
        c.strokeStyle = palette.void;
        c.stroke();
      });
      map.addImage(`node-${role}${obs ? '-obs' : ''}`, img, { pixelRatio: RATIO });
    }
  }
  await document.fonts.load('700 11px "JetBrains Mono"');
  for (let r = 1; r <= maxRank; r++) {
    const img = paint(24, (c) => {
      c.beginPath();
      c.arc(12, 12, 10, 0, Math.PI * 2);
      c.fillStyle = palette.lime;
      c.fill();
      c.lineWidth = 2;
      c.strokeStyle = palette.void;
      c.stroke();
      c.fillStyle = palette.void;
      c.font = '700 11px "JetBrains Mono", monospace';
      c.textAlign = 'center';
      c.textBaseline = 'middle';
      c.fillText(String(r), 12, 12.5);
    });
    map.addImage(`rank-${r}`, img, { pixelRatio: RATIO });
  }
}
