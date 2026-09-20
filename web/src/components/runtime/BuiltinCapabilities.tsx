import { useEffect, useRef, useState, type ReactNode } from 'react';
import { RefreshCw } from 'lucide-react';
import { BuiltinRequests } from './builtinRequests';
import { api } from '../../api/client';

export type BuiltinCapability = {
  id: string; provided: boolean; enabled: boolean; ready: boolean;
  available: boolean; transitioning: boolean; reason: string; tools: string[];
};
type Snapshot = { ok: boolean; node_id: string; builtins: BuiltinCapability[] };
type Tone = 'ok' | 'warn' | 'danger' | 'muted';

const labels: Record<string, string> = { browser: '浏览器 CDP', acp: 'Coding Agent（ACP）' };

function StatusBadge({ tone, children }: { tone: Tone; children: ReactNode }) {
  return <span className={`status-badge tone-${tone}`}><span />{children}</span>;
}

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

  return (
    <div className="builtin-caps-panel" aria-label="内置能力">
      {!online && <div className="nx-alert is-warning">节点当前离线，无法读取或修改实时状态。</div>}
      {error && <div role="alert" className="nx-alert is-error">{error}</div>}

      <div className="builtin-caps-list">
        {states.map((state) => {
          const tone: Tone = !state.provided
            ? 'muted'
            : state.transitioning
              ? 'warn'
              : state.enabled
                ? (state.available ? 'ok' : 'danger')
                : 'muted';

          const statusText = !state.provided
            ? '不支持'
            : state.transitioning
              ? '切换中…'
              : state.enabled
                ? (state.available ? (state.tools.length > 0 ? `就绪 · ${state.tools.length} 个工具` : '已就绪') : (state.reason || '未就绪'))
                : '已关闭';

          return (
            <div key={state.id} className={`builtin-cap-row ${!state.provided ? 'is-disabled' : ''}`}>
              <div className="builtin-cap-main">
                <div className="builtin-cap-title">
                  <strong>{labels[state.id] || state.id}</strong>
                  <StatusBadge tone={tone}>{statusText}</StatusBadge>
                </div>
                {state.enabled && state.provided && !state.ready && !state.transitioning && (
                  <button
                    type="button"
                    className="nx-button is-secondary is-small"
                    disabled={busy || !online}
                    onClick={() => void toggle(state, true)}
                  >
                    重试后端
                  </button>
                )}
              </div>
              <label className="ai-switch-row" title={!state.provided ? '当前发行包不提供此能力' : undefined}>
                <input
                  type="checkbox"
                  checked={state.enabled}
                  disabled={!online || busy || !state.provided || state.transitioning}
                  onChange={(event) => void toggle(state, event.target.checked)}
                />
                <span><strong>{state.enabled ? '已开启' : '已关闭'}</strong></span>
              </label>
            </div>
          );
        })}
        {states.length === 0 && online && !error && (
          <div className="builtin-caps-empty">正在读取能力…</div>
        )}
      </div>

      <footer className="builtin-caps-footer">
        <button
          type="button"
          className="nx-button is-secondary is-small"
          disabled={busy || !online}
          onClick={() => void requests.current?.refresh()}
        >
          <RefreshCw size={13} className={busy ? 'spin' : ''} />
          刷新状态
        </button>
      </footer>
    </div>
  );
}
