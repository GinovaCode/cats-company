import React, { useCallback, useEffect, useLayoutEffect, useMemo, useRef } from 'react';
import { HTML_PREVIEW_SANDBOX } from './controlled-artifact-preview';
import { connectHTMLPreview, htmlPreviewDocument, htmlPreviewReady, setHTMLPreviewMode } from '../utils/html-preview-bridge';

// Keep one opaque document alive while annotation mode changes, including its
// styles, application state and scroll position. Only pointer routing changes.
export default function HTMLFilePreview({ html, annotationMode, annotatable }) {
  const frameRef = useRef(null);
  const connectionRef = useRef(null);
  const modeRef = useRef(annotationMode);
  modeRef.current = annotationMode;
  const document = useMemo(() => {
    const nonce = crypto.randomUUID();
    return { nonce, html: annotatable ? htmlPreviewDocument(html, nonce) : html };
  }, [html, annotatable]);
  const connect = useCallback(() => {
    connectionRef.current?.dispose();
    if (annotatable && typeof MessageChannel === 'function') {
      connectionRef.current = connectHTMLPreview(frameRef.current, document.nonce, modeRef.current);
    }
  }, [annotatable, document]);
  useLayoutEffect(() => {
    if (!annotatable) return undefined;
    const ready = event => { if (htmlPreviewReady(event, frameRef.current, document.nonce)) connect(); };
    window.addEventListener('message', ready);
    return () => window.removeEventListener('message', ready);
  }, [annotatable, connect, document]);
  useEffect(() => { setHTMLPreviewMode(frameRef.current, annotationMode); }, [annotationMode]);
  useEffect(() => () => { connectionRef.current?.dispose(); }, [document]);
  return (
    <iframe ref={frameRef} className="v3-file-preview-frame" title="HTML Report Preview"
      sandbox={HTML_PREVIEW_SANDBOX} referrerPolicy="no-referrer" srcDoc={document.html}
      data-file-annotation-surface={annotatable ? 'html' : undefined}
      onLoad={connect} />
  );
}
