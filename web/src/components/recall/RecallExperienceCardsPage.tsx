import { useEffect, useMemo, useRef, useState } from 'react';
import { ArrowLeft, ChevronRight, FileText, Search, Tags } from 'lucide-react';
import { api } from '../../api/client';
import { formatTime } from '../../lib/time';
import type { Recall, RecallCardSummary } from './types';
import { formatBytes, nameOf, normalizePath } from './utils';

type StatusFilter = 'all' | 'active' | 'inbox' | 'history';
type CardPathMeta = { project: string; status: string; cardType: string };
type Props = { entries: RecallCardSummary[]; loading: boolean };

const STATUS_FILTERS: Array<{ value: StatusFilter; label: string }> = [
  { value: 'all', label: '全部' },
  { value: 'active', label: '生效' },
  { value: 'inbox', label: '待整理' },
  { value: 'history', label: '历史' },
];

const CARD_TYPE_LABELS: Record<string, string> = {
  architecture: '架构',
  anti_pattern: '反模式',
  bug_pattern: '故障模式',
  decision: '决策',
  deploy_note: '部署记录',
  preference: '偏好',
  project_trap: '项目陷阱',
  runbook: '运行手册',
};

export default function RecallExperienceCardsPage({ entries, loading }: Props) {
  const [query, setQuery] = useState('');
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('all');
  const [selectedPath, setSelectedPath] = useState('');
  const [selected, setSelected] = useState<Recall | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [error, setError] = useState('');
  const [mobileDetailOpen, setMobileDetailOpen] = useState(false);
  const detailRequestRef = useRef(0);

  const cards = useMemo(
    () => [...entries].sort((left, right) => left.path.localeCompare(right.path, 'zh-CN')),
    [entries],
  );
  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return cards.filter((entry) => {
      if (statusFilter !== 'all' && statusGroup(entry.status) !== statusFilter) return false;
      if (!needle) return true;
      return [entry.title, entry.project, entry.status, entry.card_type, CARD_TYPE_LABELS[entry.card_type], ...(entry.tags || []), entry.path]
        .filter(Boolean)
        .join(' ')
        .toLowerCase()
        .includes(needle);
    });
  }, [cards, query, statusFilter]);

  useEffect(() => {
    if (filtered.length === 0) {
      detailRequestRef.current += 1;
      setSelectedPath('');
      setSelected(null);
      setDetailLoading(false);
      return;
    }
    if (!filtered.some((entry) => entry.path === selectedPath)) void openCard(filtered[0].path, false);
  }, [filtered, selectedPath]);

  async function openCard(path: string, revealOnMobile: boolean) {
    const requestID = ++detailRequestRef.current;
    setSelectedPath(path);
    setDetailLoading(true);
    setError('');
    try {
      const response = await api<{ recall: Recall }>(`/v1/recall/${encodeURIComponent(path)}`);
      if (requestID !== detailRequestRef.current) return;
      setSelected(response.recall);
      if (revealOnMobile) setMobileDetailOpen(true);
    } catch (reason) {
      if (requestID !== detailRequestRef.current) return;
      setSelected(null);
      setError(reason instanceof Error ? reason.message : '经验卡片读取失败');
    } finally {
      if (requestID === detailRequestRef.current) setDetailLoading(false);
    }
  }

  return <section className={`recall-card-browser ${mobileDetailOpen ? 'is-detail-open' : 'is-list-open'}`}>
    <aside className="recall-card-list-panel">
      <div className="recall-panel-head recall-card-list-head">
        <div><h2>经验卡片</h2><p>浏览已沉淀的可复用经验</p></div>
        <span className="recall-card-total">{cards.length}</span>
      </div>
      <div className="recall-card-toolbar">
        <label className="recall-card-search"><Search size={15} /><input aria-label="搜索经验卡片" value={query} onChange={(event) => { setQuery(event.target.value); setMobileDetailOpen(false); }} placeholder="搜索卡片" /></label>
        <select aria-label="筛选经验卡片状态" value={statusFilter} onChange={(event) => { setStatusFilter(event.target.value as StatusFilter); setMobileDetailOpen(false); }}>{STATUS_FILTERS.map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}</select>
      </div>
      <div className="recall-card-list-summary"><strong>{filtered.length}</strong><span>张卡片</span>{query || statusFilter !== 'all' ? <em>筛选结果</em> : <em>全部</em>}</div>
      <div className="recall-card-list">
        {loading && cards.length === 0 ? <p className="recall-empty">正在读取经验卡片…</p>
          : filtered.length === 0 ? <div className="recall-card-empty"><FileText size={22} /><strong>没有匹配的经验卡片</strong><span>调整搜索词或状态筛选。</span></div>
            : filtered.map((entry) => {
              return <button type="button" key={entry.path} className={selectedPath === entry.path ? 'is-active' : ''} aria-pressed={selectedPath === entry.path} onClick={() => void openCard(entry.path, true)}>
                <span className="recall-card-file-icon"><FileText size={15} /></span>
                <span className="recall-card-list-copy"><strong>{entry.title}</strong><small>{entry.project} · {cardTypeLabel(entry.card_type)}</small></span>
                <CardStatus status={entry.status} />
                <ChevronRight size={14} />
              </button>;
            })}
      </div>
    </aside>

    <article className="recall-card-detail-panel">
      <button type="button" className="recall-card-mobile-back" onClick={() => setMobileDetailOpen(false)}><ArrowLeft size={15} />返回经验卡片</button>
      {error ? <div className="recall-card-detail-empty"><strong>卡片读取失败</strong><span>{error}</span></div>
        : detailLoading && !selected ? <p className="recall-empty">正在读取卡片内容…</p>
          : !selected ? <div className="recall-card-detail-empty"><FileText size={24} /><strong>选择一张经验卡片</strong><span>从左侧列表查看内容和来源。</span></div>
            : <CardDetail recall={selected} />}
    </article>
  </section>;
}

