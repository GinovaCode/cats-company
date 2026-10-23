import React, { useState } from 'react';
import { createRoot } from 'react-dom/client';
import { ArrowUpRight, Check, ChevronRight, FileText, Globe, Image, Laptop, LayoutGrid, ListTodo, MessageSquare, Moon, Pause, Play, Plus, RotateCcw, Sparkles, Sun } from 'lucide-react';
import '@fontsource-variable/inter';
import '@fontsource-variable/noto-sans-sc';
import './loading-motion-demo.css';

const examples = [
  { id: 'apps', title: '应用 · 窗口成形', subtitle: '全部应用', label: '正在读取应用', Icon: LayoutGrid },
  { id: 'apps-stack', kind: 'apps', variant: 'stack', title: '应用 · 层叠窗口', subtitle: '全部应用', label: '正在读取应用', Icon: LayoutGrid },
  { id: 'apps-icons', kind: 'apps', variant: 'icons', title: '应用 · 图标拼合', subtitle: '全部应用', label: '正在读取应用', Icon: LayoutGrid },
  { id: 'skills', title: 'SkillHub', subtitle: '能力库', label: '正在读取能力', Icon: Sparkles },
  { id: 'files', title: '文件预览', subtitle: '项目资料', label: '正在打开文件', Icon: FileText },
  { id: 'connect', title: '桌面助手', subtitle: '我的电脑', label: '正在连接电脑', Icon: Laptop },
  { id: 'tasks', title: '历史任务', subtitle: '最近任务', label: '正在读取任务', Icon: ListTodo },
  { id: 'chat', title: '聊天记录', subtitle: '工作助手', label: '正在读取对话', Icon: MessageSquare },
];

function Motion({ kind, fileType, variant }) {
  if (kind === 'skills') return <div className="lm-modules">{[0, 1, 2, 3].map(index => <span className={`lm-module piece-${index}`} key={index}><i /><i /></span>)}</div>;
  if (kind === 'apps' && variant === 'stack') return <div className="lm-window-stack">{[0, 1, 2].map(index => <span className={`lm-stack-sheet sheet-${index}`} key={index}><span className="lm-stack-bar"><i /><i /><i /></span><span className="lm-stack-content"><i /><i /></span></span>)}</div>;
  if (kind === 'apps' && variant === 'icons') return <div className="lm-app-icons">{[LayoutGrid, Globe, FileText, ListTodo].map((Icon, index) => <span className={`lm-app-tile tile-${index}`} key={index}><Icon size={17} strokeWidth={1.6} /></span>)}</div>;
  if (kind === 'apps') return <div className="lm-window"><div className="lm-window-top"><i /><i /><i /></div><span className="lm-window-sidebar" /><div className="lm-window-cells">{[0, 1, 2, 3].map(index => <i key={index} />)}</div></div>;
  if (kind === 'files') return fileType === 'image'
    ? <div className="lm-picture">{[0, 1, 2, 3, 4, 5].map(index => <span key={index} />)}</div>
    : <div className="lm-document"><span className="lm-document-fold" /><i /><i /><i /><i /></div>;
  if (kind === 'connect') return <div className="lm-connection"><span className="lm-endpoint"><Globe size={25} /></span><span className="lm-connection-dots"><i /><i /><i /></span><span className="lm-endpoint lm-computer"><Laptop size={28} /></span></div>;
  if (kind === 'tasks') return <div className="lm-task-lines">{[0, 1, 2].map(index => <span key={index}><i /><b /><b /></span>)}</div>;
  return <div className="lm-chat-bubbles">{[0, 1, 2].map(index => <span className={`lm-chat-bubble piece-${index}`} key={index}><i /><i /></span>)}</div>;
}

function CompleteContent({ kind, fileType }) {
  if (kind === 'skills') return <div className="lm-result-list">{[['写作助手', '整理思路与文档'], ['网页搜索', '查找信息与来源'], ['数据分析', '分析表格与数据']].map(([name, detail], index) => <div className="lm-result-row" key={name}><span className={`lm-result-icon tone-${index}`}><Sparkles size={17} /></span><span><strong>{name}</strong><small>{detail}</small></span><Plus size={15} /></div>)}</div>;
  if (kind === 'apps') return <div className="lm-app-list">{[['项目看板', '项目进度'], ['数据工作台', '经营数据'], ['资料库', '项目文档']].map(([name, detail], index) => <div className="lm-app-item" key={name}><span className={`lm-result-icon tone-${index}`}><LayoutGrid size={19} /></span><strong>{name}</strong><small>{detail}</small><ArrowUpRight size={13} /></div>)}</div>;
  if (kind === 'files') return fileType === 'image' ? <figure className="lm-image-result"><img src="/demo-artifacts/catsco-tutorial-sample.png" alt="CatsCo 示例图片" /></figure> : <article className="lm-file-result"><span>项目资料 / 会议纪要</span><h3>设计评审记录</h3><p>本次评审确认了首页布局和任务流程。</p><h4>下一步</h4><ul><li>整理交互细节与反馈</li><li>准备下一轮评审材料</li></ul></article>;
  if (kind === 'connect') return <div className="lm-connected"><span className="lm-connected-icon"><Check size={25} /></span><strong>已连接我的电脑</strong><span>CatsCo 桌面助手</span><small>在线</small></div>;
  if (kind === 'tasks') return <div className="lm-result-list">{[['整理项目资料', '11:05'], ['确认首页设计稿', '昨天'], ['准备评审材料', '昨天']].map(([name, time]) => <div className="lm-result-row lm-task-result" key={name}><Check size={15} /><span><strong>{name}</strong><small>工作助手</small></span><time>{time}</time></div>)}</div>;
  return <div className="lm-chat-result"><div><span className="lm-avatar">C</span><p>项目资料已整理好，待办事项也放在一起了。</p></div><p className="lm-user-message">把今天要跟进的发给我。</p><div><span className="lm-avatar">C</span><p>今天需要确认设计稿，并准备评审材料。</p></div></div>;
}

