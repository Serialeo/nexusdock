import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { Check, CheckCircle2, Circle, Clock3, Copy, FileText, Layers, LoaderCircle, RefreshCw, Search, ShieldAlert, Trash2 } from 'lucide-react';
import { ApiError, api } from '../../api/client';
import { formatTime } from '../../lib/time';
import { buildTaskListQuery, collectTaskSelection, deleteTaskBatch, type TaskFilters, type TaskTimeField, type TaskTimeRange } from './taskListModel';
import './task-center.css';
import Dialog from '../Dialog';
import MobileDrilldownBar from '../MobileDrilldownBar';
import CheckpointPromptPanel from './CheckpointPromptPanel';

type Tone = 'ok' | 'warn' | 'danger' | 'muted';
type TaskStatus = 'all' | 'active' | 'completed' | 'blocked';

const taskStatusLabels: Record<TaskStatus, string> = { all: '全部', active: '进行中', completed: '已完成', blocked: '阻塞' };
const runtimeTaskListLimit = 200;
const taskPollIntervalMs = 2000;
function taskStatusLabel(status?: string): string { return taskStatusLabels[status as TaskStatus] || status || '未知'; }

type TaskStep = { id: string; title: string; status: string };
type OpsTask = { id: string; title: string; goal: string; status: string; summary?: string; blocker?: string; current_step?: TaskStep; completed_step_count: number; step_count: number; updated_at: string; created_at: string; file_name: string };
type OpsTaskDetail = OpsTask & { steps?: unknown[] };
type OpsSkill = { id: string; title: string; source: string; path: string; description?: string; updated_at: string; file_count: number; status: string; active_version?: string; versions?: string[]; channels?: Record<string, string>; runtime_state_path?: string; doc_root?: string };
type OpsSkillFile = { path: string; kind: string; size_bytes: number; updated_at: string };
type OpsSkillDetail = OpsSkill & { root?: string; skill_doc?: string; files?: OpsSkillFile[]; runtime_state?: Record<string, unknown> };
type TaskCounts = { all: number; active: number; blocked: number; completed: number };
type TaskListResponse = { ok: boolean; items: OpsTask[]; count: number; total: number; offset: number; limit: number; has_more: boolean; counts: TaskCounts; root?: string; source?: string };
type TaskDetailResponse = { ok: boolean; task: OpsTaskDetail; source?: string };
type DeleteTaskResponse = { ok: boolean; task_id: string; deleted_task?: OpsTask; source?: string };
type SkillsResponse = { ok: boolean; items: OpsSkill[]; count: number; root?: string; source?: string };
type SkillDetailResponse = { ok: boolean; skill: OpsSkillDetail; source?: string };
type SkillFileContent = OpsSkillFile & { content: string; truncated: boolean };
type SkillFileResponse = { ok: boolean; file?: SkillFileContent };
type SkillEnvEntry = { key: string; configured: boolean };
type SkillEnvResponse = { ok: boolean; items: SkillEnvEntry[]; count: number; skill_id: string; source: string };
type SkillManageAction = 'activate' | 'rollback' | 'env_set' | 'env_unset';

const emptyTasks: TaskListResponse = { ok: false, items: [], count: 0, total: 0, offset: 0, limit: runtimeTaskListLimit, has_more: false, counts: { all: 0, active: 0, blocked: 0, completed: 0 } };
function formatBytes(value?: number): string { if (value === undefined) return '暂无'; const units = ['B', 'KiB', 'MiB', 'GiB']; let size = value; let unit = 0; while (size >= 1024 && unit < units.length - 1) { size /= 1024; unit += 1; } return `${size.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`; }
function apiMessage(error: unknown): string { if (error instanceof ApiError) return `${error.code || error.status}：${error.message}`; return error instanceof Error ? error.message : '请求失败'; }
function toneForTask(task: Pick<OpsTask, 'status'>): Tone { if (task.status === 'completed') return 'ok'; if (task.status === 'blocked') return 'danger'; if (task.status === 'active') return 'warn'; return 'muted'; }
function taskDisplayTitle(task?: Pick<OpsTask, 'title' | 'goal' | 'id'>): string {
  const title = task?.title?.trim();
  if (title) return title;
  const goal = task?.goal?.trim();
  if (goal) return goal.split(/[。.!?！？\n]/)[0] || goal;
  return task?.id || '未命名任务';
}

type TaskProgressState = { completed: number; total: number; percent: number; determinate: boolean; label: string };
function taskProgress(task: Pick<OpsTask, 'status' | 'completed_step_count' | 'step_count'>): TaskProgressState {
  const total = Math.max(0, Number(task.step_count) || 0);
  if (total === 0) {
    return { completed: 0, total: 0, percent: 0, determinate: false, label: task.status === 'completed' ? '已完成' : '未拆分步骤' };
  }
  const reported = Math.max(0, Number(task.completed_step_count) || 0);
  const completed = task.status === 'completed' ? total : Math.min(reported, total);
  return { completed, total, percent: Math.round((completed / total) * 100), determinate: true, label: `${completed} / ${total}` };
}

function taskCurrentText(task: OpsTask): string {
  if (task.status === 'blocked' && task.blocker) return `阻塞：${task.blocker}`;
  if (task.current_step?.title) return `当前：${task.current_step.title}`;
  if (task.summary) return task.summary;
  if (task.status === 'completed') return '任务已完成';
  return task.step_count > 0 ? '等待下一步' : '未拆分执行步骤';
}
function toneForStatus(status?: string): Tone { if (!status) return 'muted'; if (['ok', 'healthy', 'available', 'installed', 'active', 'success', 'completed'].includes(status)) return 'ok'; if (['failed', 'blocked', 'offline', 'unknown'].includes(status)) return 'danger'; if (['pending', 'draft', 'running', 'degraded'].includes(status)) return 'warn'; return 'muted'; }

type ReloadOptions = { silent?: boolean };

