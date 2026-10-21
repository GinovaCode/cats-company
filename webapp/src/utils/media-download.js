import { resolveMediaURL } from '../api';

// Cross-origin downloads ignore <a download>. Pass the display name to the
// trusted upload handler so its Content-Disposition can name the saved file.
export function downloadableMediaURL(url, name) {
  if (!url) return '';
  try {
    const urlObj = new URL(url, window.location.origin);
    const mediaBase = new URL(resolveMediaURL('/uploads/'), window.location.origin);
    const uploadRootPath = `${mediaBase.pathname.replace(/\/uploads\/?$/, '')}/uploads/`;
    const isDownloadableUpload = ['files', 'images', 'feedback'].some(
      (directory) => urlObj.pathname.startsWith(`${uploadRootPath}${directory}/`),
    );
    const trustedOrigin = urlObj.origin === window.location.origin
      || urlObj.origin === mediaBase.origin;
    if (!trustedOrigin || !isDownloadableUpload) return url;
    urlObj.searchParams.set('download', '1');
    if (typeof name === 'string' && name.trim()) urlObj.searchParams.set('name', name);
    return /^https?:\/\//i.test(url)
      ? urlObj.toString()
      : `${urlObj.pathname}${urlObj.search}${urlObj.hash}`;
  } catch {
    return url;
  }
}
