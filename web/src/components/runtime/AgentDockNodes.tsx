import { useCallback, useEffect, useMemo, useState, type FormEvent, type ReactNode } from 'react';
import { CirclePlus, Pencil, RefreshCw, Server, Trash2 } from 'lucide-react';
import { api } from '../../api/client';
import Dialog from '../Dialog';
import BuiltinCapabilities from './BuiltinCapabilities';

const selectedNodeStorageKey = 'nexus:runtime-node-id';

type Notice = { tone: 'success' | 'error' | 'info'; text: string };

export type AgentDockNode = {
  id: string;
  device_id: string;
  name: string;
  enabled: boolean;
  full_access: boolean;
  version?: string;
  protocol_version?: string;
  os?: string;
  arch?: string;
  capabilities: string[];
  online: boolean;
  last_seen_at?: string;
  created_at: string;
  updated_at: string;
};

type NodeListResponse = { ok: boolean; nodes: AgentDockNode[]; count: number };
type NodeResponse = { ok: boolean; node: AgentDockNode };
type PairingResponse = { ok: boolean; pairing: { code: string; expires_at: string } };

export function useAgentDockNodes(refreshToken: number) {
  const [nodes, setNodes] = useState<AgentDockNode[]>([]);
  const [selectedNodeID, setSelectedNodeID] = useState(() => window.localStorage.getItem(selectedNodeStorageKey) || '');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [revision, setRevision] = useState(0);
  const reload = useCallback(() => setRevision((value) => value + 1), []);
  const selectNode = useCallback((nodeID: string) => {
    setSelectedNodeID(nodeID);
    if (nodeID) window.localStorage.setItem(selectedNodeStorageKey, nodeID);
    else window.localStorage.removeItem(selectedNodeStorageKey);
  }, []);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError('');
    api<NodeListResponse>('/v1/runtime/nodes').then((result) => {
      if (cancelled) return;
      const next = result.nodes || [];
      setNodes(next);
      if (selectedNodeID && !next.some((node) => node.id === selectedNodeID && node.enabled)) selectNode('');
    }).catch((cause) => {
      if (!cancelled) setError(cause instanceof Error ? cause.message : '无法读取 AgentDock 节点');
    }).finally(() => {
      if (!cancelled) setLoading(false);
    });
    return () => { cancelled = true; };
  }, [refreshToken, revision, selectedNodeID, selectNode]);

  const selectedNode = useMemo(
    () => nodes.find((node) => node.id === selectedNodeID && node.enabled) || null,
    [nodes, selectedNodeID],
  );
  return { nodes, selectedNode, selectedNodeID, loading, error, reload, selectNode };
}

export function AgentDockNodeSelector({ nodes, selectedNodeID, onSelect }: {
  nodes: AgentDockNode[];
  selectedNodeID: string;
  onSelect: (nodeID: string) => void;
}) {
  return <label className="runtime-node-selector">
    <Server size={15} />
    <span>AgentDock</span>
    <select aria-label="选择 AgentDock 节点" value={selectedNodeID} onChange={(event) => onSelect(event.target.value)}>
      <option value="">请选择节点</option>
      {nodes.filter((node) => node.enabled).map((node) => <option key={node.id} value={node.id}>{node.name}{node.online ? '' : '（离线）'}</option>)}
    </select>
  </label>;
}

export function AgentDockNodeRequired({ children }: { children?: ReactNode }) {
  return <section className="runtime-node-required">
    <span><Server size={25} /></span>
    <h2>请选择 AgentDock 节点</h2>
    <p>节点操作必须明确指定设备。请从上方选择，或先配对一台 AgentDock。</p>
    {children}
  </section>;
}