function useOpsResource<T>(path: string, fallback: T, refreshToken: number) {
  const fallbackRef = useRef(fallback);
  const silentReloadRef = useRef(false);
  fallbackRef.current = fallback;
  const [localToken, setLocalToken] = useState(0);
  const [state, setState] = useState<{ data: T; loading: boolean; error?: string }>({ data: fallback, loading: true });
  useEffect(() => {
    const silent = silentReloadRef.current;
    silentReloadRef.current = false;
    let cancelled = false;
    if (!silent) setState((current) => ({ ...current, loading: true, error: undefined }));
    api<T>(path).then((data) => { if (!cancelled) setState({ data, loading: false }); }).catch((error) => { if (!cancelled) setState((current) => ({ data: current.data, loading: false, error: apiMessage(error) })); });
    return () => { cancelled = true; };
  }, [path, refreshToken, localToken]);
  const reload = useCallback((options: ReloadOptions = {}) => {
    silentReloadRef.current = Boolean(options.silent);
    setLocalToken((value) => value + 1);
  }, []);
  return { ...state, reload };
}

function useOptionalOpsResource<T>(path: string, fallback: T, refreshToken: number) {
  const fallbackRef = useRef(fallback);
  const silentReloadRef = useRef(false);
  const inFlightRef = useRef(false);
  fallbackRef.current = fallback;
  const [localToken, setLocalToken] = useState(0);
  const [state, setState] = useState<{ data: T; loading: boolean; error?: string }>({ data: fallback, loading: false });
  useEffect(() => {
    if (!path) {
      setState({ data: fallbackRef.current, loading: false });
      return undefined;
    }
    const silent = silentReloadRef.current;
    silentReloadRef.current = false;
    let cancelled = false;
    const controller = new AbortController();
    inFlightRef.current = true;
    if (!silent) setState((current) => ({ ...current, loading: true, error: undefined }));
    api<T>(path, { signal: controller.signal }).then((data) => { if (!cancelled) setState({ data, loading: false }); }).catch((error) => { if (!cancelled) setState((current) => ({ data: current.data, loading: false, error: apiMessage(error) })); }).finally(() => { if (!cancelled) inFlightRef.current = false; });
    return () => { cancelled = true; inFlightRef.current = false; controller.abort(); };
  }, [path, refreshToken, localToken]);
  const reload = useCallback((options: ReloadOptions = {}) => {
    if (options.silent && inFlightRef.current) return;
    silentReloadRef.current = Boolean(options.silent);
    setLocalToken((value) => value + 1);
  }, []);
  return { ...state, reload };
}

