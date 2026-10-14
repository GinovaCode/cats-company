import React, { useEffect, useMemo, useRef, useState } from 'react';
import { ArrowLeft, RotateCcw } from 'lucide-react';
import ChatMessage from '../widgets/chat-message';
import ChatComposer from '../widgets/chat-composer';
import LiquidFlowBackground from '../components/liquid-flow-background';
import './chat-bubbles-demo.css';

// Playback represents observable milestones, not a live Agent or form write.
const stages = [
  { text: '好的。我先核对表格和你之前给的资料，已经手填的内容会保留。', tool: 'read_page_context', result: '发现 24 个字段，公司已填写。' },
  { text: '公司已经填好了，我会保留。手机号没找到，先留空。', tool: 'match_profile_fields', result: '23 项资料匹配，跳过公司，手机号缺失。' },
  { text: '姓名、职位和邮箱填好了，接下来填写工作和项目信息。', tool: 'apply_profile_fields', result: '基本资料已写入，草稿版本已更新。' },
  { text: '工作和项目信息也填好了。我再检查一遍，确认没有覆盖你手填的内容。', tool: 'verify_draft', result: '22 项已填写，公司原值保留，手机号为空。' },
];
function stageBlocks(count, visibleChars, running) {
  return stages.slice(0, count).flatMap((stage, index) => [
    { type: 'text', text: index === count - 1 && visibleChars != null ? stage.text.slice(0, visibleChars) : stage.text,
      presentation_role: 'process', _streaming: running && index === count - 1 },
    { type: 'tool_use', id: `preview-${index}`, name: stage.tool, input: { target: '成员资料登记' } },
    ...(index < count - 1 ? [{ type: 'tool_result', tool_use_id: `preview-${index}`, content: stage.result }] : []),
  ]);
}

export default function ChatBubblesDemo() {
  const [summaryURL] = useState(() => URL.createObjectURL(new Blob(['成员资料摘要（虚构示例）\n姓名：林知夏\n公司：星河设计\n手机号：未提供，留空'], { type: 'text/plain' })));
  useEffect(() => () => URL.revokeObjectURL(summaryURL), [summaryURL]);
  const [draft, setDraft] = useState('');
  const [extraMessages, setExtraMessages] = useState([]);
  const [stage, setStage] = useState(1);
  const [stopped, setStopped] = useState(false);
  const [replay, setReplay] = useState(0);
  const resultArrival = useMemo(() => ({}), [replay]);
  const timelineRef = useRef(null);
  const complete = stage > stages.length;
  const running = !complete && !stopped;
  const [playback, setPlayback] = useState({ stage: 1, chars: 0 });
  const visibleChars = complete ? null : playback.stage === stage ? playback.chars : 0;
  // Simulated network chunks for this local demonstration only.
  useEffect(() => {
    if (!running) return undefined;
    let chars = 0;
    setPlayback({ stage, chars });
    let interval;
    const timer = setTimeout(() => {
      interval = setInterval(() => {
        chars = Math.min(stages[stage - 1].text.length, chars + 8);
        setPlayback({ stage, chars });
        if (chars >= stages[stage - 1].text.length) clearInterval(interval);
      }, 180);
    }, 300);
    return () => { clearTimeout(timer); clearInterval(interval); };
  }, [running, stage, replay]);
  useEffect(() => {
    if (!running) return undefined;
    const timer = setTimeout(() => setStage(value => value + 1), 2200);
    return () => clearTimeout(timer);
  }, [stage, running, replay]);
  useEffect(() => {
    const timeline = timelineRef.current;
    if (timeline && timeline.scrollHeight - timeline.clientHeight - timeline.scrollTop < 160) timeline.scrollTop = timeline.scrollHeight;
  }, [stage, extraMessages]);
  const send = () => {
    if (!draft.trim()) return;
    if (running) setStopped(true);
    setExtraMessages(previous => [...previous, { self: true, content: draft.trim() }, {
      content: running ? '我先暂停这次填写，已完成的内容保留。我们可以先处理你刚才补充的要求。' : '收到。这是交互预览，新的要求已显示在对话里；实际执行需要接入 Agent。',
    }]);
    setDraft('');
  };
  const stop = () => {
    setStopped(true);
    setExtraMessages(previous => [...previous, { content: '已停止填写，已完成的内容保留。' }]);
  };
  return <>
    <LiquidFlowBackground />
    <main className="cc-bubbles-demo v3-main">
      <header className="cc-bubbles-demo-header">
        <a href="/"><ArrowLeft size={16} />工作台</a>
        <div><strong>执行过程对话</strong><span>液态浅色 · 本地事件演示</span></div>
        <button type="button" onClick={() => { setReplay(value => value + 1); setStage(1); setPlayback({ stage: 1, chars: 0 }); setStopped(false); setExtraMessages([]); }}><RotateCcw size={15} />重播过程</button>
      </header>
      <section className="v3-chat-column cc-bubbles-demo-chat">
        <div ref={timelineRef} className="v3-timeline cc-bubbles-demo-timeline">
          <div className="cc-message-anchor"><ChatMessage message={{ id: 1, content: '根据之前的资料填写这个表格，已经手填的内容保留，缺少的信息先留空。' }} isSelf senderName="你" /></div>
          <div className="cc-message-anchor"><ChatMessage isSelf={false} senderName="达芬奇" senderIsBot
            message={{ id: 2, content: '', content_blocks: stageBlocks(stage, visibleChars, running), _stopped: stopped }} workingOnly workingComplete={complete} />
          </div>
          {complete && <div className="cc-message-anchor"><ChatMessage isSelf={false} senderName="达芬奇" senderIsBot isConsecutive artifactsFirst
            message={{ id: 3, _replyArrival: resultArrival, content: '填写完成，新增了 22 项。公司保留原值，手机号留空。你可以检查表格，确认后再登记。',
              content_blocks: [
                { type: 'file', payload: { name: '成员资料摘要.txt', url: summaryURL, size: 186, mime_type: 'text/plain' } },
                { type: 'text', text: '填写完成，新增了 22 项。公司保留原值，手机号留空。你可以检查表格，确认后再登记。' },
              ] }} />
          </div>}
          {extraMessages.map((item, index) => <div className="cc-message-anchor" key={index}>
            <ChatMessage message={{ id: 10 + index, content: item.content }} isSelf={Boolean(item.self)} senderName={item.self ? '你' : '达芬奇'} senderIsBot={!item.self} />
          </div>)}
        </div>
        <ChatComposer value={draft} onChange={event => setDraft(event.target.value)} onSend={send}
          onKeyDown={event => {
            if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); send(); }
          }}
          agentPickerVisible={false} attachmentDisabled voiceInputDisabled sendDisabled={!draft.trim()}
          separateStop={running} onStop={stop} placeholder="执行中也可以补充要求…" />
      </section>
    </main>
  </>;
}
