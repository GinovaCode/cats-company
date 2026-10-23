import React from 'react';
import '../css/history-loading-state.css';
import useLoadingHandoff from '../utils/use-loading-handoff';

export default function HistoryLoadingState() {
  const ref = useLoadingHandoff();
  return (
    <div ref={ref} className="v3-history-state cc-history-loading" role="status" aria-live="polite">
      <div className="cc-history-loading-bubbles" aria-hidden="true">
        {[0, 1, 2].map(index => (
          <div className={`cc-history-loading-bubble bubble-${index}`} key={index}>
            <i /><i />
          </div>
        ))}
      </div>
      <span>正在读取对话<span className="cc-history-loading-dots" aria-hidden="true">···</span></span>
    </div>
  );
}