export function TaskCenterPage({ nodeID, refreshToken }: { nodeID: string; refreshToken: number }) {
  const [filters, setFilters] = useState<TaskFilters>({ status: 'active', query: '', timeField: 'updated_at', timeRange: '24h', fromDate: '', toDate: '' });
  const [offset, setOffset] = useState(0);
  const [selectedId, setSelectedId] = useState('');
  const [checked, setChecked] = useState<Map<string, OpsTask>>(() => new Map());
  const [mobileDetailOpen, setMobileDetailOpen] = useState(false);
  const [checkpointPromptOpen, setCheckpointPromptOpen] = useState(false);
  const [pendingDelete, setPendingDelete] = useState<OpsTask[] | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [deleteProgress, setDeleteProgress] = useState({ completed: 0, total: 0 });
  const [deleteFailures, setDeleteFailures] = useState<{ id: string; error: string }[]>([]);
  const [deleteSummary, setDeleteSummary] = useState('');
  const [selectingAll, setSelectingAll] = useState(false);
  const [selectionProgress, setSelectionProgress] = useState({ count: 0, total: 0 });
  const [actionError, setActionError] = useState('');
  const [notice, setNotice] = useState('');
  const [list, setList] = useState<{ key: string; data: TaskListResponse; loading: boolean; error?: string }>({ key: '', data: emptyTasks, loading: true });
  const listAbort = useRef<AbortController | null>(null);
  const actionAbort = useRef<AbortController | null>(null);
  const listSequence = useRef(0);
  const listInFlight = useRef(false);
  const selectPageRef = useRef<HTMLInputElement>(null);
  const runtimeBase = `/v1/runtime/nodes/${encodeURIComponent(nodeID)}`;
  const requestKey = `${runtimeBase}:${JSON.stringify(filters)}:${offset}`;
  const currentList = list.key === requestKey ? list : { data: emptyTasks, loading: true, error: undefined };
  const tasks = currentList.data.items;
  const total = currentList.data.total;
  const selected = tasks.find((task) => task.id === selectedId) || tasks[0];
  const detail = useOptionalOpsResource<TaskDetailResponse>(selected ? `${runtimeBase}/tasks/${encodeURIComponent(selected.id)}` : '', { ok: false, task: selected as OpsTaskDetail }, refreshToken);
  const operationBusy = deleting || selectingAll || pendingDelete !== null;
  const pollingPaused = operationBusy || checked.size > 0;
  const checkedOnPage = tasks.filter((task) => checked.has(task.id)).length;
  const allPageChecked = tasks.length > 0 && checkedOnPage === tasks.length;
  let filterError = '';
  try { buildTaskListQuery(filters, offset, runtimeTaskListLimit); } catch (error) { filterError = apiMessage(error); }

  const loadTasks = useCallback(async ({ silent = false }: ReloadOptions = {}) => {
    if (silent && listInFlight.current) return;
    const sequence = ++listSequence.current;
    listInFlight.current = true;
    listAbort.current?.abort();
    const controller = new AbortController();
    listAbort.current = controller;
    setList((previous) => ({ key: requestKey, data: silent && previous.key === requestKey ? previous.data : emptyTasks, loading: !silent, error: undefined }));
    try {
      const query = buildTaskListQuery(filters, offset, runtimeTaskListLimit);
      const data = await api<TaskListResponse>(`${runtimeBase}/tasks?${query}`, { signal: controller.signal });
      if (sequence !== listSequence.current || controller.signal.aborted) return;
      if (offset > 0 && offset >= data.total) {
        setOffset(Math.max(0, Math.floor((data.total - 1) / runtimeTaskListLimit) * runtimeTaskListLimit));
        return;
      }
      setList({ key: requestKey, data, loading: false });
    } catch (error) {
      if (sequence !== listSequence.current || controller.signal.aborted) return;
      setList((previous) => ({ ...previous, key: requestKey, loading: false, error: apiMessage(error) }));
    } finally {
      if (sequence === listSequence.current) listInFlight.current = false;
    }
  }, [filters, offset, requestKey, runtimeBase]);

  useEffect(() => {
    void loadTasks();
    return () => { listSequence.current += 1; listInFlight.current = false; listAbort.current?.abort(); };
  }, [loadTasks, refreshToken]);

  useEffect(() => {
    if (pollingPaused) { listSequence.current += 1; listInFlight.current = false; listAbort.current?.abort(); }
  }, [pollingPaused]);

  useEffect(() => {
    if (pollingPaused) return;
    const refreshVisibleTasks = () => {
      if (document.visibilityState === 'visible') {
        void loadTasks({ silent: true });
        detail.reload({ silent: true });
      }
    };
    const timer = window.setInterval(refreshVisibleTasks, taskPollIntervalMs);
    document.addEventListener('visibilitychange', refreshVisibleTasks);
    return () => {
      window.clearInterval(timer);
      document.removeEventListener('visibilitychange', refreshVisibleTasks);
    };
  }, [loadTasks, detail.reload, pollingPaused]);

  useEffect(() => () => { actionAbort.current?.abort(); }, [nodeID]);
  useEffect(() => {
    if (selectPageRef.current) selectPageRef.current.indeterminate = checkedOnPage > 0 && !allPageChecked;
  }, [allPageChecked, checkedOnPage]);

  function changeFilters(patch: Partial<TaskFilters>) {
    setFilters((previous) => ({ ...previous, ...patch }));
    setOffset(0);
    setChecked(new Map());
    setSelectedId('');
    setMobileDetailOpen(false);
    setActionError('');
  }

  function toggleTask(task: OpsTask) {
    setChecked((previous) => {
      const next = new Map(previous);
      if (next.has(task.id)) next.delete(task.id); else next.set(task.id, task);
      return next;
    });
  }

  function togglePage() {
    setChecked((previous) => {
      const next = new Map(previous);
      for (const task of tasks) {
        if (allPageChecked) next.delete(task.id); else next.set(task.id, task);
      }
      return next;
    });
  }

  async function selectAllMatching() {
    if (operationBusy || currentList.loading || currentList.error || filterError) return;
    const controller = new AbortController();
    actionAbort.current = controller;
    setSelectingAll(true);
    setSelectionProgress({ count: 0, total });
    setActionError('');
    const now = Date.now();
    try {
      // 先完整读取并固定 ID，确认后再删除；边翻页边删除会使 offset 跳过记录。
      const selection = await collectTaskSelection<OpsTask>(async (pageOffset) => {
        const query = buildTaskListQuery(filters, pageOffset, runtimeTaskListLimit, now);
        return api<TaskListResponse>(`${runtimeBase}/tasks?${query}`, { signal: controller.signal });
      }, (count, countTotal) => { if (!controller.signal.aborted) setSelectionProgress({ count, total: countTotal }); });
      if (controller.signal.aborted) return;
      setChecked(new Map(selection.map((task) => [task.id, task])));
      setNotice(`已选择全部 ${selection.length} 条筛选结果。`);
    } catch (error) {
      if (!controller.signal.aborted) setActionError(apiMessage(error));
    } finally {
      if (!controller.signal.aborted) setSelectingAll(false);
    }
  }

  function requestDelete(selection: OpsTask[]) {
    if (selection.length === 0 || operationBusy) return;
    setPendingDelete(selection.map((task) => ({ ...task })));
    setDeleteFailures([]);
    setDeleteSummary('');
    setDeleteProgress({ completed: 0, total: selection.length });
    setActionError('');
  }

  async function confirmDelete() {
    if (!pendingDelete || deleting) return;
    const snapshot = pendingDelete;
    const controller = new AbortController();
    actionAbort.current = controller;
    setDeleting(true);
    setDeleteFailures([]);
    setDeleteProgress({ completed: 0, total: snapshot.length });
    const result = await deleteTaskBatch(snapshot.map((task) => task.id), async (id) => {
      const response = await api<DeleteTaskResponse>(`${runtimeBase}/tasks/${encodeURIComponent(id)}`, { method: 'DELETE', signal: controller.signal });
      if (!response.ok) throw new Error('服务未确认删除成功。');
    }, (completed, countTotal) => { if (!controller.signal.aborted) setDeleteProgress({ completed, total: countTotal }); }, controller.signal);
    if (controller.signal.aborted) return;
    const deleted = new Set(result.deleted);
    const failures = new Set(result.failed.map((item) => item.id));
    setChecked((previous) => new Map([...previous].filter(([id]) => !deleted.has(id))));
    setList((previous) => ({ ...previous, data: { ...previous.data, items: previous.data.items.filter((task) => !deleted.has(task.id)) } }));
    if (selected && deleted.has(selected.id)) { setSelectedId(''); setMobileDetailOpen(false); }
    const message = result.failed.length > 0
      ? `已删除 ${result.deleted.length} 条，${result.failed.length} 条失败。可重试失败项。`
      : `已删除 ${result.deleted.length} 条任务记录。`;
    setNotice(message);
    setDeleteSummary(message);
    setDeleteFailures(result.failed.map((item) => ({ id: item.id, error: apiMessage(item.error) })));
    setPendingDelete(result.failed.length > 0 ? snapshot.filter((task) => failures.has(task.id)) : null);
    setDeleting(false);
    void loadTasks({ silent: true });
  }

  const listUnavailable = currentList.loading || Boolean(currentList.error) || Boolean(filterError);
  const activeDeleteCount = pendingDelete?.filter((task) => task.status === 'active').length || 0;

  return <>
    <OpsShell error={filterError || currentList.error}>
      {notice && <div className={`nx-alert ${deleteFailures.length > 0 ? 'is-info' : 'is-success'}`} role="status">{notice}<button type="button" onClick={() => setNotice('')}>关闭</button></div>}
      {actionError && <div className="nx-alert is-error" role="alert">{actionError}</div>}
      <div className="ops-task-panel">
        <div className="ops-task-primary-row">
          <div className="ops-segmented">{(['active', 'blocked', 'completed', 'all'] as TaskStatus[]).map((status) => <button type="button" key={status} className={filters.status === status ? 'is-active' : ''} aria-pressed={filters.status === status} disabled={operationBusy} onClick={() => changeFilters({ status })}><span>{taskStatusLabels[status]}</span><em>{currentList.data.counts[status]}</em></button>)}</div>
          <label className="ops-search"><Search size={14} /><input aria-label="搜索任务" value={filters.query} disabled={operationBusy} onChange={(event) => changeFilters({ query: event.target.value })} placeholder="搜索任务或当前步骤" /></label>
          <div className="ops-task-head-actions">
            <button type="button" className="nx-button is-secondary is-small" disabled={operationBusy} onClick={() => setCheckpointPromptOpen(true)}>Checkpoint 提示词</button>
            <button type="button" className="nx-button is-secondary is-small" disabled={operationBusy || Boolean(filterError)} onClick={() => { setChecked(new Map()); void loadTasks(); detail.reload(); }}>刷新</button>
          </div>
        </div>
        <div className="ops-task-secondary-row">
          <div className="ops-task-filters-inline" aria-label="任务时间筛选">
            <label><span>时间依据</span><select value={filters.timeField} disabled={operationBusy} onChange={(event) => changeFilters({ timeField: event.target.value as TaskTimeField })}><option value="updated_at">更新时间</option><option value="created_at">创建时间</option></select></label>
            <label><span>时间范围</span><select value={filters.timeRange} disabled={operationBusy} onChange={(event) => changeFilters({ timeRange: event.target.value as TaskTimeRange })}><option value="all">全部时间</option><option value="24h">最近 24 小时</option><option value="7d">最近 7 天</option><option value="30d">最近 30 天</option><option value="before30d">30 天以前</option><option value="custom">自定义日期</option></select></label>
            {filters.timeRange === 'custom' && <>
              <label><span>开始</span><input type="date" aria-label="开始日期" value={filters.fromDate} disabled={operationBusy} onChange={(event) => changeFilters({ fromDate: event.target.value })} /></label>
              <label><span>结束</span><input type="date" aria-label="结束日期" value={filters.toDate} disabled={operationBusy} onChange={(event) => changeFilters({ toDate: event.target.value })} /></label>
            </>}
            <button type="button" className="nx-button is-secondary is-small" disabled={operationBusy} onClick={() => changeFilters({ status: 'all', query: '', timeRange: 'all', fromDate: '', toDate: '' })}>清除筛选</button>
          </div>
          <div className="ops-task-selection-inline" role="group" aria-label="批量选择任务">
            <label><input ref={selectPageRef} type="checkbox" aria-label="全选当前页任务" checked={allPageChecked} disabled={operationBusy || listUnavailable || tasks.length === 0} onChange={togglePage} />全选本页</label>
            <span>已选 <strong>{checked.size}</strong> 条 · 筛选结果 {total} 条</span>
            {total > tasks.length && <button type="button" className="nx-button is-secondary is-small" disabled={operationBusy || listUnavailable} onClick={() => { void selectAllMatching(); }}>全选筛选结果（{total}）</button>}
            {checked.size > 0 && <button type="button" className="nx-button is-secondary is-small" disabled={operationBusy} onClick={() => setChecked(new Map())}>取消选择</button>}
            <button type="button" className="nx-button is-danger is-small ops-task-bulk-delete" disabled={operationBusy || listUnavailable || checked.size === 0} onClick={() => requestDelete([...checked.values()])}><Trash2 size={13} />删除所选（{checked.size}）</button>
            {selectingAll && <span role="status"><LoaderCircle size={13} className="is-spinning" />正在选择 {selectionProgress.count} / {selectionProgress.total} 条…</span>}
          </div>
        </div>
      </div>
      <section className={`ops-master-detail mobile-drilldown ${mobileDetailOpen ? 'is-detail-open' : 'is-list-open'}`}>
        <div className="ops-task-browser mobile-drilldown-list">
          <div className="ops-task-rail" aria-busy={currentList.loading}>
            {currentList.loading && !filterError ? <EmptyOps text="正在读取任务…" /> : tasks.length === 0 ? <EmptyOps text={filterError ? '请调整日期范围。' : '没有匹配任务，请调整状态或时间筛选。'} /> : tasks.map((task) => <div key={task.id} className={`ops-task-row ${checked.has(task.id) ? 'is-checked' : ''}`}>
              <label className="ops-task-check"><input type="checkbox" aria-label={`选择任务 ${taskDisplayTitle(task)}`} checked={checked.has(task.id)} disabled={operationBusy || listUnavailable} onChange={() => toggleTask(task)} /></label>
              <button type="button" className={`ops-task-line ${selected?.id === task.id ? 'is-selected' : ''}`} aria-pressed={selected?.id === task.id} onClick={() => { setSelectedId(task.id); setMobileDetailOpen(true); }}>
                <span className="ops-task-line-title"><strong>{taskDisplayTitle(task)}</strong><span className={`ops-task-state tone-${toneForTask(task)}`}>{taskStatusLabel(task.status)}</span></span>
                <TaskProgress task={task} compact /><small>{taskCurrentText(task)}</small>
                <small>{filters.timeField === 'created_at' ? '创建' : '更新'}：<time dateTime={task[filters.timeField]}>{formatTime(task[filters.timeField], { compact: true })}</time></small>
              </button>
            </div>)}
          </div>
          <nav className="ops-task-pagination" aria-label="任务分页">
            <span>{total > 0 ? `${offset + 1}–${offset + tasks.length} / ${total}` : '0 条任务'}</span>
            <button type="button" className="nx-button is-secondary is-small" disabled={operationBusy || listUnavailable || offset === 0} onClick={() => { setOffset(Math.max(0, offset - runtimeTaskListLimit)); setSelectedId(''); setMobileDetailOpen(false); }}>上一页</button>
            <button type="button" className="nx-button is-secondary is-small" disabled={operationBusy || listUnavailable || !currentList.data.has_more} onClick={() => { setOffset(offset + runtimeTaskListLimit); setSelectedId(''); setMobileDetailOpen(false); }}>下一页</button>
          </nav>
        </div>
        <div className="mobile-drilldown-detail">
          {selected && <MobileDrilldownBar label="任务详情" title={taskDisplayTitle(selected)} meta={taskStatusLabel(selected.status)} backLabel="返回任务列表" onBack={() => setMobileDetailOpen(false)} />}
          <TaskDetail task={selected} detail={detail.data.task} loading={detail.loading} error={detail.error} deleting={deleting} deleteDisabled={operationBusy || listUnavailable} onDelete={(task) => requestDelete([task])} />
        </div>
      </section>
    </OpsShell>
    {checkpointPromptOpen && <Dialog title="Checkpoint 提示词" description="配置任务保存断点时交付给 AI 的提示词。" wide onClose={() => setCheckpointPromptOpen(false)}>
      <CheckpointPromptPanel />
    </Dialog>}
    {pendingDelete && <Dialog title={deleteFailures.length > 0 ? '部分任务删除失败' : pendingDelete.length > 1 ? '批量删除任务' : '删除任务'} description="任务记录和步骤将被永久删除，无法恢复。此操作不会终止进程或删除工作目录。" closeDisabled={deleting} onClose={() => setPendingDelete(null)}>
      <div className="ops-delete-dialog">
        <p>确定删除以下 <strong>{pendingDelete.length}</strong> 条任务记录？</p>
        {activeDeleteCount > 0 && <div className="nx-alert is-info">其中 {activeDeleteCount} 条任务仍标记为进行中。</div>}
        <ul className="ops-task-delete-preview">{pendingDelete.slice(0, 10).map((task) => <li key={task.id}><strong>{taskDisplayTitle(task)}</strong><code>{task.id}</code></li>)}</ul>
        {pendingDelete.length > 10 && <p>以及另外 {pendingDelete.length - 10} 条所选任务。</p>}
        {deleteSummary && <p role="status">{deleteSummary}</p>}
        {deleteFailures.length > 0 && <ul className="ops-task-delete-errors" role="alert">{deleteFailures.map((failure) => <li key={failure.id}><code>{failure.id}</code>：{failure.error}</li>)}</ul>}
        {deleting && <p role="status" aria-live="polite">正在删除 {deleteProgress.completed} / {deleteProgress.total} 条…</p>}
        <div className="nx-dialog-actions">
          <button type="button" className="nx-button is-secondary" data-dialog-initial-focus onClick={() => setPendingDelete(null)} disabled={deleting}>{deleteFailures.length > 0 ? '关闭' : '取消'}</button>
          <button type="button" className="nx-button is-danger" aria-busy={deleting} onClick={() => { void confirmDelete(); }} disabled={deleting}><Trash2 size={15} />{deleting ? '正在删除…' : deleteFailures.length > 0 ? `重试失败项（${pendingDelete.length}）` : `确认删除 ${pendingDelete.length} 条`}</button>
        </div>
      </div>
    </Dialog>}
  </>;
}