export function AgentDockNodesPanel({ nodes, selectedNodeID, loading, error, onReload, onSelect }: {
  nodes: AgentDockNode[];
  selectedNodeID: string;
  loading: boolean;
  error: string;
  onReload: () => void;
  onSelect: (nodeID: string) => void;
}) {
  const [capabilityNode, setCapabilityNode] = useState<AgentDockNode | null>(null);
  const [editing, setEditing] = useState<AgentDockNode | null>(null);
  const [editName, setEditName] = useState('');
  const [editEnabled, setEditEnabled] = useState(true);
  const [editFullAccess, setEditFullAccess] = useState(false);
  const [deleting, setDeleting] = useState<AgentDockNode | null>(null);
  const [pairing, setPairing] = useState<PairingResponse['pairing'] | null>(null);
  const [busy, setBusy] = useState('');
  const [notice, setNotice] = useState<Notice | null>(null);

  async function createPairingCode() {
    setBusy('pair');
    setNotice(null);
    try {
      const result = await api<PairingResponse>('/v1/runtime/nodes/pairing-codes', { method: 'POST' });
      setPairing(result.pairing);
    } catch (cause) {
      setNotice({ tone: 'error', text: cause instanceof Error ? cause.message : '无法生成配对码' });
    } finally {
      setBusy('');
    }
  }

  function openEdit(node: AgentDockNode) {
    setEditing(node);
    setEditName(node.name);
    setEditEnabled(node.enabled);
    setEditFullAccess(node.full_access);
  }

  async function submitEdit(event: FormEvent) {
    event.preventDefault();
    if (!editing || busy) return;
    setBusy('save');
    try {
      const result = await api<NodeResponse>(`/v1/runtime/nodes/${encodeURIComponent(editing.id)}`, {
        method: 'PATCH', body: JSON.stringify({ name: editName.trim(), enabled: editEnabled, full_access: editFullAccess }),
      });
      setEditing(null);
      onReload();
      setNotice({ tone: 'success', text: `已更新 ${result.node.name}` });
    } catch (cause) {
      setNotice({ tone: 'error', text: cause instanceof Error ? cause.message : '节点保存失败' });
    } finally {
      setBusy('');
    }
  }

  async function remove() {
    if (!deleting || busy) return;
    setBusy('delete');
    try {
      await api(`/v1/runtime/nodes/${encodeURIComponent(deleting.id)}`, { method: 'DELETE' });
      if (selectedNodeID === deleting.id) onSelect('');
      const name = deleting.name;
      setDeleting(null);
      onReload();
      setNotice({ tone: 'success', text: `已删除 ${name}` });
    } catch (cause) {
      setNotice({ tone: 'error', text: cause instanceof Error ? cause.message : '节点删除失败' });
    } finally {
      setBusy('');
    }
  }

  const pairCommand = pairing ? `agentdock nexus pair --endpoint ${window.location.origin} --code ${pairing.code}` : '';

  return <section className="agentdock-nodes-panel">
    <header>
      <div><span className="nexus-eyebrow">RUNTIME NODES</span><h2>AgentDock 节点</h2><p>Node 是运行环境；Project/Deployment 决定工作目录与能力。旧 Guidance / Direct Instructions 不再从控制台配置。</p></div>
      <div className="agentdock-node-actions">
        <button type="button" className="nx-button is-secondary" onClick={onReload} disabled={loading}><RefreshCw size={15} />刷新</button>
        <button type="button" className="nx-button" onClick={() => void createPairingCode()} disabled={busy === 'pair'}><CirclePlus size={15} />{busy === 'pair' ? '生成中…' : '配对设备'}</button>
      </div>
    </header>

    {(error || notice) && <div className={`nx-alert is-${error || notice?.tone === 'error' ? 'error' : notice?.tone || 'info'}`}>{error || notice?.text}</div>}

    <div className="agentdock-node-list">
      {loading && nodes.length === 0 ? <p className="empty-mini">正在读取 AgentDock 节点…</p> : nodes.length === 0 ? <p className="empty-mini">尚未配对 AgentDock 节点。</p> : nodes.map((node) => <article key={node.id} className={selectedNodeID === node.id ? 'is-selected' : ''}>
        <span className="agentdock-node-icon"><Server size={18} /></span>
        <div className="agentdock-node-copy">
          <div><strong>{node.name}</strong><code>{node.id}</code>{!node.enabled && <em>已停用</em>}</div>
          <small>{node.os && node.arch ? `${node.os}/${node.arch}` : '等待首次连接'}{node.version ? ` · AgentDock ${node.version}` : ''}</small>
          <span className={`agentdock-node-status ${node.online ? 'is-online' : 'is-offline'}`}><strong>{node.online ? '在线' : '离线'}</strong><span>· {node.full_access ? 'Full Access' : '按 Deployment 权限'} · {node.capabilities?.length || 0} 个节点工具{node.last_seen_at ? ` · 最近 ${new Date(node.last_seen_at).toLocaleString()}` : ''}</span></span>
        </div>
        <div className="agentdock-node-row-actions">
 <button type="button" className="nx-button is-secondary is-small" onClick={() => setCapabilityNode(node)}>内置能力</button>
          <button type="button" className="nx-button is-secondary is-small" disabled={!!busy} onClick={() => openEdit(node)}><Pencil size={14} />编辑</button>
          <button type="button" className="nx-button is-danger is-small" disabled={!!busy} onClick={() => setDeleting(node)}><Trash2 size={14} />删除</button>
        </div>
      </article>)}
    </div>

    {capabilityNode && <Dialog title={`${capabilityNode.name} · 内置能力`} description={`配置作用于节点 ${capabilityNode.id}`} onClose={() => { setCapabilityNode(null); onReload(); }}>
      <BuiltinCapabilities key={capabilityNode.id} nodeID={capabilityNode.id} online={!!nodes.find((node) => node.id === capabilityNode.id)?.online} />
    </Dialog>}

    {pairing && <Dialog title="配对 AgentDock" description={`配对码将在 ${new Date(pairing.expires_at).toLocaleString()} 失效，且只能使用一次。`} onClose={() => setPairing(null)} wide>
      <div className="agentdock-node-delete"><p>在目标设备执行以下命令，然后重启 AgentDock：</p><code>{pairCommand}</code><footer><button type="button" className="nx-button" onClick={() => void navigator.clipboard.writeText(pairCommand)}>复制命令</button></footer></div>
    </Dialog>}

    {editing && <Dialog title={`编辑 ${editing.name}`} description="Full Access 是 Node 级 Project 执行权限，与 Project Folder 独立；Folder 只决定默认 cwd 与 AGENTS.md 搜索边界。" onClose={() => setEditing(null)}>
      <form className="agentdock-node-form" onSubmit={submitEdit}>
        <label className="is-wide"><span>显示名称</span><input required maxLength={100} value={editName} onChange={(event) => setEditName(event.target.value)} /></label>
        <label className="agentdock-node-check"><input type="checkbox" checked={editFullAccess} onChange={(event) => setEditFullAccess(event.target.checked)} /><span>Full Access</span></label>
        <p className="empty-mini">开启后，该 Node 上的 Project Target 可使用节点已暴露的全部执行能力；不会因为 Project 选择了某个文件夹而限制在该目录内。关闭后继续按各 Deployment 的细粒度权限执行。</p>
        <label className="agentdock-node-check"><input type="checkbox" checked={editEnabled} onChange={(event) => setEditEnabled(event.target.checked)} /><span>启用节点</span></label>
        <footer><button type="button" className="nx-button is-secondary" onClick={() => setEditing(null)}>取消</button><button type="submit" className="nx-button" disabled={busy === 'save'}>{busy === 'save' ? '保存中…' : '保存'}</button></footer>
      </form>
    </Dialog>}

    {deleting && <Dialog title="删除 AgentDock 节点" description="该设备的 Device Token 会同时失效；AgentDock 本地服务与源码目录不受影响。" onClose={() => setDeleting(null)}>
      <div className="agentdock-node-delete"><p>确定删除「{deleting.name}」？</p><code>{deleting.id}</code><footer><button type="button" className="nx-button is-secondary" onClick={() => setDeleting(null)}>取消</button><button type="button" className="nx-button is-danger" disabled={busy === 'delete'} onClick={() => void remove()}>{busy === 'delete' ? '删除中…' : '确认删除'}</button></footer></div>
    </Dialog>}
  </section>;
}
