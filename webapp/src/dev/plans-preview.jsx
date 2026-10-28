import React, { useEffect, useState } from 'react';
import { createRoot } from 'react-dom/client';
import '@fontsource-variable/inter';
import '@fontsource-variable/noto-sans-sc';
import '../views/workspace-styles';
import { api } from '../api';
import { FeedbackProvider } from '../components/feedback-system';
import RelayAccessModal from '../widgets/relay-access-modal';
import EnterpriseReference from './plans-enterprise-reference';
import './plans-preview.css';
import './plans-enterprise-variants.css';

// Draft monthly pricing for the three plans. This document
// uses no account or real orders; prices here do not update the live catalogue.
const plans = [
  { id: -59, slug: 'preview-basic', name: '基础版', description: '适合提炼资料重点、起草邮件和文案。', price_fen: 5900, currency: 'CNY', sale_state: 'public', duration_days: 30 },
  { id: 21, slug: 'catsco-personal', name: '个人版', description: '适合每天整理文件、汇总数据和处理待办。', price_fen: 9900, currency: 'CNY', sale_state: 'public', duration_days: 30 },
  { id: 22, slug: 'catsco-pro', name: '专业版', description: '适合多项工作并行、持续推进复杂项目。', price_fen: 15900, currency: 'CNY', sale_state: 'public', duration_days: 30 },
];

const cloudNote = '云端 AI 员工随套餐有效；到期后保留 15 天，期间续费可恢复。';
const planPresentationOverrides = {
  'preview-basic': {
    kicker: 'CATSCO BASIC', tagline: '轻量日常协作', actionLabel: '获取 CatsCo 基础版',
    highlights: [
      { kind: 'capacity', label: 'Token 额度', value: '5 亿', unit: 'Token' },
    ],
    featuresLabel: false,
    features: [
      { label: 'Office 文件处理', details: ['文档阅读与写作', '表格整理与数据汇总'] },
      '资料检索与信息整理',
      '邮件、文案与报告写作',
      '制作并发布网页与小应用',
      '文件、图片与成果预览',
      '任务进度与执行过程查看',
    ],
  },
  'catsco-personal': {
    kicker: 'CATSCO PERSONAL', tagline: '日常工作自动化', actionLabel: '获取 CatsCo 个人版',
    highlights: [
      { kind: 'capacity', label: 'Token 额度', value: '约 10 亿', unit: 'Token' },
    ],
    featuresLabel: false,
    features: [
      { label: '基础版中的全部权益，以及：', emphasis: true },
      'Agent 后台任务，持续执行',
      '授权设备、文件与工具调用',
      { label: '个人 Skill 与工作偏好', details: ['安装与管理常用 Skill', '保存工作偏好与常用流程'] },
      '持续会话，接着上次的工作',
      '任务成果集中管理与分享',
    ],
    note: cloudNote,
  },
  'catsco-pro': {
    kicker: 'CATSCO PRO', tagline: '高频多任务协作', actionLabel: '获取 CatsCo 专业版', pro: true,
    highlights: [
      { kind: 'capacity', label: 'Token 额度', value: '约 20 亿', unit: 'Token' },
    ],
    featuresLabel: false,
    features: [
      { label: '个人版中的全部权益，以及：', emphasis: true },
      { label: 'Agent 多任务并行', details: ['资料整理与报告撰写并行推进', '多个后台任务同时执行'] },
      '复杂任务，分步持续推进',
      '跨会话检索历史工作与决策',
      '从已完成任务沉淀可复用 Skill',
      '高峰期优先响应',
    ],
    note: cloudNote,
  },
};

api.getRelayConfig = async () => ({ self_service_enabled: false });
api.getRelayCommercial = async () => ({ enabled: true, summary: { uid: 0, models: [], entitlements: [], plans } });
api.getCommercialCatalog = async () => ({ enabled: true, plans, channels: [{ id: 'preview', label: '预览' }], trial_available: false });
api.getCommercialOrders = async () => ({ orders: [] });
api.getRelayUsage = async () => ({ configured: false, summary: null });
// API writes stay local to this document; the enterprise contact link is preserved.
const previewOnly = async () => { throw new Error('当前为界面预览，不会创建订单或发起支付。'); };
for (const method of ['createCommercialOrder', 'cancelCommercialOrder', 'confirmCommercialTestPayment', 'claimCommercialTrial',
  'createRelaySession', 'createRelayKey', 'rotateRelayKey', 'revealRelayKey', 'revokeRelayKey', 'redeemRelayInvite']) {
  api[method] = previewOnly;
}

