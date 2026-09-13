import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { ArrowLeft, Check, ChevronRight, Copy, FileJson, History, Search } from 'lucide-react';
import { api } from '../../api/client';
import { formatTime } from '../../lib/time';
import MobileDrilldownBar from '../MobileDrilldownBar';

type WorkflowStatus = 'active' | 'retired';
type Tone = 'ok' | 'warn' | 'danger' | 'info' | 'muted';
type DetailMode = 'current' | 'history' | 'history-detail';

type WorkflowTemplateSummary = {
  id: string;
  version: string;
  title: string;
  description?: string;
  status: WorkflowStatus;
  file_name: string;
  path: string;
  size_bytes: number;
  updated_at: string;
  step_count: number;
  keywords?: string[];
  version_count?: number;
  active_count?: number;
  retired_count?: number;
  has_conflict?: boolean;
};

type WorkflowTemplateDetail = WorkflowTemplateSummary & {
  content: string;
  json?: Record<string, unknown>;
};

type ListResponse = { ok: boolean; items: WorkflowTemplateSummary[]; count: number };
type DetailResponse = { ok: boolean; template: Record<string, unknown>; template_summary: WorkflowTemplateSummary };
type Notice = { tone: Tone; text: string };
type StepView = { id: string; title: string; phase: string; required: boolean; depends: string[]; substitution: string };
type StepGroup = { phase: string; steps: StepView[] };
type MatchView = { label: string; values: string[] };

function statusLabel(status: WorkflowStatus): string {
  return status === 'active' ? '当前版本' : '历史版本';
}

function statusTone(template?: Pick<WorkflowTemplateSummary, 'status'>): Tone {
  if (!template) return 'muted';
  return template.status === 'active' ? 'info' : 'muted';
}

function templateDisplayTitle(template?: Pick<WorkflowTemplateSummary, 'title' | 'id' | 'file_name'>): string {
  return template?.title?.trim() || template?.id || template?.file_name || '未命名模板';
}

function templateListMeta(template: WorkflowTemplateSummary): string {
  return [`v${template.version || '—'}`, `${template.step_count || 0} 步`, `${template.version_count ?? 1} 个版本`].join(' · ');
}

function sortTemplateVersions(items: WorkflowTemplateSummary[]): WorkflowTemplateSummary[] {
  return [...items].sort((left, right) => {
    if (left.status !== right.status) return left.status === 'active' ? -1 : 1;
    return right.version.localeCompare(left.version, undefined, { numeric: true, sensitivity: 'base' });
  });
}

function parseTemplate(content: string): { body: Record<string, unknown>; id: string; version: string; title: string; description: string; stepCount: number; error?: string } {
  try {
    const body = JSON.parse(content || '{}') as Record<string, unknown>;
    const id = text(body.id);
    const version = text(body.version);
    const title = text(body.title);
    const description = text(body.description);
    const steps = array(body.steps).length;
    if (!id || !version) return { body, id, version, title, description, stepCount: steps, error: 'JSON 需要包含 id 和 version。' };
    return { body, id, version, title, description, stepCount: steps };
  } catch (error) {
    return { body: {}, id: '', version: '', title: '', description: '', stepCount: 0, error: error instanceof Error ? error.message : 'JSON 解析失败' };
  }
}

