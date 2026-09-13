import { useEffect, useMemo, useState } from 'react';
import { ArrowLeft, BrainCircuit, ChevronRight, CircleAlert, Search, Sparkles } from 'lucide-react';
import { ApiError, api } from '../../api/client';
import { formatTime } from '../../lib/time';

type LifecycleStatus = 'provisional' | 'active' | 'verified' | 'quarantine' | 'retired';
type LifecycleSummary = {
  evolution_id: string;
  title: string;
  statement: string;
  type: string;
  scope: string;
  project: string;
  device?: string;
  status: LifecycleStatus;
  revision: number;
  support_count: number;
  contradict_count: number;
  evidence_count?: number;
  tags?: string[];
  updated_at: string;
};
type LifecycleEvidence = {
  ref: string;
  relation: 'support' | 'contradict';
  task_id?: string;
  review_revision?: string;
  rationale?: string;
  recorded_at: string;
};
type LifecycleDetail = LifecycleSummary & {
  canonical_key?: string;
  policy_version: string;
  source?: string;
  evidence?: LifecycleEvidence[];
  superseded_by?: string;
  created_at: string;
};
type LifecycleListResponse = { records?: LifecycleSummary[]; count?: number };
type LifecycleDetailResponse = { record: LifecycleDetail };
type Stage3Settings = {
  enabled: boolean;
  configured: boolean;
  model: string;
  interval_minutes: number;
};
type AISettingsResponse = { settings?: { stage3?: Stage3Settings } };

type StatusFilter = 'all' | LifecycleStatus;

const statusOptions: Array<{ value: StatusFilter; label: string }> = [
  { value: 'all', label: '全部状态' },
  { value: 'verified', label: '已验证' },
  { value: 'active', label: '生效' },
  { value: 'provisional', label: '待验证' },
  { value: 'quarantine', label: '隔离' },
  { value: 'retired', label: '已退出' },
];