function Example({ example, complete, paused, onComplete, onReplay, revision }) {
  const [fileType, setFileType] = useState('document');
  const { id, title, subtitle, label, Icon, variant } = example;
  const kind = example.kind || id;
  return (
    <section className={`lm-example ${paused ? 'is-paused' : ''}`} aria-labelledby={`title-${id}`}>
      <div className="lm-example-heading"><h2 id={`title-${id}`}>{title}</h2>{id === 'chat' && <span className="lm-reference">已采用</span>}<div className="lm-example-actions"><button type="button" className="lm-icon-button" title={`重播${title}`} aria-label={`重播${title}`} onClick={onReplay}><RotateCcw size={14} /></button><button type="button" className="lm-icon-button" title={`模拟${title}完成`} aria-label={`模拟${title}完成`} onClick={onComplete} disabled={complete}><Check size={15} /></button></div></div>
      <div className="lm-preview">
        <header className="lm-preview-header"><Icon size={15} /><span>{subtitle}</span>{id === 'files' && <div className="lm-file-switch" role="group" aria-label="文件类型"><button title="文档预览" aria-label="文档预览" aria-pressed={fileType === 'document'} onClick={() => setFileType('document')}><FileText size={14} /></button><button title="图片预览" aria-label="图片预览" aria-pressed={fileType === 'image'} onClick={() => setFileType('image')}><Image size={14} /></button></div>}{id !== 'files' && <ChevronRight className="lm-header-chevron" size={14} />}</header>
        <div className={`lm-preview-body ${complete ? 'is-complete' : ''}`} aria-busy={!complete}>
          {complete ? <div className="lm-content-enter"><CompleteContent kind={kind} fileType={fileType} /></div> : <div className="lm-loading" key={`${revision}-${fileType}`} role="status"><div className="lm-motion" aria-hidden="true"><Motion kind={kind} variant={variant} fileType={fileType} /></div><span className="lm-loading-label">{label}<span aria-hidden="true"> ···</span></span></div>}
        </div>
      </div>
    </section>
  );
}

function App() {
  const [theme, setTheme] = useState(() => new URLSearchParams(location.search).get('theme') === 'dark' ? 'dark' : 'light');
  const [paused, setPaused] = useState(false);
  const [complete, setComplete] = useState({});
  const [revision, setRevision] = useState({});
  const replay = id => {
    setComplete(state => ({ ...state, [id]: false }));
    setRevision(state => ({ ...state, [id]: (state[id] || 0) + 1 }));
  };
  const replayAll = () => { setComplete({}); setPaused(false); setRevision(state => Object.fromEntries(examples.map(({ id }) => [id, (state[id] || 0) + 1]))); };
  const changeTheme = value => { setTheme(value); const url = new URL(location.href); url.searchParams.set('theme', value); history.replaceState(null, '', url); };
  return <main className="lm-demo" data-theme={theme}>
    <header className="lm-header"><div className="lm-title"><span className="lm-wordmark">CatsCo</span><span className="lm-divider" /><h1>加载动效</h1></div><div className="lm-theme-switch" role="group" aria-label="主题"><button className="lm-icon-button" title="浅色主题" aria-label="浅色主题" aria-pressed={theme === 'light'} onClick={() => changeTheme('light')}><Sun size={17} /></button><button className="lm-icon-button" title="深色主题" aria-label="深色主题" aria-pressed={theme === 'dark'} onClick={() => changeTheme('dark')}><Moon size={17} /></button></div></header>
    <div className="lm-toolbar"><span className="lm-count">{examples.filter(({ id }) => complete[id]).length} / {examples.length} 已完成</span><div className="lm-toolbar-actions"><button className="lm-icon-button" title="全部重播" aria-label="全部重播" onClick={replayAll}><RotateCcw size={16} /></button><button className="lm-icon-button" title={paused ? '继续播放' : '暂停全部'} aria-label={paused ? '继续播放' : '暂停全部'} aria-pressed={paused} onClick={() => setPaused(value => !value)}>{paused ? <Play size={16} /> : <Pause size={16} />}</button><button className="lm-complete-all" onClick={() => setComplete(Object.fromEntries(examples.map(({ id }) => [id, true])))}><Check size={15} />全部完成</button></div></div>
    <div className="lm-grid">{examples.map(example => <Example key={example.id} example={example} complete={!!complete[example.id]} paused={paused} revision={revision[example.id] || 0} onComplete={() => setComplete(state => ({ ...state, [example.id]: true }))} onReplay={() => replay(example.id)} />)}</div>
  </main>;
}

createRoot(document.getElementById('root')).render(<App />);
