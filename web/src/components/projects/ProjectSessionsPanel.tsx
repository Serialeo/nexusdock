import { useEffect, useMemo, useRef, useState } from 'react';
import { CircleAlert, RefreshCw, Server, Waypoints } from 'lucide-react';
import { ApiError, api } from '../../api/client';
import { formatTime } from '../../lib/time';
import type { AgentDockNode } from '../runtime/AgentDockNodes';
import type { DeploymentPermissions, ProjectDeployment } from './ProjectsPage';
import { projectSessionStatusTone } from './projectUiModel';

type ContextDeliveryEvidence = {
  status: 'returned' | 'host_consumed';
  returned_at: string;
  host_consumed_at?: string;
  updated_at: string;
};

type WorkSession = {
  work_session_id: string;
  status: string;
  created_at: string;
  updated_at: string;
  delivery?: ContextDeliveryEvidence;
};

type PromptSource = { path: string; scope: string; bytes: number };
type WorkTargetValue = {
  target_id: string;
  deployment_id: string;
  node_id: string;
  cwd_rel: string;
  status: string;
  permissions: DeploymentPermissions;
  prompt: { complete: boolean; bytes: number; sources: PromptSource[] };
};
type WorkTarget = {
  target: WorkTargetValue;
  last_error?: string;
  created_at: string;
  updated_at: string;
  delivery?: ContextDeliveryEvidence;
};
type SessionListResponse = { ok: boolean; project_id: string; sessions: WorkSession[]; count: number };
type SessionDetailResponse = { ok: boolean; project_id: string; session: WorkSession; targets: WorkTarget[] };

function messageOf(error: unknown): string {
  if (error instanceof ApiError) return `${error.code || error.status}：${error.message}`;
  return error instanceof Error ? error.message : 'WorkSession 读取失败';
}

function permissionText(value: DeploymentPermissions): string {
  if (value.full_access) return 'Full Access（Node）';
  return [
    `files:${value.files}`,
    value.shell ? 'shell' : '',
    value.browser ? 'browser' : '',
    value.dynamic_mcp ? 'dynamic_mcp' : '',
    value.acp ? 'acp' : '',
  ].filter(Boolean).join(' · ');
}

function deliveryEvidenceText(delivery?: ContextDeliveryEvidence): string {
  if (!delivery) return '尚未 returned';
  if (delivery.status === 'host_consumed') return `host_consumed · ${formatTime(delivery.host_consumed_at || delivery.updated_at, { compact: true })}`;
  return `returned · ${formatTime(delivery.returned_at, { compact: true })}`;
}