export default function WorkflowTemplatesPage({ refreshToken }: { refreshToken: number }) {
  const [items, setItems] = useState<WorkflowTemplateSummary[]>([]);
  const [query, setQuery] = useState('');
  const [selectedCurrent, setSelectedCurrent] = useState<WorkflowTemplateDetail | null>(null);
  const [selectedHistory, setSelectedHistory] = useState<WorkflowTemplateDetail | null>(null);
  const [historyVersions, setHistoryVersions] = useState<WorkflowTemplateSummary[]>([]);
  const [detailMode, setDetailMode] = useState<DetailMode>('current');
  const [mobileDetailOpen, setMobileDetailOpen] = useState(false);
  const [loading, setLoading] = useState(true);
  const [historyLoading, setHistoryLoading] = useState(false);
  const [notice, setNotice] = useState<Notice | null>(null);

  const loadListRef = useRef(loadList);
  loadListRef.current = loadList;
  useEffect(() => { void loadListRef.current(); }, [refreshToken]);

  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) return items;
    return items.filter((item) => [item.id, item.version, item.title, item.description, ...(item.keywords || [])].filter(Boolean).join(' ').toLowerCase().includes(needle));
  }, [items, query]);

  const visibleDetail = detailMode === 'history-detail' ? selectedHistory : selectedCurrent;
  const parsed = useMemo(() => parseTemplate(visibleDetail?.content || ''), [visibleDetail]);

  async function loadList() {
    setLoading(true);
    setNotice(null);
    try {
      // 默认列表接口就是“当前视图”。历史版本只在用户进入某个模板的历史页时按需读取。
      const result = await api<ListResponse>('/v1/workflow-templates');
      const nextItems = (result.items || []).filter((item) => item.status === 'active');
      setItems(nextItems);
      const target = nextItems.find((item) => item.id === selectedCurrent?.id) || nextItems[0];
      if (target) await openCurrentTemplate(target);
      else {
        setSelectedCurrent(null);
        setSelectedHistory(null);
        setHistoryVersions([]);
        setDetailMode('current');
      }
    } catch (error) {
      setNotice({ tone: 'danger', text: error instanceof Error ? error.message : '工作流模板读取失败' });
    } finally {
      setLoading(false);
    }
  }

  async function readTemplate(template: WorkflowTemplateSummary, aggregate: WorkflowTemplateSummary = template): Promise<WorkflowTemplateDetail> {
    const result = await api<DetailResponse>(`/v1/workflow-templates/${encodeURIComponent(template.id)}/${encodeURIComponent(template.version)}`);
    return {
      ...result.template_summary,
      version_count: aggregate.version_count ?? result.template_summary.version_count,
      active_count: aggregate.active_count ?? result.template_summary.active_count,
      retired_count: aggregate.retired_count ?? result.template_summary.retired_count,
      has_conflict: aggregate.has_conflict ?? result.template_summary.has_conflict,
      content: JSON.stringify(result.template, null, 2),
      json: result.template,
    };
  }

  async function openCurrentTemplate(template: WorkflowTemplateSummary, revealOnMobile = false) {
    setNotice(null);
    try {
      const detail = await readTemplate(template);
      setSelectedCurrent(detail);
      setSelectedHistory(null);
      setHistoryVersions([]);
      setDetailMode('current');
      if (revealOnMobile) setMobileDetailOpen(true);
    } catch (error) {
      setNotice({ tone: 'danger', text: error instanceof Error ? error.message : '模板详情读取失败' });
    }
  }

  async function openHistory() {
    if (!selectedCurrent) return;
    setHistoryLoading(true);
    setNotice(null);
    try {
      const queryValue = encodeURIComponent(selectedCurrent.id);
      const result = await api<ListResponse>(`/v1/workflow-templates?include_history=true&q=${queryValue}`);
      const versions = sortTemplateVersions((result.items || []).filter((item) => item.id === selectedCurrent.id));
      setHistoryVersions(versions);
      setSelectedHistory(null);
      setDetailMode('history');
    } catch (error) {
      setNotice({ tone: 'danger', text: error instanceof Error ? error.message : '历史版本读取失败' });
    } finally {
      setHistoryLoading(false);
    }
  }

  async function openHistoryVersion(template: WorkflowTemplateSummary) {
    if (template.status === 'active') {
      setSelectedHistory(null);
      setDetailMode('current');
      return;
    }
    setNotice(null);
    try {
      setSelectedHistory(await readTemplate(template, selectedCurrent || template));
      setDetailMode('history-detail');
    } catch (error) {
      setNotice({ tone: 'danger', text: error instanceof Error ? error.message : '历史版本详情读取失败' });
    }
  }

  function copyPath() {
    if (!visibleDetail) return;
    void navigator.clipboard?.writeText(visibleDetail.path);
    setNotice({ tone: 'ok', text: '模板路径已复制。' });
  }

  function showCurrentDetail() {
    setSelectedHistory(null);
    setDetailMode('current');
  }

  function mobileBar() {
    if (!selectedCurrent) return null;
    if (detailMode === 'history') {
      return <MobileDrilldownBar label="历史版本" title={templateDisplayTitle(selectedCurrent)} meta={`${historyVersions.length} 个版本`} backLabel="返回当前版本" onBack={showCurrentDetail} />;
    }
    if (detailMode === 'history-detail' && selectedHistory) {
      return <MobileDrilldownBar label="历史版本" title={templateDisplayTitle(selectedHistory)} meta={`v${selectedHistory.version}`} backLabel="返回历史版本" onBack={() => setDetailMode('history')} />;
    }
    return <MobileDrilldownBar label="模板详情" title={templateDisplayTitle(selectedCurrent)} meta={`v${selectedCurrent.version} · 当前版本`} backLabel="返回模板列表" onBack={() => setMobileDetailOpen(false)} />;
  }

  return <section className="workflow-page">
    {notice && <div className={`nx-alert is-${notice.tone === 'danger' ? 'error' : notice.tone === 'ok' ? 'success' : 'warning'}`}>{notice.text}</div>}

    <section className={`workflow-layout mobile-drilldown ${mobileDetailOpen ? 'is-detail-open' : 'is-list-open'}`}>
      <aside className="workflow-list-panel mobile-drilldown-list">
        <div className="workflow-toolbar">
          <label className="workflow-search"><Search size={15} /><input aria-label="搜索工作流模板" value={query} onChange={(event) => { setQuery(event.target.value); setMobileDetailOpen(false); }} placeholder="搜索标题或关键词" /></label>
        </div>
        <div className="workflow-list-summary"><strong>{filtered.length}</strong><span>个工作流模板</span><em>仅显示当前版本</em></div>
        <div className="workflow-list">
          {loading ? <p className="empty-mini">正在读取工作流模板…</p> : filtered.length === 0 ? <p className="empty-mini">没有匹配的模板。</p> : filtered.map((item) => <button type="button" key={item.id} className={selectedCurrent?.id === item.id ? 'is-active' : ''} aria-pressed={selectedCurrent?.id === item.id} onClick={() => void openCurrentTemplate(item, true)}>
            <span className="workflow-file-icon"><FileJson size={16} /></span>
            <span><strong>{templateDisplayTitle(item)}</strong><small>{templateListMeta(item)}</small></span>
            {item.has_conflict && <StatusPill tone="danger">当前×{item.active_count}</StatusPill>}
          </button>)}
        </div>
      </aside>

      <main className="workflow-runtime-viewer mobile-drilldown-detail">
        {mobileBar()}
        {!selectedCurrent ? <div className="empty-state"><span><FileJson size={24} /></span><h3>选择模板</h3><p>从左侧选择一个模板查看执行步骤。</p></div>
          : detailMode === 'history' ? <WorkflowHistoryViewer selected={selectedCurrent} versions={historyVersions} loading={historyLoading} onBack={showCurrentDetail} onOpenVersion={(version) => void openHistoryVersion(version)} />
            : visibleDetail && <>
              {detailMode === 'history-detail' && <div className="workflow-detail-context"><button type="button" className="nx-button is-secondary is-small" onClick={() => setDetailMode('history')}><ArrowLeft size={14} />返回历史版本</button><span>正在查看 v{visibleDetail.version}</span></div>}
              <RuntimeTemplateViewer selected={visibleDetail} parsed={parsed} onCopy={copyPath} onOpenHistory={detailMode === 'current' ? () => void openHistory() : undefined} historyLoading={historyLoading} />
            </>}
      </main>
    </section>
  </section>;
}