export default function RecallEvolutionPage({ refreshToken }: { refreshToken: number }) {
  const [records, setRecords] = useState<LifecycleSummary[]>([]);
  const [stage3, setStage3] = useState<Stage3Settings | null>(null);
  const [query, setQuery] = useState('');
  const [status, setStatus] = useState<StatusFilter>('all');
  const [selectedID, setSelectedID] = useState('');
  const [detail, setDetail] = useState<LifecycleDetail | null>(null);
  const [detailOpen, setDetailOpen] = useState(false);
  const [loading, setLoading] = useState(true);
  const [detailLoading, setDetailLoading] = useState(false);
  const [listError, setListError] = useState('');
  const [settingsError, setSettingsError] = useState('');
  const [detailError, setDetailError] = useState('');

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setListError('');
    setSettingsError('');

    Promise.allSettled([
      api<LifecycleListResponse>('/v1/evolution/lifecycle'),
      api<AISettingsResponse>('/v1/settings/ai'),
    ]).then(([lifecycleResult, settingsResult]) => {
      if (cancelled) return;
      if (lifecycleResult.status === 'fulfilled') {
        setRecords(lifecycleResult.value.records || []);
      } else {
        setRecords([]);
        setListError(errorMessage(lifecycleResult.reason));
      }
      if (settingsResult.status === 'fulfilled') {
        setStage3(settingsResult.value.settings?.stage3 || null);
      } else {
        setStage3(null);
        setSettingsError(errorMessage(settingsResult.reason));
      }
      setLoading(false);
    });

    return () => { cancelled = true; };
  }, [refreshToken]);

  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return [...records]
      .sort((left, right) => right.updated_at.localeCompare(left.updated_at))
      .filter((record) => status === 'all' || record.status === status)
      .filter((record) => {
        if (!needle) return true;
        return [
          record.title, record.statement, record.type, record.project, record.scope,
          record.device, record.evolution_id, ...(record.tags || []),
        ].filter(Boolean).join(' ').toLowerCase().includes(needle);
      });
  }, [query, records, status]);

  useEffect(() => {
    if (filtered.length === 0) {
      setSelectedID('');
      setDetail(null);
      setDetailOpen(false);
      return;
    }
    if (!filtered.some((record) => record.evolution_id === selectedID)) {
      setSelectedID(filtered[0].evolution_id);
    }
  }, [filtered, selectedID]);

  useEffect(() => {
    if (!selectedID) return;
    let cancelled = false;
    setDetail(null);
    setDetailLoading(true);
    setDetailError('');
    api<LifecycleDetailResponse>(`/v1/evolution/lifecycle/${encodeURIComponent(selectedID)}`)
      .then((result) => { if (!cancelled) setDetail(result.record); })
      .catch((error) => {
        if (!cancelled) {
          setDetail(null);
          setDetailError(errorMessage(error));
        }
      })
      .finally(() => { if (!cancelled) setDetailLoading(false); });
    return () => { cancelled = true; };
  }, [selectedID, refreshToken]);

  const counts = useMemo(() => ({
    total: records.length,
    active: records.filter((record) => record.status === 'active' || record.status === 'verified').length,
    provisional: records.filter((record) => record.status === 'provisional').length,
    quarantine: records.filter((record) => record.status === 'quarantine').length,
  }), [records]);

  const stage3State = !stage3
    ? { label: '状态未知', tone: 'muted' as const }
    : stage3.enabled && stage3.configured
      ? { label: '已启用', tone: 'ok' as const }
      : stage3.enabled
        ? { label: '等待配置', tone: 'warn' as const }
        : { label: '已关闭', tone: 'muted' as const };

  return <section className={`recall-evolution-page ${detailOpen ? 'is-detail-open' : 'is-list-open'}`}>
    <section className="evolution-overview">
      <div className="evolution-overview-copy">
        <span className="evolution-overview-icon"><Sparkles size={18} /></span>
        <div><h2>进化</h2><p>查看 AgentDock 沉淀的生命周期记录、验证证据与当前生效状态。</p></div>
      </div>
      <div className="evolution-stage3-state">
        <span><small>Stage 3</small><strong>{stage3?.model || '未配置模型'}</strong></span>
        <EvolutionState tone={stage3State.tone}>{stage3State.label}</EvolutionState>
        <button type="button" className="nx-button is-secondary is-small" onClick={() => { window.location.hash = 'settings/ai'; }}>配置模型</button>
      </div>
    </section>

    {(listError || settingsError) && <div className="evolution-errors" role="alert">
      <CircleAlert size={16} />
      <span>{[listError, settingsError].filter(Boolean).join('；')}</span>
    </div>}

    <section className="evolution-stats" aria-label="进化状态概览">
      <EvolutionStat label="生命周期记录" value={counts.total} />
      <EvolutionStat label="已生效 / 已验证" value={counts.active} tone="ok" />
      <EvolutionStat label="待验证" value={counts.provisional} tone="info" />
      <EvolutionStat label="隔离" value={counts.quarantine} tone={counts.quarantine > 0 ? 'warn' : 'muted'} />
    </section>

    <section className="evolution-browser">
      <aside className="evolution-list-panel">
        <div className="evolution-panel-head"><div><h3>生命周期</h3><p>{loading ? '正在读取…' : `${filtered.length} / ${records.length} 条记录`}</p></div></div>
        <div className="evolution-toolbar">
          <label className="evolution-search"><Search size={15} /><input aria-label="搜索进化记录" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索记录" /></label>
          <select aria-label="筛选进化状态" value={status} onChange={(event) => setStatus(event.target.value as StatusFilter)}>{statusOptions.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}</select>
        </div>
        <div className="evolution-record-list">
          {loading && records.length === 0 && <EvolutionEmpty text="正在读取进化记录…" />}
          {!loading && !listError && filtered.length === 0 && <EvolutionEmpty text={records.length === 0 ? '还没有进化记录。' : '没有匹配的进化记录。'} />}
          {filtered.map((record) => <button type="button" key={record.evolution_id} className={selectedID === record.evolution_id ? 'is-active' : ''} aria-current={selectedID === record.evolution_id ? 'true' : undefined} onClick={() => { setSelectedID(record.evolution_id); setDetailOpen(true); }}>
            <span className="evolution-record-main"><strong>{recordTitle(record)}</strong><small>{record.project} · {typeLabel(record.type)}</small></span>
            <EvolutionStatus status={record.status} />
            <ChevronRight size={14} />
          </button>)}
        </div>
      </aside>

      <article className="evolution-detail-panel">
        <button type="button" className="evolution-mobile-back" onClick={() => setDetailOpen(false)}><ArrowLeft size={15} />返回生命周期</button>
        {detailLoading && !detail && <EvolutionEmpty text="正在读取记录详情…" />}
        {detailError && <EvolutionEmpty text={detailError} danger />}
        {!detailLoading && !detailError && !detail && <EvolutionEmpty text="选择一条记录查看验证证据。" />}
        {detail && <EvolutionDetail record={detail} />}
      </article>
    </section>
  </section>;
}

