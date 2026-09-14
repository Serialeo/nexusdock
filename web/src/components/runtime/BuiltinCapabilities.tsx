import { useEffect, useRef, useState } from 'react';
import { BuiltinRequests } from './builtinRequests';
import { api } from '../../api/client';

export type BuiltinCapability = {
  id: string; provided: boolean; enabled: boolean; ready: boolean;
  available: boolean; transitioning: boolean; reason: string; tools: string[];
};
type Snapshot = { ok: boolean; node_id: string; builtins: BuiltinCapability[] };
const labels: Record<string, string> = { browser: '浏览器 CDP', acp: 'Coding Agent（ACP）' };

export default function BuiltinCapabilities({ nodeID, online }: { nodeID: string; online: boolean }) {
  const [states, setStates] = useState<BuiltinCapability[]>([]);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const requests = useRef<BuiltinRequests<Snapshot, { id: string; enabled: boolean }> | null>(null);
  const endpoint = `/v1/runtime/nodes/${encodeURIComponent(nodeID)}/builtins`;
  useEffect(() => {
    setError(''); setStates([]); setBusy(false);
    if (!online) return;
    const client = new BuiltinRequests<Snapshot, { id: string; enabled: boolean }>(
      (signal) => api<Snapshot>(endpoint, { signal }),
      (update, signal) => api<Snapshot>(endpoint, { method: 'POST', signal, timeoutMs: 35_000, body: JSON.stringify(update) }),
      (result) => { setStates(result.builtins); setError(''); },
      (cause) => { setError(`${cause instanceof Error ? cause.message : '读取能力失败'}。请刷新核对节点实际状态。`); setStates([]); },
      setBusy,
    );
    requests.current = client;
    const visibility = () => client.setVisible(!document.hidden);
    document.addEventListener('visibilitychange', visibility);
    visibility();
    return () => { client.close(); requests.current = null; document.removeEventListener('visibilitychange', visibility); };
  }, [endpoint, online]);

  function toggle(state: BuiltinCapability, enabled: boolean) {
    void requests.current?.update({ id: state.id, enabled });
  }

  return <section className="agentdock-node-form" aria-label="内置能力">
    <p>选择保存在此 AgentDock 节点，重启后保留。开启能力后仍按 Deployment 权限执行；外部 MCP 服务在 MCP 页面管理。</p>
    {!online && <p className="empty-mini">节点离线，无法读取或修改实时状态。重连后会读取节点保存的选择。</p>}
    {error && <p role="alert" className="nx-alert is-error">{error}</p>}
    {states.map((state) => <div key={state.id}>
      <label className="agentdock-node-check">
        <input type="checkbox" checked={state.enabled} disabled={!online || busy || !state.provided || state.transitioning} onChange={(event) => void toggle(state, event.target.checked)} />
        <span>{labels[state.id] || state.id}</span>
      </label>
      <p className="empty-mini">{state.available ? `可用 · ${state.tools.length} 个工具 ${state.reason}` : state.reason || '后端未就绪'}</p>
      {state.enabled && state.provided && !state.ready && !state.transitioning && <button type="button" className="nx-button is-secondary is-small" disabled={busy || !online} onClick={() => void toggle(state, true)}>重新检查后端</button>}
    </div>)}
    <button type="button" className="nx-button is-secondary" disabled={busy || !online} onClick={() => void requests.current?.refresh()}>刷新状态</button>
    <p className="empty-mini">关闭会取消并清理当前会话，已发生的操作不会撤销；重新开启不会自动恢复旧任务。</p>
  </section>;
}