export function SkillsPage({ nodeID, refreshToken }: { nodeID: string; refreshToken: number }) {
  const runtimeBase = `/v1/runtime/nodes/${encodeURIComponent(nodeID)}`;
  const resource = useOpsResource<SkillsResponse>(`${runtimeBase}/skills`, { ok: false, items: [], count: 0, root: '' }, refreshToken);
  const [query, setQuery] = useState('');
  const [selectedKey, setSelectedKey] = useState('');
  const [mobileDetailOpen, setMobileDetailOpen] = useState(false);
  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) return resource.data.items;
    return resource.data.items.filter((item) => [item.id, item.title, item.description, item.active_version].filter(Boolean).join(' ').toLowerCase().includes(needle));
  }, [query, resource.data.items]);
  const selected = filtered.find((item) => `${item.source}:${item.id}` === selectedKey) || filtered[0];
  const detail = useOptionalOpsResource<SkillDetailResponse>(selected ? `${runtimeBase}/skills/${encodeURIComponent(selected.source)}/${encodeURIComponent(selected.id)}` : '', { ok: false, skill: selected as OpsSkillDetail }, refreshToken);
  return <OpsShell error={resource.error}>
    <section className={`skills-workspace mobile-drilldown ${mobileDetailOpen ? 'is-detail-open' : 'is-list-open'}`}>
      <aside className="skills-catalog mobile-drilldown-list">
        <header className="skills-catalog-head">
          <label className="ops-search"><Search size={15} /><input aria-label="搜索 Skill" value={query} onChange={(event) => { setQuery(event.target.value); setMobileDetailOpen(false); }} placeholder="搜索名称或说明" /></label>
        </header>
        <div className="skills-rail">
          {filtered.length === 0 ? <EmptyOps text="没有匹配的 Skill。" /> : filtered.map((skill) => <button type="button" key={`${skill.source}:${skill.id}`} className={`skill-list-item ${selected?.source === skill.source && selected?.id === skill.id ? 'is-selected' : ''}`} aria-pressed={selected?.source === skill.source && selected?.id === skill.id} onClick={() => { setSelectedKey(`${skill.source}:${skill.id}`); setMobileDetailOpen(true); }}><span className="ops-card-icon"><Layers size={16} /></span><span><strong>{skill.title || skill.id}</strong><small>{skill.active_version || '未标记版本'} · {skill.file_count > 0 ? `${skill.file_count} 个文件` : '文件按需读取'}</small></span></button>)}
        </div>
      </aside>
      <div className="skills-detail mobile-drilldown-detail">
        {selected && <MobileDrilldownBar label="Skill" title={selected.title || selected.id} meta={selected.active_version || selected.status} backLabel="返回 Skill 列表" onBack={() => setMobileDetailOpen(false)} />}
        <SkillDetail nodeID={nodeID} skill={selected} detail={detail.data.skill} loading={detail.loading} error={detail.error} refreshToken={refreshToken} onChanged={() => { resource.reload({ silent: true }); detail.reload(); }} />
      </div>
    </section>
  </OpsShell>;
}

