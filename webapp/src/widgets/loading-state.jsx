import React from 'react';
import { Globe, Laptop } from 'lucide-react';
import '../css/loading-state.css';
import useLoadingHandoff from '../utils/use-loading-handoff';

function LoadingMotion({ kind }) {
  if (kind === 'skills') return <div className="cc-load-modules">{[0, 1, 2, 3].map(index => <span className={`cc-load-module piece-${index}`} key={index}><i /><i /></span>)}</div>;
  if (kind === 'apps') return <div className="cc-load-window-stack">{[0, 1, 2].map(index => <span className={`cc-load-stack-sheet sheet-${index}`} key={index}><span className="cc-load-stack-bar"><i /><i /><i /></span><span className="cc-load-stack-content"><i /><i /></span></span>)}</div>;
  if (kind === 'image') return <div className="cc-load-picture">{[0, 1, 2, 3, 4, 5].map(index => <span key={index} />)}</div>;
  if (kind === 'connect') return <div className="cc-load-connection"><span className="cc-load-endpoint"><Globe size={25} /></span><span className="cc-load-connection-dots"><i /><i /><i /></span><span className="cc-load-endpoint cc-load-computer"><Laptop size={28} /></span></div>;
  if (kind === 'tasks') return <div className="cc-load-task-lines">{[0, 1, 2].map(index => <span key={index}><i /><b /><b /></span>)}</div>;
  return <div className="cc-load-document"><span className="cc-load-document-fold" /><i /><i /><i /><i /></div>;
}

export default function LoadingState({ kind, label, copy, compact = false, className = '', announce = true }) {
  const ref = useLoadingHandoff();
  return (
    <div ref={ref} className={`cc-loading-state${compact ? ' is-compact' : ''} ${className}`} role={announce ? 'status' : undefined}>
      <div className="cc-loading-motion" data-loading-kind={kind} aria-hidden="true"><LoadingMotion kind={kind} /></div>
      {label && <span className="cc-loading-label">{label}<span aria-hidden="true"> ···</span></span>}
      {copy && <span className="cc-loading-copy">{copy}</span>}
    </div>
  );
}