export default function ProjectSessionsPanel({ projectID, deployments, nodes }: {
  projectID: string;
  deployments: ProjectDeployment[];
  nodes: AgentDockNode[];
}) {
  const [sessions, setSessions] = useState<WorkSession[]>([]);
  const [selectedID, setSelectedID] = useState('');
  const [detail, setDetail] = useState<SessionDetailResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [detailLoading, setDetailLoading] = useState(false);
  const [error, setError] = useState('');
  const [detailError, setDetailError] = useState('');
  const listRequestID = useRef(0);
  const detailRequestID = useRef(0);
  const detailAbort = useRef<AbortController | null>(null);

  async function loadSessions() {
    const requestID = ++listRequestID.current;
    setLoading(true);
    setError('');
    try {
      const response = await api<SessionListResponse>(`/v1/projects/${encodeURIComponent(projectID)}/sessions?limit=100`);
      if (requestID !== listRequestID.current) return;
      setSessions(response.sessions || []);
      setSelectedID((current) => (current && response.sessions.some((item) => item.work_session_id === current)) ? current : response.sessions[0]?.work_session_id || '');
    } catch (cause) {
      if (requestID !== listRequestID.current) return;
      setError(messageOf(cause));
    } finally {
      if (requestID === listRequestID.current) setLoading(false);
    }
  }

  useEffect(() => { void loadSessions(); }, [projectID]);

  useEffect(() => {
    detailAbort.current?.abort();
    const requestID = ++detailRequestID.current;
    if (!selectedID) {
      setDetail(null);
      setDetailLoading(false);
      setDetailError('');
      return undefined;
    }
    const controller = new AbortController();
    detailAbort.current = controller;
    setDetailLoading(true);
    setDetailError('');
    api<SessionDetailResponse>(`/v1/projects/${encodeURIComponent(projectID)}/sessions/${encodeURIComponent(selectedID)}`, { signal: controller.signal }).then((response) => {
      if (requestID === detailRequestID.current) setDetail(response);
    }).catch((cause) => {
      if (requestID !== detailRequestID.current || (cause instanceof Error && cause.message === '请求已取消')) return;
      setDetail(null);
      setDetailError(messageOf(cause));
    }).finally(() => {
      if (requestID === detailRequestID.current) setDetailLoading(false);
    });
    return () => controller.abort();
  }, [projectID, selectedID]);

  const selectedSession = useMemo(() => sessions.find((item) => item.work_session_id === selectedID), [selectedID, sessions]);

  return <section className="project-sessions-panel">
    <header className="project-sessions-heading"><div><Waypoints size={18} /><span><strong>WorkSessions</strong></span></div><button type="button" className="nx-button is-secondary is-small" disabled={loading} onClick={() => void loadSessions()}><RefreshCw size={14} />刷新</button></header>
    {error && <div className="nx-alert is-error" role="alert"><CircleAlert size={16} />{error}</div>}
    <section className="project-sessions-workspace">
      <aside className="project-session-list" aria-busy={loading}>
        {loading && sessions.length === 0 && <div className="project-session-empty">正在读取 WorkSessions…</div>}
        {!loading && sessions.length === 0 && <div className="project-session-empty">这个 Project 还没有由 MCP Host 创建的 WorkSession。</div>}
        {sessions.map((session) => <button type="button" className={session.work_session_id === selectedID ? 'is-active' : ''} key={session.work_session_id} onClick={() => setSelectedID(session.work_session_id)}><span><strong>{session.status}</strong><small>更新于 {formatTime(session.updated_at, { compact: true })}</small></span><em className={session.delivery?.status === 'host_consumed' ? 'is-ok' : session.delivery?.status === 'returned' ? 'is-warning' : 'is-muted'}>{deliveryEvidenceText(session.delivery)}</em></button>)}
      </aside>
      <article className="project-session-detail">
        {!selectedSession && <div className="project-session-empty">选择一个 WorkSession 查看 Target。</div>}
        {selectedSession && <>
          <header><div><h3>工作会话</h3></div><span className={`project-state ${projectSessionStatusTone(selectedSession.status)}`}>{selectedSession.status}</span></header>
          <section className="project-session-meta"><span><small>创建时间</small><strong>{formatTime(selectedSession.created_at)}</strong></span><span><small>最近更新</small><strong>{formatTime(selectedSession.updated_at)}</strong></span><span><small>上下文交付</small><strong>{deliveryEvidenceText(selectedSession.delivery)}</strong></span></section>
          {detailLoading && <div className="project-session-empty">正在读取 Target 详情…</div>}
          {detailError && <div className="nx-alert is-error" role="alert">{detailError}</div>}
          {!detailLoading && detail && <section className="project-target-list">{detail.targets.length === 0 ? <div className="project-session-empty">该 WorkSession 没有 Target。</div> : detail.targets.map((item) => {
            const target = item.target;
            const node = nodes.find((candidate) => candidate.id === target.node_id);
            const deployment = deployments.find((candidate) => candidate.id === target.deployment_id);
            return <article className="project-target-card" key={target.target_id}>
              <header><span><Server size={15} /><strong>{node?.name || '节点不可用'}</strong><small>{deployment?.role || '未填写 role'} · {deployment?.purpose || '未填写 purpose'}</small></span><span className={`project-state ${projectSessionStatusTone(target.status)}`}>{target.status}</span></header>
              <dl><div><dt>工作目录</dt><dd><code>{target.cwd_rel}</code></dd></div><div><dt>上下文交付</dt><dd>{deliveryEvidenceText(item.delivery)}</dd></div><div><dt>权限</dt><dd>{permissionText(target.permissions)}</dd></div><div><dt>Prompt</dt><dd>{target.prompt.complete ? `完整 · ${target.prompt.bytes} bytes` : '不完整'}</dd></div><div><dt>最近更新</dt><dd>{formatTime(item.updated_at)}</dd></div></dl>
              {item.last_error && <div className="nx-alert is-error">{item.last_error}</div>}
              <details><summary>适用 Prompt 来源</summary>{target.prompt.sources.length === 0 ? <p>无 AGENTS.md 来源。</p> : <div className="project-target-prompt-sources">{target.prompt.sources.map((source, index) => <span key={`${source.path}:${source.scope}:${index}`}><strong>{source.path}</strong><small>scope {source.scope} · {source.bytes} bytes</small></span>)}</div>}</details>
            </article>;
          })}</section>}
        </>}
      </article>
    </section>
  </section>;
}