function CardDetail({ recall }: { recall: Recall }) {
  const frontmatter = recall.frontmatter || {};
  const pathMeta = cardPathMeta(recall.path);
  const status = frontmatter.status || pathMeta.status;
  const cardType = frontmatter.card_type || pathMeta.cardType;
  const title = cardTitle(recall);
  const body = cardBody(recall.body || recall.content, title);
  const tags = splitTags(frontmatter.tags);

  return <>
    <header className="recall-card-detail-head">
      <div><span className="nexus-eyebrow">{frontmatter.project || pathMeta.project} / {cardTypeLabel(cardType)}</span><h2>{title}</h2><p>{frontmatter.summary || '结构化经验卡片'}</p></div>
      <CardStatus status={status} />
    </header>
    <div className="recall-card-meta-grid">
      <Info label="项目" value={frontmatter.project || pathMeta.project} />
      <Info label="类型" value={cardTypeLabel(cardType)} />
      <Info label="范围" value={scopeLabel(frontmatter.scope)} />
      <Info label="可信度" value={confidenceLabel(frontmatter.confidence)} />
    </div>
    {tags.length > 0 && <div className="recall-card-tags"><Tags size={14} />{tags.map((tag) => <span key={tag}>{tag}</span>)}</div>}
    <section className="recall-card-body"><p>{body || '这张卡片暂无正文。'}</p></section>
    <details className="recall-card-technical">
      <summary>来源与技术信息</summary>
      <div>
        <Info label="来源" value={frontmatter.source || '未记录'} />
        {frontmatter.evidence && <Info label="证据" value={frontmatter.evidence} />}
        <Info label="更新时间" value={formatTime(frontmatter.updated_at || frontmatter.created_at || '', { fallback: '未记录' })} />
        <Info label="大小" value={formatBytes(recall.size_bytes)} />
        <Info label="路径" value={recall.path} />
      </div>
    </details>
  </>;
}

function CardStatus({ status }: { status: string }) {
  const group = statusGroup(status);
  return <span className={`recall-card-status is-${group}`}><i />{statusLabel(status)}</span>;
}

function Info({ label, value }: { label: string; value: string }) {
  return <div className="recall-card-info"><span>{label}</span><strong>{value || '未记录'}</strong></div>;
}

function cardPathMeta(path: string): CardPathMeta {
  const parts = normalizePath(path).split('/');
  const cardsIndex = parts.indexOf('cards');
  return {
    project: parts[cardsIndex + 1] || 'global',
    status: parts[cardsIndex + 2] || 'unknown',
    cardType: parts[cardsIndex + 3] || 'unknown',
  };
}

function cardListTitle(entry: Pick<RecallCardSummary, 'path' | 'title'>): string {
  if (entry.title.trim()) return entry.title.trim();
  return nameOf(entry.path)
    .replace(/\.(md|txt)$/i, '')
    .replace(/--+/g, ' / ')
    .replace(/-/g, ' ');
}

function cardTitle(recall: Recall): string {
  const heading = (recall.body || recall.content).match(/^#\s+(.+)$/m)?.[1]?.trim();
  return heading || cardListTitle({ path: recall.path, title: '' });
}

function cardBody(body: string, title: string): string {
  const trimmed = body.trim();
  const heading = `# ${title}`;
  return trimmed.startsWith(heading) ? trimmed.slice(heading.length).trim() : trimmed;
}

function splitTags(value?: string): string[] {
  if (!value) return [];
  return value.split(',').map((tag) => tag.trim()).filter(Boolean);
}

function statusGroup(status: string): Exclude<StatusFilter, 'all'> {
  const normalized = status.toLowerCase();
  if (normalized === 'active' || normalized === 'verified') return 'active';
  if (normalized === 'inbox' || normalized === 'unverified' || normalized === 'conflicted') return 'inbox';
  return 'history';
}

function statusLabel(status: string): string {
  switch (status.toLowerCase()) {
    case 'active': return '生效';
    case 'verified': return '已验证';
    case 'inbox': return '待整理';
    case 'unverified': return '待验证';
    case 'conflicted': return '有冲突';
    case 'historical': return '历史';
    case 'archived': return '已归档';
    case 'deprecated': return '已弃用';
    case 'stale': return '待复核';
    case 'rejected': return '已拒绝';
    default: return status || '未知';
  }
}

function cardTypeLabel(cardType: string): string {
  return CARD_TYPE_LABELS[cardType] || cardType || '未分类';
}

function scopeLabel(scope?: string): string {
  if (!scope) return '未记录';
  if (scope === 'project') return '项目';
  if (scope === 'global' || scope === 'shared') return '全局';
  if (scope === 'device') return '设备';
  if (scope === 'user') return '用户';
  return scope;
}

function confidenceLabel(confidence?: string): string {
  if (!confidence) return '未记录';
  if (confidence === 'high') return '高';
  if (confidence === 'medium') return '中';
  if (confidence === 'low') return '低';
  return confidence;
}
