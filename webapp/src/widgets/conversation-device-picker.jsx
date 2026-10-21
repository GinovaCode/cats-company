import React, { useCallback, useEffect, useId, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { Laptop, X } from 'lucide-react';
import { api, getAuthRevision, getToken, isCurrentAuthSession } from '../api';
import '../css/conversation-device-picker.css';

export default function ConversationDevicePicker({ topicId, activeAgent }) {
  const agentId = activeAgent?.uid || activeAgent?.id;
  if (!topicId) return null;
  if (!agentId && topicId.startsWith('grp_')) return <GroupDevicePicker key={topicId} topicId={topicId} />;
  if (!agentId) return null;
  return <DevicePicker key={`${topicId}:${agentId}`} topicId={topicId} agentId={agentId}
    agentName={activeAgent?.display_name || activeAgent?.name || activeAgent?.username || String(agentId)} />;
}

function GroupDevicePicker({ topicId }) {
  const [agent, setAgent] = useState(null);
  const [visited, setVisited] = useState(false);
  useEffect(() => {
    const reset = () => { setAgent(null); setVisited(false); };
    window.addEventListener('cc:auth-changed', reset);
    return () => window.removeEventListener('cc:auth-changed', reset);
  }, []);
  return <DevicePicker key={agent?.uid || 'choose-agent'} topicId={topicId} agentId={agent?.uid || ''}
    agentName={agent?.display_name} onAgentPick={(next) => { setAgent(next); setVisited(true); }} initialOpen={visited} />;
}

function DevicePicker({ topicId, agentId, agentName, onAgentPick, initialOpen = false }) {
  const [snapshot, setSnapshot] = useState(null);
  const [draft, setDraft] = useState('');
  const [open, setOpen] = useState(initialOpen);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const sequence = useRef(0);
  const trigger = useRef(null);
  const dialog = useRef(null);
  const titleId = useId();

  const load = useCallback(async () => {
    const seq = ++sequence.current;
    const token = getToken(), authRevision = getAuthRevision();
    const current = () => sequence.current === seq && isCurrentAuthSession(token, authRevision);
    setBusy(true);
    setError('');
    try {
      const next = await api.getConversationDevice(topicId, agentId);
      if (!current()) return;
      setSnapshot(next);
      setDraft(next.selected_device_id || '');
    } catch {
      if (!current()) return;
      setSnapshot(null);
      setDraft('');
      setError('暂时无法读取设备状态，请刷新重试。');
    } finally {
      if (current()) setBusy(false);
    }
  }, [topicId, agentId]);

  useEffect(() => {
    void load();
    const onAuth = () => {
      ++sequence.current;
      setSnapshot(null); setDraft(''); setOpen(false); setBusy(false); setError('');
      if (getToken()) void load();
    };
    window.addEventListener('cc:auth-changed', onAuth);
    return () => {
      ++sequence.current;
      window.removeEventListener('cc:auth-changed', onAuth);
    };
  }, [load]);

  useEffect(() => {
    if (open || busy) return undefined;
    const refresh = () => { if (getToken()) void load(); };
    window.addEventListener('focus', refresh);
    return () => window.removeEventListener('focus', refresh);
  }, [open, busy, load]);

  useEffect(() => {
    if (!open) return undefined;
    dialog.current?.focus();
    const onKey = (event) => {
      if (event.key === 'Escape') { event.preventDefault(); setOpen(false); }
      if (event.key !== 'Tab') return;
      const controls = [...(dialog.current?.querySelectorAll('button:not(:disabled), input:not(:disabled)') || [])];
      const first = controls[0], last = controls[controls.length - 1];
      if (event.shiftKey && (document.activeElement === first || document.activeElement === dialog.current)) {
        event.preventDefault(); last?.focus();
      } else if (!event.shiftKey && (document.activeElement === last || document.activeElement === dialog.current)) {
        event.preventDefault(); first?.focus();
      }
    };
    document.addEventListener('keydown', onKey);
    return () => { document.removeEventListener('keydown', onKey); trigger.current?.focus(); };
  }, [open]);

  const confirm = async () => {
    if (busy || !snapshot || !snapshot.devices?.some((device) => device.deviceId === draft && device.routable)) return;
    const seq = ++sequence.current;
    const token = getToken(), authRevision = getAuthRevision();
    const current = () => sequence.current === seq && isCurrentAuthSession(token, authRevision);
    setBusy(true); setError('');
    try {
      const next = await api.confirmConversationDevice(topicId, agentId, draft, snapshot.revision);
      if (!current()) return;
      setSnapshot(next); setDraft(next.selected_device_id || ''); setOpen(false);
    } catch {
      if (!current()) return;
      // A timeout may have committed, or another tab may have selected a
      // different computer. Never resubmit a switch without user confirmation.
      setSnapshot(null);
      setError('未能确认选择结果，可能设备状态或选择已变化。请先刷新，再确认；不会自动重试切换。');
    } finally {
      if (current()) setBusy(false);
    }
  };

  const selected = snapshot?.devices?.find((device) => device.deviceId === snapshot.selected_device_id);
  const label = !snapshot ? (busy ? '读取中' : '状态待确认')
    : snapshot.status === 'choose_agent' ? '按 Bot 选择'
    : snapshot.status === 'unavailable' ? `${selected?.displayName || snapshot.selected_device_id} · 离线`
      : snapshot.status === 'needs_selection' ? (snapshot.devices?.length ? '请选择' : '未连接')
        : selected?.displayName || selected?.deviceId || '请选择';
  const scopedLabel = onAgentPick && agentId ? `${snapshot?.agent_name || agentName}：${label}` : label;
  const description = `默认操作的电脑${agentId ? `（${snapshot?.agent_name || agentName}）` : ''}：${label}`;
  return <>
    <button ref={trigger} type="button" disabled={busy} className="cc-device-trigger" aria-haspopup="dialog" aria-expanded={open}
      title={description} aria-label={description} onClick={() => { setOpen(true); void load(); }}>
      <Laptop size={16} aria-hidden="true" /><span>默认电脑</span><span className="cc-device-current">{scopedLabel}</span>
    </button>
    {open && createPortal(<div className="cc-device-backdrop" onClick={(event) => { if (event.target === event.currentTarget) setOpen(false); }}>
      <section ref={dialog} tabIndex={-1} role="dialog" aria-modal="true" aria-labelledby={titleId} className="cc-device-dialog">
        <header><h2 id={titleId}>默认操作的电脑</h2><button type="button" aria-label="关闭设备选择" onClick={() => setOpen(false)}><X size={18} /></button></header>
        {agentId ? <p>仅用于你在当前对话中与「{snapshot?.agent_name || agentName}」的设备操作，不影响其他成员或 Bot。</p>
          : <p>请先选择这段对话中的 Bot。每位成员、每位 Bot 的设备选择分别保存。</p>}
        {onAgentPick && agentId && <button type="button" disabled={busy} onClick={() => onAgentPick(null)}>更换 Bot</button>}
        {snapshot?.status === 'choose_agent' && <div className="cc-device-list">
          {(snapshot.agents || []).map((agent) => <button type="button" key={agent.uid} disabled={busy} onClick={() => onAgentPick(agent)}>{agent.display_name} · {agent.uid}</button>)}
          {!snapshot.agents?.length && <p>当前对话中没有可选择的 Bot。</p>}
        </div>}
        <p>未指定设备时默认使用这台电脑；也可以在指令中指定其他设备或多台设备，不改变默认选择。设备断线不会自动改用其他电脑。</p>
        {error && <p role="alert" className="cc-device-error">{error}</p>}
        {busy && <p role="status">正在同步设备选择…</p>}
        <div className="cc-device-list" role="group" aria-label="我的电脑">
          {(snapshot?.devices || []).map((device) => <label key={device.deviceId} className="cc-device-option">
            <input type="radio" name={titleId} value={device.deviceId} checked={draft === device.deviceId}
              disabled={busy || !device.routable} onChange={() => setDraft(device.deviceId)} />
            <span><strong>{device.displayName || device.deviceId}</strong><small>{device.deviceId}</small></span>
            <span>{device.routable ? '在线' : '离线'}</span>
          </label>)}
        </div>
        {snapshot && snapshot.status !== 'choose_agent' && !snapshot.devices?.length && <p>没有已连接的电脑。请在需要操作的电脑上登录 Connector。</p>}
        <p className="cc-device-note">确认后，请重新发送需要执行的指令。切换不会撤销已开始的操作，也不会自动重放先前的删除或覆盖指令。</p>
        <footer><button type="button" disabled={busy} onClick={() => void load()}>刷新</button>
          <button type="button" disabled={busy || !snapshot?.devices?.some((device) => device.deviceId === draft && device.routable)} onClick={() => void confirm()}>确认使用这台电脑</button></footer>
      </section>
    </div>, document.body)}
  </>;
}
