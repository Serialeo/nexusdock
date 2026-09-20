import { useEffect, useMemo, useRef, useState } from 'react';
import { CircleAlert, RefreshCw, Server, Waypoints } from 'lucide-react';
import { ApiError, api } from '../../api/client';
import { formatTime } from '../../lib/time';
import type { DeploymentPermissions } from './ProjectsPage';
import { projectSessionStatusTone } from './projectUiModel';

type Delivery = { status: 'returned' | 'host_consumed'; returned_at: string; host_consumed_at?: string; updated_at: string };
type NodeSession = {
  work_session_id: string;
  node_id: string;
  node_name?: string;
  status: string;
  created_at: string;
  updated_at: string;
  delivery?: Delivery;
};
type Target = {
  target: {
    target_id: string;
    deployment_id: string;
    node_id: string;
    cwd_rel: string;
    status: string;
    permissions: DeploymentPermissions;
    prompt: { complete: boolean; bytes: number; sources: Array<{ path: string; scope: string; bytes: number }> };
  };
  last_error?: string;
  created_at: string;
  updated_at: string;
  delivery?: Delivery;
};
type ListResponse = { ok: boolean; sessions: NodeSession[]; count: number };
type DetailResponse = { ok: boolean; session: NodeSession; targets: Target[] };

function messageOf(error: unknown): string {
  if (error instanceof ApiError) return `${error.code || error.status}：${error.message}`;
  return error instanceof Error ? error.message : '节点临时会话读取失败';
}

function deliveryText(delivery?: Delivery): string {
  if (!delivery) return '尚未 returned';
  if (delivery.status === 'host_consumed') return `host_consumed · ${formatTime(delivery.host_consumed_at || delivery.updated_at, { compact: true })}`;
  return `returned · ${formatTime(delivery.returned_at, { compact: true })}`;
}

function permissionText(value: DeploymentPermissions): string {
  if (value.full_access) return 'Full Access（Node）';
  return [`files:${value.files}`, value.shell ? 'shell' : '', value.browser ? 'browser' : '', value.dynamic_mcp ? 'dynamic_mcp' : '', value.acp ? 'acp' : ''].filter(Boolean).join(' · ');
}

export default function NodeSessionsPage() {
  const [sessions, setSessions] = useState<NodeSession[]>([]);
  const [selectedID, setSelectedID] = useState('');
  const [detail, setDetail] = useState<DetailResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [detailLoading, setDetailLoading] = useState(false);
  const [error, setError] = useState('');
  const [detailError, setDetailError] = useState('');
  const [detailRefresh, setDetailRefresh] = useState(0);
  const listRequest = useRef(0);
  const detailRequest = useRef(0);

  async function loadSessions() {
    const requestID = ++listRequest.current;
    setLoading(true);
    setError('');
    try {
      const response = await api<ListResponse>('/v1/sessions/node?limit=100');
      if (requestID !== listRequest.current) return;
      setSessions(response.sessions || []);
      setSelectedID((current) => current && response.sessions.some((item) => item.work_session_id === current) ? current : response.sessions[0]?.work_session_id || '');
    } catch (cause) {
      if (requestID === listRequest.current) setError(messageOf(cause));
    } finally {
      if (requestID === listRequest.current) setLoading(false);
    }
  }

  useEffect(() => { void loadSessions(); }, []);
  useEffect(() => {
    const requestID = ++detailRequest.current;
    if (!selectedID) {
      setDetail(null);
      setDetailError('');
      setDetailLoading(false);
      return;
    }
    const controller = new AbortController();
    setDetail(null);
    setDetailLoading(true);
    setDetailError('');
    api<DetailResponse>(`/v1/sessions/node/${encodeURIComponent(selectedID)}`, { signal: controller.signal }).then((response) => {
      if (requestID === detailRequest.current) setDetail(response);
    }).catch((cause) => {
      if (requestID === detailRequest.current && !(cause instanceof Error && cause.message === '请求已取消')) setDetailError(messageOf(cause));
    }).finally(() => {
      if (requestID === detailRequest.current) setDetailLoading(false);
    });
    return () => controller.abort();
  }, [selectedID, detailRefresh]);

  function refreshSessions() {
    setDetailRefresh((current) => current + 1);
    void loadSessions();
  }

  const selected = useMemo(() => sessions.find((item) => item.work_session_id === selectedID), [sessions, selectedID]);
  return <section className="project-detail-page">
    {error && <div className="nx-alert is-error"><CircleAlert size={16} />{error}</div>}
    <section className="project-sessions-panel">
      <header className="project-sessions-heading">
        <div><Waypoints size={18} /><span><strong>Node Sessions</strong></span></div>
        <button type="button" className="nx-button is-secondary is-small" disabled={loading} onClick={refreshSessions}><RefreshCw size={14} />刷新</button>
      </header>
      <section className="project-sessions-workspace">
        <aside className="project-session-list" aria-busy={loading}>
          {loading && sessions.length === 0 && <div className="project-session-empty">正在读取节点临时会话…</div>}
          {!loading && sessions.length === 0 && <div className="project-session-empty">还没有节点临时会话。</div>}
          {sessions.map((item) => <button type="button" className={item.work_session_id === selectedID ? 'is-active' : ''} key={item.work_session_id} onClick={() => setSelectedID(item.work_session_id)}><span><strong>{item.node_name || item.node_id}</strong><small>{item.status} · {formatTime(item.updated_at, { compact: true })}</small></span><em className={item.delivery?.status === 'host_consumed' ? 'is-ok' : item.delivery?.status === 'returned' ? 'is-warning' : 'is-muted'}>{deliveryText(item.delivery)}</em></button>)}
        </aside>
        <article className="project-session-detail">
          {!selected && <div className="project-session-empty">选择一个 WorkSession 查看 Target。</div>}
          {selected && <>
            <header><div><h3>{selected.node_name || selected.node_id}</h3></div><span className={`project-state ${projectSessionStatusTone(selected.status)}`}>{selected.status}</span></header>
            <section className="project-session-meta"><span><small>创建时间</small><strong>{formatTime(selected.created_at)}</strong></span><span><small>最近更新</small><strong>{formatTime(selected.updated_at)}</strong></span><span><small>上下文交付</small><strong>{deliveryText(selected.delivery)}</strong></span></section>
            {detailLoading && <div className="project-session-empty">正在读取 Target 详情…</div>}
            {detailError && <div className="nx-alert is-error">{detailError}</div>}
            {!detailLoading && detail && <section className="project-target-list">{detail.targets.map((item) => <article className="project-target-card" key={item.target.target_id}>
              <header><span><Server size={15} /><strong>{detail.session.node_name || detail.session.node_id}</strong><small>节点临时 Target</small></span><span className={`project-state ${projectSessionStatusTone(item.target.status)}`}>{item.target.status}</span></header>
              <dl><div><dt>工作目录</dt><dd><code>{item.target.cwd_rel}</code></dd></div><div><dt>上下文交付</dt><dd>{deliveryText(item.delivery)}</dd></div><div><dt>权限</dt><dd>{permissionText(item.target.permissions)}</dd></div><div><dt>Prompt</dt><dd>{item.target.prompt.complete ? `完整 · ${item.target.prompt.bytes} bytes` : '不完整'}</dd></div></dl>
              {item.last_error && <div className="nx-alert is-error">{item.last_error}</div>}
              {item.target.prompt.sources.length !== 0 && <div className="nx-alert is-warning">该临时会话意外包含 Prompt 来源，请检查 Node 配置。</div>}
            </article>)}</section>}
          </>}
        </article>
      </section>
    </section>
  </section>;
}
