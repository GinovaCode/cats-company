import React, { useState } from 'react';
import { createRoot } from 'react-dom/client';
import { ArrowUp, Check, Moon, Pause, Play, Plus, RotateCcw, Sun } from 'lucide-react';
import '@fontsource-variable/inter';
import '@fontsource-variable/noto-sans-sc';
import './history-loading-demo.css';

function BubbleLoader() {
  return (
    <div className="bubble-loader" aria-hidden="true">
      {[0, 1, 2].map(index => (
        <div className={`little-bubble bubble-${index}`} key={index}>
          <i /><i />
        </div>
      ))}
    </div>
  );
}

function SkeletonLoader() {
  return (
    <div className="skeleton-loader" aria-hidden="true">
      <div className="skeleton-message from-assistant">
        <div className="skeleton-avatar" />
        <div className="skeleton-copy"><i /><i /><i /></div>
      </div>
      <div className="skeleton-message from-user"><div className="skeleton-user-bubble"><i /></div></div>
      <div className="skeleton-message from-assistant last-message">
        <div className="skeleton-avatar" />
        <div className="skeleton-copy"><i /><i /></div>
      </div>
    </div>
  );
}

function Conversation() {
  return (
    <div className="loaded-conversation">
      <div className="message assistant-message">
        <span className="agent-avatar">C</span>
        <div><strong>工作助手</strong><p>项目资料已整理好，会议记录和待办事项都放在一起了。</p><time>14:32</time></div>
      </div>
      <div className="message user-message"><p>帮我列一下今天需要跟进的事情。</p></div>
      <div className="message assistant-message">
        <span className="agent-avatar">C</span>
        <div><strong>工作助手</strong><p>今天有两件事需要跟进：</p><ol><li>确认首页设计稿的反馈。</li><li>整理周五评审需要的材料。</li></ol><time>14:33</time></div>
      </div>
    </div>
  );
}

function Preview({ variant, phase, paused, revision }) {
  const bubble = variant === 'bubbles';
  return (
    <section className="preview-section" aria-labelledby={`${variant}-title`}>
      <div className="preview-heading"><span className="option-number">{bubble ? '01' : '03'}</span><h2 id={`${variant}-title`}>{bubble ? '气泡逐渐拼好' : '消息骨架'}</h2></div>
      <div className={`chat-window ${paused ? 'is-paused' : ''} ${phase}`}>
        <header className="chat-header"><span>工作助手</span><span className="chat-date">今天</span></header>
        <div className="chat-body" aria-busy={phase === 'loading'}>
          {phase === 'loading' ? (
            <div key={revision} className={`history-loading ${bubble ? 'compact-loading' : 'skeleton-loading'}`} role="status">
              {bubble ? <BubbleLoader /> : <SkeletonLoader />}
              <span className="loading-label">正在读取对话<span className="loading-dots" aria-hidden="true">···</span></span>
            </div>
          ) : <Conversation />}
        </div>
        <footer className="chat-footer">
          <div className="preview-composer" aria-hidden="true"><Plus size={17} /><span>发送消息</span><span className="send-placeholder"><ArrowUp size={16} /></span></div>
        </footer>
      </div>
    </section>
  );
}

function App() {
  const [theme, setTheme] = useState(() => new URLSearchParams(location.search).get('theme') === 'dark' ? 'dark' : 'light');
  const [phase, setPhase] = useState('loading');
  const [paused, setPaused] = useState(false);
  const [revision, setRevision] = useState(0);
  const replay = () => { setPhase('loading'); setPaused(false); setRevision(value => value + 1); };
  const changeTheme = value => {
    setTheme(value);
    const url = new URL(location.href);
    url.searchParams.set('theme', value);
    history.replaceState(null, '', url);
  };
  return (
    <main className="loading-demo" data-theme={theme}>
      <header className="demo-header">
        <div className="demo-title"><span className="wordmark">CatsCo</span><span className="title-divider" /><h1>对话加载</h1></div>
        <div className="theme-switch" role="group" aria-label="主题">
          <button type="button" title="浅色主题" aria-label="浅色主题" aria-pressed={theme === 'light'} onClick={() => changeTheme('light')}><Sun size={17} /></button>
          <button type="button" title="深色主题" aria-label="深色主题" aria-pressed={theme === 'dark'} onClick={() => changeTheme('dark')}><Moon size={17} /></button>
        </div>
      </header>
      <div className="demo-toolbar">
        <div className="demo-status" role="status"><span className={phase === 'complete' ? 'status-dot is-complete' : 'status-dot'} />{phase === 'complete' ? '加载完成' : paused ? '已暂停' : '加载中'}</div>
        <div className="demo-actions">
          <button type="button" className="tool-button" title="重新播放" aria-label="重新播放" onClick={replay}><RotateCcw size={16} /></button>
          <button type="button" className="tool-button" title={paused ? '继续播放' : '暂停动效'} aria-label={paused ? '继续播放' : '暂停动效'} onClick={() => setPaused(value => !value)} disabled={phase !== 'loading'}>{paused ? <Play size={16} /> : <Pause size={16} />}</button>
          <button type="button" className="complete-button" onClick={() => setPhase('complete')} disabled={phase === 'complete'}><Check size={16} />模拟加载完成</button>
        </div>
      </div>
      <div className="preview-grid">
        <Preview variant="bubbles" phase={phase} paused={paused} revision={revision} />
        <Preview variant="skeleton" phase={phase} paused={paused} revision={revision} />
      </div>
    </main>
  );
}

createRoot(document.getElementById('root')).render(<App />);