function WorkflowHistoryViewer({ selected, versions, loading, onBack, onOpenVersion }: { selected: WorkflowTemplateDetail; versions: WorkflowTemplateSummary[]; loading: boolean; onBack: () => void; onOpenVersion: (template: WorkflowTemplateSummary) => void }) {
  return <article className="workflow-history-card">
    <header className="workflow-history-head">
      <div><button type="button" className="workflow-history-back" onClick={onBack}><ArrowLeft size={14} />返回当前版本</button><span className="nexus-eyebrow">{selected.id}</span><h3>历史版本</h3><p>{templateDisplayTitle(selected)}</p></div>
      <span className="workflow-history-count">{versions.length} 个版本</span>
    </header>
    <div className="workflow-history-list">
      {loading ? <EmptyMini>正在读取历史版本…</EmptyMini> : versions.length <= 1 ? <div className="workflow-history-empty"><History size={22} /><strong>还没有历史版本</strong><span>发布新版本后，旧版本会自动进入这里。</span></div> : versions.map((version) => <button type="button" key={version.path} onClick={() => onOpenVersion(version)}>
        <span className="workflow-history-version"><strong>v{version.version}</strong><small>更新于 {formatTime(version.updated_at)}</small></span>
        <StatusPill tone={statusTone(version)}>{statusLabel(version.status)}</StatusPill>
        <ChevronRight size={15} />
      </button>)}
    </div>
  </article>;
}