function TaskDetail({ task, detail, loading, error, deleting, deleteDisabled, onDelete }: { task?: OpsTask; detail?: OpsTaskDetail; loading: boolean; error?: string; deleting: boolean; deleteDisabled?: boolean; onDelete: (task: OpsTask) => void }) {
  if (!task) return <article className="ops-detail-empty"><EmptyOps text="请选择一个任务。" /></article>;
  // 切换任务时详情请求可能尚未返回，不能显示或删除上一个任务。
  const matchingDetail = detail?.id === task.id ? detail : undefined;
  const full = matchingDetail || task;
  const steps = taskSteps(matchingDetail?.steps);
  const currentStep = full.current_step || steps.find((step) => step.status === 'in_progress') || steps.find((step) => step.status === 'pending');
  const currentTitle = currentStep?.title || (full.status === 'completed' ? '任务已完成' : full.status === 'blocked' ? '任务已阻塞' : '等待下一步');
  return <article className="ops-task-detail">
    <header>
      <div><span>任务</span><h3>{taskDisplayTitle(full)}</h3>{full.goal && <p>{full.goal}</p>}</div>
      <div className="ops-task-detail-actions">
        <StatusBadge tone={toneForTask(full)}>{taskStatusLabel(full.status)}</StatusBadge>
        <button type="button" className="nx-button is-danger is-small" aria-label={`删除任务 ${taskDisplayTitle(full)}`} onClick={() => onDelete(full)} disabled={deleting || deleteDisabled}><Trash2 size={15} />{deleting ? '删除中…' : '删除'}</button>
      </div>
    </header>
    {loading && <div className="nx-alert is-info">正在读取任务详情…</div>}
    {error && <div className="nx-alert is-error">{error}</div>}
    {full.blocker && <div className="ops-blocker"><ShieldAlert size={15} />{full.blocker}</div>}
    <TaskProgress task={full} />
    <section className="ops-current-step" aria-label="当前进展">
      <span>{full.status === 'completed' ? '结果' : full.status === 'blocked' ? '当前状态' : '当前步骤'}</span>
      <strong>{currentTitle}</strong>
      {full.summary && full.summary !== currentTitle && <p>{full.summary}</p>}
    </section>
    <TaskStepList steps={steps} status={full.status} />
    <footer className="ops-task-updated">更新于 {formatTime(full.updated_at)}</footer>
  </article>;
}