function EvolutionDetail({ record }: { record: LifecycleDetail }) {
  const supportEvidence = (record.evidence || []).filter((item) => item.relation === 'support');
  const contradictEvidence = (record.evidence || []).filter((item) => item.relation === 'contradict');

  return <>
    <header className="evolution-detail-head">
      <div><span className="nexus-eyebrow">{record.project} / {typeLabel(record.type)}</span><h3>{recordTitle(record)}</h3><p>{record.statement}</p></div>
      <EvolutionStatus status={record.status} />
    </header>
    <div className="evolution-detail-meta">
      <EvolutionMeta label="范围" value={scopeLabel(record.scope)} />
      <EvolutionMeta label="设备" value={record.device || '全部设备'} />
      <EvolutionMeta label="修订" value={`r${record.revision}`} />
      <EvolutionMeta label="更新时间" value={formatTime(record.updated_at, { compact: true })} />
    </div>
    <section className="evolution-evidence-summary">
      <div className="is-support"><span>支持证据</span><strong>{record.support_count}</strong></div>
      <div className={record.contradict_count > 0 ? 'is-contradict' : ''}><span>反证</span><strong>{record.contradict_count}</strong></div>
      <div><span>证据条目</span><strong>{record.evidence?.length || 0}</strong></div>
    </section>
    {(record.tags || []).length > 0 && <div className="evolution-tags">{record.tags!.map((tag) => <span key={tag}>{tag}</span>)}</div>}
    <section className="evolution-evidence-list">
      <div className="evolution-section-title"><h4>验证证据</h4><span>{supportEvidence.length} 支持 · {contradictEvidence.length} 反证</span></div>
      {(record.evidence || []).length === 0 && <p className="evolution-no-evidence">暂无证据条目。</p>}
      {(record.evidence || []).map((evidence, index) => <article key={`${evidence.ref}-${index}`} className={`evolution-evidence is-${evidence.relation}`}>
        <span className="evolution-evidence-mark" />
        <div><strong>{evidence.relation === 'support' ? '支持' : '反证'}</strong><p>{evidence.rationale || evidence.ref}</p><small>{[evidence.task_id, evidence.review_revision, formatTime(evidence.recorded_at, { compact: true })].filter(Boolean).join(' · ')}</small></div>
      </article>)}
    </section>
    <details className="evolution-technical">
      <summary>技术信息</summary>
      <div>
        <EvolutionMeta label="Evolution ID" value={record.evolution_id} mono />
        <EvolutionMeta label="Policy" value={record.policy_version} />
        <EvolutionMeta label="Canonical Key" value={record.canonical_key || '—'} mono />
        <EvolutionMeta label="来源" value={record.source || '未记录'} />
        <EvolutionMeta label="创建时间" value={formatTime(record.created_at, { compact: true })} />
        <EvolutionMeta label="替代记录" value={record.superseded_by || '—'} mono />
      </div>
    </details>
  </>;
}

function EvolutionStat({ label, value, tone = 'muted' }: { label: string; value: number; tone?: 'ok' | 'warn' | 'info' | 'muted' }) {
  return <div className={`evolution-stat is-${tone}`}><span>{label}</span><strong>{value}</strong></div>;
}

function EvolutionState({ tone, children }: { tone: 'ok' | 'warn' | 'muted'; children: string }) {
  return <span className={`evolution-state is-${tone}`}><i />{children}</span>;
}

function EvolutionStatus({ status }: { status: LifecycleStatus }) {
  return <span className={`evolution-status is-${status}`}><i />{statusLabel(status)}</span>;
}

function EvolutionMeta({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return <div className="evolution-meta"><span>{label}</span><strong className={mono ? 'nx-mono' : ''}>{value}</strong></div>;
}

function EvolutionEmpty({ text, danger = false }: { text: string; danger?: boolean }) {
  return <div className={`evolution-empty ${danger ? 'is-danger' : ''}`}><BrainCircuit size={22} /><span>{text}</span></div>;
}

function recordTitle(record: Pick<LifecycleSummary, 'title' | 'statement'>): string {
  const title = record.title.trim();
  if (title) return title;
  return record.statement.length > 42 ? `${record.statement.slice(0, 42)}…` : record.statement;
}

function errorMessage(error: unknown): string {
  if (error instanceof ApiError) return error.message;
  return error instanceof Error ? error.message : '读取进化数据失败';
}

function statusLabel(status: LifecycleStatus): string {
  switch (status) {
    case 'verified': return '已验证';
    case 'active': return '生效';
    case 'provisional': return '待验证';
    case 'quarantine': return '隔离';
    case 'retired': return '已退出';
  }
}

function typeLabel(type: string): string {
  const labels: Record<string, string> = {
    preference: '偏好', user_preference: '用户偏好', decision: '决策', explicit_decision: '明确决策', constraint: '约束',
    runbook: '运行手册', bug_pattern: '故障模式', deploy_note: '部署记录', project_trap: '项目陷阱', architecture: '架构',
    anti_pattern: '反模式', operational_lesson: '运维经验', technical_fact: '技术事实', workflow_template: '工作流模板', skill: 'Skill',
  };
  return labels[type] || type || '未分类';
}

function scopeLabel(scope: string): string {
  const labels: Record<string, string> = { project: '项目', device: '设备', user: '用户', shared: '共享', global: '全局', local_only: '仅本机' };
  return labels[scope] || scope || '未记录';
}
