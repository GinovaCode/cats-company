export function imageFitScale(image, viewport) {
  if (!image.width || !image.height || !viewport.width || !viewport.height) return 1;
  return Math.min(1, viewport.width / image.width, viewport.height / image.height);
}

// A real layout canvas gives oversized images positive, reachable scroll bounds.
export function imageViewGeometry(image, viewport, scale) {
  const width = image.width * scale, height = image.height * scale;
  return { width, height, stageWidth: Math.max(viewport.width, width),
    stageHeight: Math.max(viewport.height, height),
    left: Math.max(0, (viewport.width - width) / 2),
    top: Math.max(0, (viewport.height - height) / 2) };
}

export function imagePointAt(image, viewport, scale, scroll, anchor) {
  const geometry = imageViewGeometry(image, viewport, scale);
  return { x: (scroll.left + anchor.x - geometry.left) / scale,
    y: (scroll.top + anchor.y - geometry.top) / scale };
}

export function imageScrollAt(image, viewport, scale, point, anchor) {
  const geometry = imageViewGeometry(image, viewport, scale);
  return {
    left: Math.max(0, Math.min(geometry.stageWidth - viewport.width, geometry.left + point.x * scale - anchor.x)),
    top: Math.max(0, Math.min(geometry.stageHeight - viewport.height, geometry.top + point.y * scale - anchor.y)),
  };
}