function SkillDetail({ nodeID, skill, detail, loading, error, refreshToken, onChanged }: { nodeID: string; skill?: OpsSkill; detail?: OpsSkillDetail; loading: boolean; error?: string; refreshToken: number; onChanged: () => void }) {
  if (!skill) return <article className="ops-detail-empty"><EmptyOps text="请选择一个 Skill。" /></article>;
  return <SkillDetailContent nodeID={nodeID} skill={skill} detail={detail} loading={loading} error={error} refreshToken={refreshToken} onChanged={onChanged} />;
}

function SkillDetailContent({ nodeID, skill, detail, loading, error, refreshToken, onChanged }: { nodeID: string; skill: OpsSkill; detail?: OpsSkillDetail; loading: boolean; error?: string; refreshToken: number; onChanged: () => void }) {
  const [selectedPath, setSelectedPath] = useState('');
  const [copied, setCopied] = useState(false);
  const full = detail?.id ? detail : skill;
  const files = detail?.files || [];
  const preferredPath = files.find((file) => file.path.toLowerCase() === 'skill.md')?.path || files[0]?.path || '';
  const activePath = files.some((file) => file.path === selectedPath) ? selectedPath : preferredPath;
  const fileURL = activePath ? `/v1/runtime/nodes/${encodeURIComponent(nodeID)}/skills/${encodeURIComponent(full.source)}/${encodeURIComponent(full.id)}/files/${encodePathSegments(activePath)}` : '';
  const preview = useOptionalOpsResource<SkillFileResponse>(fileURL, { ok: false }, refreshToken);
  const raw = detail?.runtime_state;
  const manifest = asRecord(raw?.manifest);
  const selection = asRecord(raw?.selection);
  const manageBase = full.source === 'agentdock-api' ? `/v1/runtime/nodes/${encodeURIComponent(nodeID)}/skills/${encodeURIComponent(full.source)}/${encodeURIComponent(full.id)}` : '';
  const environment = useOptionalOpsResource<SkillEnvResponse>(manageBase ? `${manageBase}/environment` : '', { ok: false, items: [], count: 0, skill_id: full.id, source: full.source }, refreshToken);
  const [envKey, setEnvKey] = useState('');
  const [envValue, setEnvValue] = useState('');
  const [managing, setManaging] = useState('');
  const [manageError, setManageError] = useState('');
  const [manageNotice, setManageNotice] = useState('');

  async function manageSkill(action: SkillManageAction, payload: Record<string, unknown> = {}) {
    if (!manageBase || managing) return;
    setManaging(action);
    setManageError('');
    setManageNotice('');
    try {
      await api(`${manageBase}/manage`, { method: 'POST', body: JSON.stringify({ action, ...payload }), timeoutMs: 15_000 });
      setManageNotice(action === 'activate' ? '已激活所选 Skill 版本。' : action === 'rollback' ? '已回滚到上一已安装版本。' : action === 'env_set' ? '环境变量已保存；值不会从 API 回显。' : '环境变量已删除。');
      if (action === 'env_set') { setEnvKey(''); setEnvValue(''); }
      environment.reload();
      onChanged();
    } catch (cause) {
      setManageError(apiMessage(cause));
    } finally {
      setManaging('');
    }
  }

  function handleCopy() {
    if (preview.data.file?.content) {
      void navigator.clipboard.writeText(preview.data.file.content);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    }
  }

  return <article className="skill-detail-panel">
    <header className="skill-detail-head">
      <div><h3>{full.title || full.id}</h3><p>{full.description || '暂无用途说明。'}</p></div>
      <StatusBadge tone={toneForStatus(full.status)}>{full.active_version || full.status}</StatusBadge>
    </header>
    {loading && <div className="nx-alert is-info">正在读取 Skill 详情…</div>}
    {error && <div className="nx-alert is-error">{error}</div>}

    <dl className="skill-meta" aria-label="Skill 摘要">
      <div className="skill-meta-version">
        <dt>版本</dt>
        <dd>
          {manageBase && (full.versions || []).length > 0 ? (
            <div className="skill-version-buttons">
              {(full.versions || []).map((version) => (
                <button
                  key={version}
                  type="button"
                  className={`nx-button is-small ${version === full.active_version ? 'is-secondary' : ''}`}
                  disabled={Boolean(managing) || version === full.active_version}
                  onClick={() => void manageSkill('activate', { version })}
                >
                  {version === full.active_version ? `${version} · Active` : `激活 ${version}`}
                </button>
              ))}
              <button
                type="button"
                className="nx-button is-secondary is-small"
                disabled={Boolean(managing) || (full.versions || []).length < 2}
                onClick={() => void manageSkill('rollback')}
              >
                回滚上一版本
              </button>
            </div>
          ) : (
            <strong>{full.active_version || '未标记'}</strong>
          )}
        </dd>
      </div>
      <div><dt>文件</dt><dd><strong>{files.length}</strong></dd></div>
      <div><dt>更新</dt><dd><strong>{formatTime(full.updated_at)}</strong></dd></div>
    </dl>
    {(manageError || manageNotice) && <div className={`nx-alert ${manageError ? 'is-error' : 'is-success'}`}>{manageError || manageNotice}</div>}

    <section className="skill-file-workspace">
      <aside className="skill-file-nav">
        <header><strong>文件列表</strong><span>{files.length}</span></header>
        {files.length === 0 ? <EmptyOps text="当前安装包没有可展示的文件。" /> : <>
          <div className="skill-mobile-file-tabs" role="tablist" aria-label="选择文件">{files.map((file) => <button type="button" role="tab" aria-selected={activePath === file.path} key={file.path} className={activePath === file.path ? 'is-active' : ''} onClick={() => setSelectedPath(file.path)}><FileText size={13} /><span>{file.path}</span></button>)}</div>
          <div className="skill-file-list">{files.map((file) => <button type="button" key={file.path} className={`skill-file-row ${activePath === file.path ? 'is-active' : ''}`} onClick={() => setSelectedPath(file.path)}><FileText size={15} /><span><strong>{file.path}</strong><small>{fileKindLabel(file.kind)} · {formatBytes(file.size_bytes)}</small></span></button>)}</div>
        </>}
      </aside>
      <div className="skill-file-preview">
        {!activePath ? <EmptyOps text="选择文件后在这里查看内容。" /> : preview.loading ? <EmptyOps text="正在读取文件…" /> : preview.error ? <div className="nx-alert is-error">{preview.error}</div> : preview.data.file ? <>
          <header>
            <div className="skill-file-preview-meta">
              <code>{preview.data.file.path}</code>
              <span>{fileKindLabel(preview.data.file.kind)} · {formatBytes(preview.data.file.size_bytes)}</span>
              {preview.data.file.truncated && <em>仅显示前 256 KiB</em>}
            </div>
            <button type="button" className="nx-button is-secondary is-small" onClick={handleCopy} disabled={!preview.data.file.content}>
              {copied ? <><Check size={13} />已复制</> : <><Copy size={13} />复制内容</>}
            </button>
          </header>
          <pre>{preview.data.file.content}</pre>
        </> : <EmptyOps text="文件内容不可用。" />}
      </div>
    </section>

    {manageBase && <section className="skill-management" aria-label="Skill 环境变量">
      <header>
        <div className="skill-management-heading">
          <strong>环境变量</strong>
          <small>运行时隔离变量配置</small>
        </div>
        <button type="button" className="nx-button is-secondary is-small" disabled={environment.loading || Boolean(managing)} onClick={() => environment.reload()}><RefreshCw size={13} />刷新</button>
      </header>
      <div className="skill-management-body">
        {environment.error && <div className="nx-alert is-error">{environment.error}</div>}
        <div className="skill-env-list">
          {environment.data.items.length === 0 ? (
            <div className="skill-env-empty">暂未配置环境变量</div>
          ) : (
            environment.data.items.map((item) => (
              <div key={item.key}>
                <code>{item.key}</code>
                <span>{item.configured ? '已配置非空值' : '已配置空值'}</span>
                <button type="button" className="nx-button is-danger is-small" disabled={Boolean(managing)} onClick={() => void manageSkill('env_unset', { key: item.key })}>删除</button>
              </div>
            ))
          )}
        </div>
        <div className="skill-env-editor">
          <input
            value={envKey}
            onChange={(event) => setEnvKey(event.target.value)}
            placeholder="变量名 (如 API_TOKEN)"
            spellCheck={false}
          />
          <input
            type="password"
            value={envValue}
            onChange={(event) => setEnvValue(event.target.value)}
            placeholder="变量值 (可留空)"
            autoComplete="new-password"
          />
          <button
            type="button"
            className="nx-button is-small"
            disabled={Boolean(managing) || !envKey.trim()}
            onClick={() => void manageSkill('env_set', { key: envKey.trim(), value: envValue })}
          >
            {managing === 'env_set' ? '保存中…' : '保存'}
          </button>
        </div>
      </div>
    </section>}

    <details className="ops-secondary-details skill-technical-details">
      <summary>版本与技术信息</summary>
      <div className="ops-detail-grid skill-technical-grid">
        <Info label="ID" value={full.id} />
        <Info label="来源" value={full.source || 'agentdock-api'} />
        <Info label="版本历史" value={(full.versions || []).join(' → ') || '暂无'} />
        <Info label="安装目录" value={detail?.root || '不可用'} />
      </div>
      <ChannelChips channels={full.channels} />
      {manifest && <div className="ops-key-values is-compact"><Info label="metadata" value={Object.keys(asRecord(manifest.metadata) || {}).join(', ') || '无'} /><Info label="operations" value={String((manifest.operations as unknown[] | undefined)?.length || 0)} /><Info label="permissions" value={Object.keys(asRecord(manifest.permissions) || {}).join(', ') || '无'} /><Info label="env" value={String((manifest.env as unknown[] | undefined)?.length || 0)} /><Info label="Active" value={full.active_version || pickText(selection || {}, ['active_version']) || 'unknown'} /></div>}
      {raw && <RawJsonPanel title="Runtime 原始响应" value={raw} />}
    </details>
  </article>;
}

