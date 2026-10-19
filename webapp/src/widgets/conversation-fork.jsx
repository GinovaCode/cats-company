import React, { useEffect, useRef, useState } from 'react';
import { Copy, ExternalLink, Info, LoaderCircle, MoreHorizontal, RefreshCw } from 'lucide-react';
import { describeForkError } from '../utils/conversation-fork';
import '../css/conversation-fork.css';

export function ConversationForkAction({ eligible, busy, pending, onCreate }) {
  const [open, setOpen] = useState(false);
  const root = useRef(null);
  const trigger = useRef(null);
  const item = useRef(null);
  useEffect(() => {
    if (!open) return undefined;
    item.current?.focus();
    const outside = event => { if (!root.current?.contains(event.target)) setOpen(false); };
    document.addEventListener('pointerdown', outside);
    return () => document.removeEventListener('pointerdown', outside);
  }, [open]);
  if (!eligible) return null;
  return (
    <div ref={root} className="cc-fork-menu" onKeyDown={event => {
      if (event.key === 'Escape') { setOpen(false); trigger.current?.focus(); }
      if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) { event.preventDefault(); item.current?.focus(); }
      if (event.key === 'Tab') setOpen(false);
    }}>
      <button ref={trigger} type="button" className="v3-action-btn" aria-label="会话操作" title="会话操作"
        aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen(value => !value)}>
        <MoreHorizontal size={18} aria-hidden="true" />
      </button>
      {open && <div className="cc-fork-menu-items" role="menu" aria-label="会话操作">
        <button ref={item} type="button" role="menuitem" disabled={busy || pending} onClick={() => {
          setOpen(false); trigger.current?.focus(); void onCreate({ newRequest: true });
        }}><Copy size={16} aria-hidden="true" /><span>复制为新会话</span></button>
        <p className="cc-fork-help" role="note">历史各自独立，仍共用同一 Agent、设置和工作目录；不是 Git worktree。</p>
      </div>}
    </div>
  );
}

export function ConversationForkNotice({ fork, canCreate = false }) {
  const { operation, provenance, busy, error, targetError, checking, blocked } = fork;
  return (
    <>
      {provenance && <div className="cc-fork-notice cc-fork-provenance" role="note" aria-label="会话来源">
        <Info size={15} aria-hidden="true" />
        <span>复制自 <bdi>{provenance.source_topic_id}</bdi></span>
      </div>}
      {blocked && <div className="cc-fork-notice" role={targetError || provenance?.state === 'failed' ? 'alert' : 'status'}>
        {checking && <LoaderCircle size={16} className="is-spinning" aria-hidden="true" />}
        <span>{targetError ? `无法确认会话状态：${targetError}`
          : provenance?.state === 'failed' ? `会话初始化失败：${describeForkError(provenance.error) || '未能复制本地上下文，请返回原会话重新创建。'}`
            : provenance?.state === 'pending' ? '新会话正在初始化，暂不能发送消息。' : '正在确认会话状态…'}</span>
        {targetError && <button type="button" disabled={checking} onClick={() => void fork.checkTarget()}>
          <RefreshCw size={15} aria-hidden="true" />重新查询
        </button>}
      </div>}
      {(operation || busy || error) && <div className="cc-fork-notice" role={error || operation?.state === 'failed' ? 'alert' : 'status'}>
        {busy && <LoaderCircle size={16} className="is-spinning" aria-hidden="true" />}
        <span>{error ? `复制结果未确认：${describeForkError(error)}` : busy ? '正在处理会话复制…'
          : operation?.state === 'failed' ? `复制失败：${describeForkError(operation.error) || '未能复制本地上下文，请重新创建。'}`
            : operation?.state === 'ready' ? '新会话已就绪。' : '正在初始化新会话…'}</span>
        {operation?.state === 'ready' && <button type="button" disabled={busy} onClick={() => void fork.open()}>
          <ExternalLink size={15} aria-hidden="true" />打开新会话
        </button>}
        {canCreate && operation?.state === 'failed' && <button type="button" disabled={busy} onClick={() => void fork.create({ newRequest: true })}>
          <Copy size={15} aria-hidden="true" />重新创建
        </button>}
        {canCreate && (error && operation?.state !== 'ready' && operation?.state !== 'failed' || operation?.state === 'pending') && (
          <button type="button" disabled={busy} onClick={() => void fork.create()}><RefreshCw size={15} aria-hidden="true" />重试复制</button>
        )}
      </div>}
    </>
  );
}