function RuntimeTemplateViewer({ selected, parsed, onCopy, onOpenHistory, historyLoading = false }: { selected: WorkflowTemplateDetail; parsed: ReturnType<typeof parseTemplate>; onCopy: () => void; onOpenHistory?: () => void; historyLoading?: boolean }) {
  const match = record(parsed.body.match);
  const steps = stepViews(parsed.body.steps);
  const conditions = stringValues(parsed.body.completion_conditions);
  const keywords = [...stringValues(match.keywords), ...(selected.keywords || [])].filter((value, index, list) => value && list.indexOf(value) === index);
  const matchRows = matchViews(match);
  const stepGroups = groupSteps(steps);
  const phases = stepGroups.flatMap((group) => group.phase ? [group.phase] : []);
  const raw = selected.json || parsed.body;

  return <article className="workflow-runtime-card">
    <header className="workflow-runtime-head">
      <div><span className="nexus-eyebrow">{selected.id}</span><h3>{parsed.title || selected.title || selected.file_name}</h3><p>{parsed.description || selected.description || '暂无模板说明。'}</p></div>
      <div className="workflow-runtime-actions"><StatusPill tone={selected.has_conflict ? 'danger' : statusTone(selected)}>{selected.has_conflict ? `当前×${selected.active_count}` : statusLabel(selected.status)}</StatusPill><span className="workflow-step-count">{steps.length || selected.step_count || 0} 步</span></div>
    </header>

    <div className="workflow-runtime-meta"><span>版本 {selected.version}</span><span>{phases.length || 1} 个阶段</span><span>更新于 {formatTime(selected.updated_at)}</span>{onOpenHistory && (selected.retired_count ?? 0) > 0 && <button type="button" className="workflow-history-link" onClick={onOpenHistory} disabled={historyLoading}><History size={13} />{historyLoading ? '读取中…' : `历史版本 ${selected.retired_count}`}<ChevronRight size={13} /></button>}</div>

    <section className="workflow-runtime-section">
      <SectionTitle title="执行步骤" subtitle="按阶段查看任务主流程。" />
      {steps.length === 0 ? <EmptyMini>没有步骤。</EmptyMini> : <div className="workflow-phase-list">{stepGroups.map((group) => <div className="workflow-phase-block" key={group.phase}><header><span>{group.phase}</span><strong>{group.steps.length} 步</strong></header><div className="workflow-step-list">{group.steps.map((step, index) => <StepCard key={`${group.phase}:${step.id}:${index}`} step={step} index={steps.indexOf(step) + 1} />)}</div></div>)}</div>}
    </section>

    <section className="workflow-runtime-section">
      <SectionTitle title="完成条件" subtitle="任务结束前必须满足的结果。" />
      {conditions.length === 0 ? <EmptyMini>没有完成条件。</EmptyMini> : <div className="workflow-condition-list">{conditions.map((condition, index) => <div key={`${condition}:${index}`}><span>{index + 1}</span><p>{condition}</p></div>)}</div>}
    </section>

    <details className="workflow-secondary-details">
      <summary>匹配与技术信息</summary>
      <div className="workflow-secondary-body">
        <section>
          <SectionTitle title="匹配规则" subtitle="模型用这些信号判断是否使用该模板。" />
          {keywords.length > 0 && <ChipRow values={keywords} />}
          {matchRows.length === 0 ? <EmptyMini>没有匹配规则。</EmptyMini> : <div className="workflow-match-grid">{matchRows.map((row) => <div key={row.label}><span>{row.label}</span><p>{row.values.join(' · ')}</p></div>)}</div>}
        </section>
        <section className="workflow-runtime-grid is-compact">
          <InfoTile label="模板 ID" value={parsed.id || selected.id} />
          <InfoTile label="文件名" value={selected.file_name} />
          <InfoTile label="版本数" value={String(selected.version_count ?? 1)} />
          <InfoTile label="历史版本" value={String(selected.retired_count ?? 0)} />
          <InfoTile label="JSON" value={parsed.error || '可解析'} />
        </section>
        <div className="workflow-technical-actions"><button type="button" className="nx-button is-secondary" onClick={onCopy}><Copy size={15} />复制模板路径</button></div>
        <details className="workflow-runtime-json"><summary><Check size={13} />查看 Runtime 原始 JSON</summary><pre>{JSON.stringify(raw, null, 2)}</pre></details>
      </div>
    </details>
  </article>;
}