function encodePathSegments(value: string): string {
  return value.split('/').map((segment) => encodeURIComponent(segment)).join('/');
}

function fileKindLabel(kind: string): string {
  if (kind === 'doc') return '文档';
  if (kind === 'code') return '代码';
  if (kind === 'config') return '配置';
  if (kind === 'manifest') return '清单';
  return '文件';
}

function taskSteps(values?: unknown[]): TaskStep[] {
  if (!Array.isArray(values)) return [];
  return values.flatMap((value, index) => {
    const record = asRecord(value);
    if (!record) return [];
    const title = pickText(record, ['title', 'name', 'text']) || `步骤 ${index + 1}`;
    return [{ id: pickText(record, ['id']) || `step-${index + 1}`, title, status: pickText(record, ['status']) || 'pending' }];
  });
}

function TaskProgress({ task, compact = false }: { task: OpsTask; compact?: boolean }) {
  const progress = taskProgress(task);
  const progressText = progress.determinate ? `${progress.label} · ${progress.percent}%` : progress.label;
  return <div className={`ops-task-progress ${compact ? 'is-compact' : ''}`}>
    {!compact && <div className="ops-task-progress-head"><strong>进度</strong><span>{progressText}</span></div>}
    <div className={`ops-progress-track tone-${toneForTask(task)} ${progress.determinate ? '' : 'is-undetermined'}`} role="progressbar" aria-label="任务进度" aria-valuemin={0} aria-valuemax={100} aria-valuenow={progress.determinate ? progress.percent : undefined} aria-valuetext={progressText}>
      <i style={{ width: `${progress.percent}%` }} />
    </div>
    {compact && <span className="ops-progress-count">{progressText}</span>}
  </div>;
}