function PlansPreview() {
  const [theme, setTheme] = useState('light');
  const [open, setOpen] = useState(true);
  const [revision, setRevision] = useState(0);
  const [showChooser, setShowChooser] = useState(true);
  const [enterpriseLayout, setEnterpriseLayout] = useState(() => {
    const layout = new URLSearchParams(location.search).get('enterprise');
    return ['a', 'b', 'c', 'd', 'e'].includes(layout) ? layout : 'e';
  });
  const [enterpriseRequested, setEnterpriseRequested] = useState(() => new URLSearchParams(location.search).has('enterprise'));
  useEffect(() => { document.documentElement.dataset.enterpriseLayout = enterpriseLayout; }, [enterpriseLayout]);
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    document.documentElement.dataset.liquidVariant = 'green';
  }, [theme]);
  useEffect(() => {
    if (!open || !showChooser) return undefined;
    const openChooser = () => {
      const button = document.querySelector('button[aria-controls="relay-access-plan-chooser"]');
      if (!button || button.disabled) return;
      observer.disconnect();
      button.click();
    };
    const observer = new MutationObserver(openChooser);
    observer.observe(document.getElementById('root'), { childList: true, subtree: true });
    openChooser();
    return () => observer.disconnect();
  }, [open, revision, showChooser]);
  useEffect(() => {
    if (!enterpriseRequested || !open || !showChooser) return undefined;
    const selectEnterprise = () => {
      const tab = document.getElementById('relay-access-enterprise-plan-tab');
      if (!tab) return;
      observer.disconnect();
      tab.click();
    };
    const observer = new MutationObserver(selectEnterprise);
    observer.observe(document.getElementById('root'), { childList: true, subtree: true });
    selectEnterprise();
    return () => observer.disconnect();
  }, [enterpriseRequested, open, revision, showChooser]);
  const show = chooser => { setShowChooser(chooser); setOpen(true); setRevision(value => value + 1); };
  return <FeedbackProvider>
    <header className="plans-preview-toolbar">
      <strong>套餐界面预览</strong>
      <span>套餐调整草案 · 示例账号 · 不发起支付</span>
      <nav aria-label="预览控制">
        <button onClick={() => show(true)}>套餐选择</button>
        <button onClick={() => show(false)}>权益概览</button>
        <label>企业排版<select aria-label="企业排版" value={enterpriseLayout} onChange={event => {
          const layout = event.target.value;
          setEnterpriseLayout(layout);
          setEnterpriseRequested(true);
          const url = new URL(location.href);
          url.searchParams.set('enterprise', layout);
          history.replaceState(null, '', url);
          show(true);
        }}>
          <option value="a">A · 紧凑双栏</option><option value="b">B · 独立双卡</option>
          <option value="c">C · 开放清单</option><option value="d">D · 居中单栏</option>
          <option value="e">E · 图文分区</option>
        </select></label>
        <label>主题<select aria-label="预览主题" value={theme} onChange={event => setTheme(event.target.value)}>
          <option value="light">浅色</option><option value="dark">深色</option><option value="liquid">液态</option>
        </select></label>
      </nav>
    </header>
    <main className="plans-preview-empty"><h1>套餐与权益</h1><button onClick={() => show(true)}>打开套餐预览</button></main>
    {open && <RelayAccessModal key={revision} onClose={() => setOpen(false)} showFreePlan={false} planPresentationOverrides={planPresentationOverrides} enterprisePlanContent={enterpriseLayout === 'e' ? <EnterpriseReference /> : null} />}
  </FeedbackProvider>;
}

createRoot(document.getElementById('root')).render(<PlansPreview />);
