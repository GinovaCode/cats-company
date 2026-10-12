import { describe, expect, it } from 'vitest';
import { imageFitScale, imagePointAt, imageScrollAt, imageViewGeometry } from './image-viewport';

describe('image viewport geometry', () => {
  const image = { width: 2400, height: 1200 };
  const viewport = { width: 800, height: 600 };

  it('fits large images without enlarging small ones', () => {
    expect(imageFitScale(image, viewport)).toBeCloseTo(1 / 3);
    expect(imageFitScale({ width: 240, height: 120 }, viewport)).toBe(1);
  });

  it('keeps a zoom anchor on the same source pixel', () => {
    const scale = 1;
    const anchor = { x: 400, y: 300 };
    const point = imagePointAt(image, viewport, scale, { left: 800, top: 300 }, anchor);
    const next = imageScrollAt(image, viewport, scale * 2, point, anchor);
    const after = imagePointAt(image, viewport, scale * 2, next, anchor);
    expect(after.x).toBeCloseTo(point.x);
    expect(after.y).toBeCloseTo(point.y);
  });

  it('creates scrollable stage bounds only when the image exceeds the viewport', () => {
    expect(imageViewGeometry(image, viewport, 1)).toMatchObject({ width: 2400, height: 1200, stageWidth: 2400, stageHeight: 1200 });
    expect(imageViewGeometry({ width: 240, height: 120 }, viewport, 1)).toMatchObject({ width: 240, height: 120, stageWidth: 800, stageHeight: 600 });
  });
});