function taskStepStatusLabel(status: string): string {
  if (status === 'completed') return '已完成';
  if (status === 'in_progress') return '进行中';
  return '待处理';
}

function TaskStepList({ steps, status }: { steps: TaskStep[]; status: string }) {
  return <section className="ops-task-steps">
    <header><h4>步骤</h4><span>{steps.length} 项</span></header>
    {steps.length === 0 ? <p className="ops-no-steps">该任务未拆分步骤，只能显示任务状态。</p> : <div className="ops-task-step-list">{steps.map((step) => {
      const stepStatus = status === 'completed' ? 'completed' : step.status;
      return <div className={`ops-task-step is-${stepStatus}`} key={step.id}>
        <span className="ops-task-step-icon">{stepStatus === 'completed' ? <CheckCircle2 size={17} /> : stepStatus === 'in_progress' ? <LoaderCircle size={17} /> : <Circle size={17} />}</span>
        <strong>{step.title}</strong>
        <small>{taskStepStatusLabel(stepStatus)}</small>
      </div>;
    })}</div>}
  </section>;
}

function asRecord(value: unknown): Record<string, unknown> | null { return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : null; }
function pickText(record: Record<string, unknown>, keys: string[]): string {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === 'string' && value.trim()) return value;
    if (typeof value === 'number' || typeof value === 'boolean') return String(value);
  }
  return '';
}
function ChannelChips({ channels }: { channels?: Record<string, string> }) {
  const entries = Object.entries(channels || {});
  if (entries.length === 0) return null;
  return <div className="ops-chip-row">{entries.map(([name, version]) => <span key={name}>{name}: {version}</span>)}</div>;
}
function RawJsonPanel({ title, value }: { title: string; value: unknown }) {
  return <details className="ops-json-panel"><summary>{title}</summary><pre>{JSON.stringify(value, null, 2)}</pre></details>;
}

function OpsShell({ error, children }: { error?: string; children: ReactNode }) { return <section className="ops-page">{error && <div className="nx-alert is-error">{error}</div>}{children}</section>; }
function Info({ label, value }: { label: string; value: string }) { return <div><dt>{label}</dt><dd>{value}</dd></div>; }
function StatusBadge({ tone, children }: { tone: Tone; children: ReactNode }) { return <span className={`status-badge tone-${tone}`}><span />{children}</span>; }
function EmptyOps({ text }: { text: string }) { return <p className="empty-mini">{text}</p>; }