function StepCard({ step, index }: { step: StepView; index: number }) {
  return <div className="workflow-step-card"><div><span>{index}</span><strong>{step.title || step.id || `步骤 ${index}`}</strong></div><footer><em>{step.phase || '未分阶段'}</em>{step.required && <em>必需</em>}{step.depends.length > 0 && <em>依赖 {step.depends.join(', ')}</em>}{step.substitution && <em>{step.substitution}</em>}</footer></div>;
}

function StatusPill({ tone, children }: { tone: Tone; children: ReactNode }) {
  return <span className={`status-badge tone-${tone}`}><span />{children}</span>;
}

function InfoTile({ label, value }: { label: string; value: string }) {
  return <div className="workflow-info-tile"><span>{label}</span><strong>{value || '暂无'}</strong></div>;
}

function SectionTitle({ title, subtitle }: { title: string; subtitle: string }) {
  return <header className="workflow-section-title"><div><h4>{title}</h4><p>{subtitle}</p></div></header>;
}

function ChipRow({ values }: { values: string[] }) {
  return <div className="workflow-chip-row">{values.slice(0, 18).map((value) => <span key={value}>{value}</span>)}</div>;
}

function EmptyMini({ children }: { children: ReactNode }) {
  return <p className="empty-mini">{children}</p>;
}

function record(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {};
}

function array(value: unknown): unknown[] {
  return Array.isArray(value) ? value : [];
}

function text(value: unknown): string {
  if (typeof value === 'string') return value;
  if (typeof value === 'number' || typeof value === 'boolean') return String(value);
  return '';
}

function stringValues(value: unknown): string[] {
  if (Array.isArray(value)) return value.flatMap((item) => { const current = text(item); return current ? [current] : []; });
  const single = text(value);
  return single ? [single] : [];
}

function groupSteps(steps: StepView[]): StepGroup[] {
  const groups = new Map<string, StepView[]>();
  for (const step of steps) {
    const phase = step.phase || '未分阶段';
    groups.set(phase, [...(groups.get(phase) || []), step]);
  }
  return Array.from(groups.entries()).map(([phase, groupedSteps]) => ({ phase, steps: groupedSteps }));
}

function stepViews(value: unknown): StepView[] {
  return array(value).map((item) => {
    const body = record(item);
    return {
      id: text(body.id),
      title: text(body.title) || text(body.name),
      phase: text(body.phase),
      required: body.required === true,
      depends: stringValues(body.depends_on || body.depends),
      substitution: text(body.substitution),
    };
  });
}

function matchViews(match: Record<string, unknown>): MatchView[] {
  const labels: Record<string, string> = { keywords: '关键词', devices: '运行端', task_types: '任务类型', projects: '项目', tools: '工具', skills: 'Skill', priority: '优先级' };
  return Object.entries(match).reduce<MatchView[]>((rows, [key, value]) => {
    const values = stringValues(value);
    if (values.length > 0) rows.push({ label: labels[key] || key, values });
    return rows;
  }, []);
}
