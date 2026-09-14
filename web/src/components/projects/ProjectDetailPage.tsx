import { useCallback, useEffect, useMemo, useState, type FormEvent } from 'react';
import { ArrowLeft, CircleAlert, CirclePlus, CloudCog, FolderOpen, FolderTree, Pencil, RefreshCw, RotateCw, Trash2 } from 'lucide-react';
import { ApiError, api } from '../../api/client';
import Dialog from '../Dialog';
import type { AgentDockNode } from '../runtime/AgentDockNodes';
import type { DeploymentPermissions, FileCapability, ProjectDeployment, ProjectRecord } from './ProjectsPage';
import RemoteFolderPicker from './RemoteFolderPicker';
import ProjectPromptPanel from './ProjectPromptPanel';
import ProjectSessionsPanel from './ProjectSessionsPanel';
import { deploymentApplyState, deploymentIsAvailable, projectShellPermissionWarning } from './projectUiModel';

type ProjectResponse = { ok: boolean; project: ProjectRecord };
type DeploymentListResponse = { ok: boolean; deployments: ProjectDeployment[]; count: number };
type DeploymentResponse = { ok: boolean; deployment: ProjectDeployment };

type DeploymentDraft = {
  nodeID: string;
  workingFolder: string;
  role: string;
  purpose: string;
  files: FileCapability;
  shell: boolean;
  browser: boolean;
  dynamicMCP: boolean;
  acp: boolean;
  enabled: boolean;
};

const emptyDeploymentDraft: DeploymentDraft = {
  nodeID: '', workingFolder: '', role: '', purpose: '', files: 'read_only',
  shell: false, browser: false, dynamicMCP: false, acp: false, enabled: true,
};

function messageOf(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 409 || error.code === 'REVISION_CONFLICT') return 'Deployment 已被其他操作更新。当前草稿未丢失；请刷新详情后重新确认。';
    return `${error.code || error.status}：${error.message}`;
  }
  return error instanceof Error ? error.message : 'Deployment 操作失败';
}

function permissionsFromDraft(draft: DeploymentDraft): DeploymentPermissions {
  return { full_access: false, files: draft.files, computer: 'none', shell: draft.shell, browser: draft.browser, dynamic_mcp: draft.dynamicMCP, acp: draft.acp };
}

function draftFromDeployment(value: ProjectDeployment): DeploymentDraft {
  return {
    nodeID: value.node_id,
    workingFolder: value.working_folder,
    role: value.role || '',
    purpose: value.purpose || '',
    files: value.permissions.files,
    shell: value.permissions.shell,
    browser: value.permissions.browser,
    dynamicMCP: value.permissions.dynamic_mcp,
    acp: value.permissions.acp,
    enabled: value.enabled,
  };
}

function permissionText(value: ProjectDeployment): string {
  const enabled = [
    `files:${value.permissions.files}`,
    value.permissions.shell ? 'shell' : '',
    value.permissions.browser ? 'browser' : '',
    value.permissions.dynamic_mcp ? 'dynamic_mcp' : '',
    value.permissions.acp ? 'acp' : '',
  ].filter(Boolean);
  return enabled.join(' · ');
}

export default function ProjectDetailPage({ projectID, nodes, onBack }: { projectID: string; nodes: AgentDockNode[]; onBack: () => void }) {
  const [project, setProject] = useState<ProjectRecord | null>(null);
  const [deployments, setDeployments] = useState<ProjectDeployment[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [revision, setRevision] = useState(0);
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<ProjectDeployment | null>(null);
  const [deleting, setDeleting] = useState<ProjectDeployment | null>(null);
  const [draft, setDraft] = useState<DeploymentDraft>(emptyDeploymentDraft);
  const [busy, setBusy] = useState('');
  const [dialogError, setDialogError] = useState('');
  const [folderPickerOpen, setFolderPickerOpen] = useState(false);
  const [activeTab, setActiveTab] = useState<'deployments' | 'prompt' | 'sessions'>('deployments');

  const reload = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const [projectResponse, deploymentResponse] = await Promise.all([
        api<ProjectResponse>(`/v1/projects/${encodeURIComponent(projectID)}`),
        api<DeploymentListResponse>(`/v1/projects/${encodeURIComponent(projectID)}/deployments`),
      ]);
      setProject(projectResponse.project);
      setDeployments(deploymentResponse.deployments || []);
    } catch (cause) {
      setError(messageOf(cause));
    } finally {
      setLoading(false);
    }
  }, [projectID]);

  useEffect(() => { void reload(); }, [reload, revision]);

  const available = useMemo(() => deployments.filter((deployment) => deploymentIsAvailable(deployment, nodes)).length, [deployments, nodes]);
  const selectedNode = useMemo(() => nodes.find((node) => node.id === draft.nodeID), [draft.nodeID, nodes]);

  function openCreate() {
    const firstEnabledNode = nodes.find((node) => node.enabled);
    setFolderPickerOpen(false);
    setDraft({ ...emptyDeploymentDraft, nodeID: firstEnabledNode?.id || '' });
    setDialogError('');
    setCreating(true);
  }

  function openEdit(deployment: ProjectDeployment) {
    setFolderPickerOpen(false);
    setDraft(draftFromDeployment(deployment));
    setDialogError('');
    setEditing(deployment);
  }

  function dirtyAgainst(deployment: ProjectDeployment): boolean {
    const original = draftFromDeployment(deployment);
    return JSON.stringify(original) !== JSON.stringify(draft);
  }

  async function saveDeployment(event: FormEvent) {
    event.preventDefault();
    if (busy || !draft.nodeID) return;
    setBusy('deployment-save');
    setDialogError('');
    try {
      const body = {
        working_folder: draft.workingFolder.trim(), role: draft.role.trim(), purpose: draft.purpose.trim(),
        permissions: permissionsFromDraft(draft), enabled: draft.enabled,
      };
      let response: DeploymentResponse;
      if (editing) {
        response = await api<DeploymentResponse>(`/v1/projects/${encodeURIComponent(projectID)}/deployments/${encodeURIComponent(editing.id)}`, {
          method: 'PUT',
          body: JSON.stringify({ ...body, expected_revision: editing.desired_revision }),
        });
        setEditing(null);
      } else {
        response = await api<DeploymentResponse>(`/v1/projects/${encodeURIComponent(projectID)}/deployments`, {
          method: 'POST', body: JSON.stringify({ ...body, node_id: draft.nodeID }),
        });
        setCreating(false);
      }
      const state = deploymentApplyState(response.deployment);
      setFolderPickerOpen(false);
      setNotice(`Deployment 已保存。${state.label}。`);
      setRevision((value) => value + 1);
    } catch (cause) {
      setDialogError(messageOf(cause));
    } finally {
      setBusy('');
    }
  }

  async function applyDeployment(deployment: ProjectDeployment) {
    if (busy) return;
    setBusy(`apply:${deployment.id}`);
    setError('');
    try {
      const response = await api<DeploymentResponse>(`/v1/projects/${encodeURIComponent(projectID)}/deployments/${encodeURIComponent(deployment.id)}/apply`, { method: 'POST' });
      const state = deploymentApplyState(response.deployment);
      setNotice(`已请求应用。${state.label}。`);
      setRevision((value) => value + 1);
    } catch (cause) {
      setError(messageOf(cause));
    } finally {
      setBusy('');
    }
  }

  async function deleteDeployment() {
    if (!deleting || busy) return;
    setBusy('deployment-delete');
    setDialogError('');
    try {
      await api<{ ok: boolean; deployment_id: string; deleted: boolean }>(`/v1/projects/${encodeURIComponent(projectID)}/deployments/${encodeURIComponent(deleting.id)}`, {
        method: 'DELETE', body: JSON.stringify({ expected_revision: deleting.desired_revision }),
      });
      setNotice('Deployment 配置已删除；Node 上的工作目录与源码未删除。');
      setDeleting(null);
      setRevision((value) => value + 1);
    } catch (cause) {
      setDialogError(messageOf(cause));
    } finally {
      setBusy('');
    }
  }

  if (loading && !project) return <section className="project-detail-page"><div className="project-empty">正在读取 Project…</div></section>;

  return <section className="project-detail-page">
    <header className="project-detail-heading">
      <button type="button" className="nx-button is-secondary is-small" onClick={onBack}><ArrowLeft size={14} />Projects</button>
      <div className="project-detail-title"><span className="nexus-eyebrow">WORK / PROJECT</span><h2>{project?.name || projectID}</h2><p>{project?.orchestration_policy || '尚未填写协作说明。'}</p></div>
      <div className="project-detail-actions"><button type="button" className="nx-button is-secondary" disabled={loading} onClick={() => void reload()}><RefreshCw size={15} />刷新</button><button type="button" className="nx-button" disabled={!project?.enabled} onClick={openCreate}><CirclePlus size={16} />添加 Deployment</button></div>
    </header>

    <section className="project-detail-meta">
      <span><small>Project</small><code>{project?.id || projectID}</code></span>
      <span><small>Revision</small><strong>{project?.revision || '—'}</strong></span>
      <span><small>Deployment</small><strong>{deployments.length}</strong></span>
      <span><small>可用 Target</small><strong>{available}</strong></span>
      <span><small>状态</small><strong>{project?.enabled ? '已启用' : '已停用'}</strong></span>
    </section>

    {error && <div className="nx-alert is-error" role="alert"><CircleAlert size={16} />{error}</div>}
    {notice && <div className="nx-alert is-success" role="status">{notice}</div>}

    <nav className="project-detail-tabs" aria-label="Project 详情导航">
      <button type="button" className={activeTab === 'deployments' ? 'is-active' : ''} aria-current={activeTab === 'deployments' ? 'page' : undefined} onClick={() => setActiveTab('deployments')}>部署</button>
      <button type="button" className={activeTab === 'prompt' ? 'is-active' : ''} aria-current={activeTab === 'prompt' ? 'page' : undefined} onClick={() => setActiveTab('prompt')}>项目规则</button>
      <button type="button" className={activeTab === 'sessions' ? 'is-active' : ''} aria-current={activeTab === 'sessions' ? 'page' : undefined} onClick={() => setActiveTab('sessions')}>工作记录</button>
    </nav>

    {activeTab === 'deployments' && <section className="deployment-panel">
      <header><div><CloudCog size={18} /><span><strong>Deployments</strong><small>每个 Deployment 固定绑定一个 Node；Project Folder 可选，只提供默认 cwd 与 Project Prompt 边界。</small></span></div><button type="button" className="nx-button is-small" disabled={!project?.enabled} onClick={openCreate}><CirclePlus size={14} />添加</button></header>
      {deployments.length === 0 ? <div className="deployment-empty"><FolderTree size={24} /><strong>还没有 Deployment</strong><span>先选择一个 Node；Project Folder 可以稍后再配置。</span></div> : <div className="deployment-list">
        {deployments.map((deployment) => {
          const node = nodes.find((item) => item.id === deployment.node_id);
          const state = deploymentApplyState(deployment);
          return <article className="deployment-row" key={deployment.id}>
            <div className="deployment-identity"><strong>{node?.name || deployment.node_id}</strong><small>{node?.os && node?.arch ? `${node.os}/${node.arch}` : 'Node 信息未知'} · {node?.online ? '在线' : '离线'} · {node?.full_access ? 'Full Access' : '按 Deployment 权限'}</small><code title={deployment.working_folder || '未设置 Project Folder'}>{deployment.working_folder || '未设置（使用 Node 默认 cwd）'}</code></div>
            <div className="deployment-purpose"><span><small>Role</small><strong>{deployment.role || '未填写'}</strong></span><span><small>Purpose</small><strong>{deployment.purpose || '未填写'}</strong></span></div>
            <div className="deployment-permissions"><small>有效权限</small><strong>{node?.full_access ? 'Full Access（Node）' : permissionText(deployment)}</strong><span>{node?.full_access ? '不受 Project Folder 限制，仍受 Node OS 权限约束' : deployment.permissions.shell ? 'Git 随 shell 可执行' : 'Git 不可通过命令执行'}</span></div>
            <div className="deployment-apply"><span className={`project-state ${state.className}`}>{state.label}</span><small>{state.detail}</small><code>desired {deployment.desired_revision || '—'}</code><code>applied {deployment.applied_revision || '—'}</code></div>
            <div className="deployment-actions"><button type="button" className="nx-button is-secondary is-small" disabled={!!busy} onClick={() => openEdit(deployment)}><Pencil size={14} />编辑</button><button type="button" className="nx-button is-secondary is-small" disabled={!!busy || !deployment.enabled} onClick={() => void applyDeployment(deployment)}><RotateCw size={14} />重试应用</button><button type="button" className="nx-button is-danger is-small" disabled={!!busy} onClick={() => { setDialogError(''); setDeleting(deployment); }}><Trash2 size={14} />删除</button></div>
          </article>;
        })}
      </div>}
    </section>}

    {activeTab === 'prompt' && <ProjectPromptPanel projectID={projectID} deployments={deployments} nodes={nodes} />}
    {activeTab === 'sessions' && <ProjectSessionsPanel projectID={projectID} deployments={deployments} nodes={nodes} />}

    {(creating || editing) && <Dialog title={editing ? '编辑 Deployment' : '添加 Deployment'} description="Node 是执行目标；Project Folder 可选，只定义默认 cwd 与 Project Prompt 边界。Full Access 在 Node 页面独立配置。" onClose={() => { if (!busy) { setCreating(false); setEditing(null); } }} closeDisabled={!!busy} wide>
      <form className="deployment-form" onSubmit={saveDeployment}>
        {dialogError && <div className="nx-alert is-error" role="alert">{dialogError}</div>}
        {editing && dirtyAgainst(editing) && <div className="nx-alert is-info" role="status">本地表单有未保存修改；Node 仍使用当前 applied revision。</div>}
        <label><span>Node</span><select data-dialog-initial-focus value={draft.nodeID} disabled={!!editing || !!busy} onChange={(event) => { setFolderPickerOpen(false); setDraft((value) => ({ ...value, nodeID: event.target.value, workingFolder: editing ? value.workingFolder : '' })); }} required><option value="">选择 Node</option>{nodes.filter((node) => node.enabled).map((node) => <option key={node.id} value={node.id}>{node.name} · {node.os || 'unknown'}{node.online ? '' : ' · 离线'}</option>)}</select>{editing && <small>已存在的 Deployment 不原地换 Node；如需迁移，请新建 Deployment 后删除旧映射。</small>}</label>
        <label className="is-wide"><span>Project Folder（可选）</span><span className="deployment-working-folder"><input value={draft.workingFolder} onChange={(event) => setDraft((value) => ({ ...value, workingFolder: event.target.value }))} placeholder={selectedNode?.os === 'windows' ? '可选，例如 D:\\Project' : '可选，例如 /srv/project'} disabled={!!busy} spellCheck={false} /><button type="button" className="nx-button is-secondary" disabled={!!busy || !selectedNode} onClick={() => setFolderPickerOpen(true)}><FolderOpen size={15} />浏览 Node</button></span><small>{draft.workingFolder.trim() ? '该路径作为默认 cwd、AGENTS.md 搜索边界与源码 provenance 根；不会限制 Full Access 的 OS 访问范围。' : '留空时相对路径从该 Node 的 AgentDock 默认目录开始，且不会自动发现任何 Project AGENTS.md。'} {selectedNode ? `${selectedNode.name} · ${selectedNode.online ? '在线' : '离线'}` : '先选择 Node'}。</small></label>
        <label><span>Role</span><input value={draft.role} onChange={(event) => setDraft((value) => ({ ...value, role: event.target.value }))} placeholder="primary-development" disabled={!!busy} /></label>
        <label><span>Purpose</span><input value={draft.purpose} onChange={(event) => setDraft((value) => ({ ...value, purpose: event.target.value }))} placeholder="Linux 主开发环境" disabled={!!busy} /></label>
        {selectedNode?.full_access && <div className="nx-alert is-info" role="status"><strong>Full Access 已在 Node 上开启。</strong> 下方细粒度权限作为关闭 Full Access 后的回退配置保留；当前有效权限不受 Project Folder 限制。</div>}
        <fieldset className="deployment-permission-fieldset"><legend>Deployment 细粒度权限{selectedNode?.full_access ? '（当前被 Full Access 覆盖）' : ''}</legend><label><span>Files</span><select value={draft.files} onChange={(event) => setDraft((value) => ({ ...value, files: event.target.value as FileCapability }))} disabled={!!busy}><option value="none">禁用</option><option value="read_only">只读</option><option value="read_write">读写（含删除/移动）</option></select></label><div className="deployment-toggle-grid"><label><input type="checkbox" checked={draft.shell} onChange={(event) => setDraft((value) => ({ ...value, shell: event.target.checked }))} disabled={!!busy} /><span>Shell / Git via shell</span></label><label><input type="checkbox" checked={draft.browser} onChange={(event) => setDraft((value) => ({ ...value, browser: event.target.checked }))} disabled={!!busy} /><span>Browser</span></label><label><input type="checkbox" checked={draft.dynamicMCP} onChange={(event) => setDraft((value) => ({ ...value, dynamicMCP: event.target.checked }))} disabled={!!busy} /><span>Dynamic MCP</span></label><label><input type="checkbox" checked={draft.acp} onChange={(event) => setDraft((value) => ({ ...value, acp: event.target.checked }))} disabled={!!busy} /><span>ACP</span></label></div></fieldset>
        {!selectedNode?.full_access && projectShellPermissionWarning(draft.files, draft.shell) && <div className="nx-alert is-warning" role="status"><CircleAlert size={16} />{projectShellPermissionWarning(draft.files, draft.shell)}</div>}
        <label className="deployment-enabled"><input type="checkbox" checked={draft.enabled} onChange={(event) => setDraft((value) => ({ ...value, enabled: event.target.checked }))} disabled={!!busy} /><span>启用 Deployment</span></label>
        <footer><button type="button" className="nx-button is-secondary" disabled={!!busy} onClick={() => { setCreating(false); setEditing(null); }}>取消</button><button type="submit" className="nx-button" disabled={!!busy || !draft.nodeID}>{busy ? '保存中…' : editing ? '保存 desired 配置' : '创建并应用'}</button></footer>
      </form>
    </Dialog>}

    {folderPickerOpen && selectedNode && <RemoteFolderPicker node={selectedNode} initialPath={draft.workingFolder} onSelect={(path) => setDraft((value) => ({ ...value, workingFolder: path }))} onClose={() => setFolderPickerOpen(false)} />}

    {deleting && <Dialog title="删除 Deployment" description="删除映射会撤销相关 Target，并通知在线 Node 移除 applied Deployment；不会删除 working folder。" onClose={() => { if (!busy) setDeleting(null); }} closeDisabled={!!busy}>
      <section className="project-delete-dialog">{dialogError && <div className="nx-alert is-error" role="alert">{dialogError}</div>}<p><strong>{deleting.working_folder || '未设置 Project Folder'}</strong></p><p>Node 上的源码目录、Git 仓库和其他文件都保持原样。</p><footer><button type="button" className="nx-button is-secondary" disabled={!!busy} onClick={() => setDeleting(null)}>取消</button><button type="button" className="nx-button is-danger" disabled={!!busy} onClick={() => void deleteDeployment()}>{busy ? '删除中…' : '确认删除映射'}</button></footer></section>
    </Dialog>}
  </section>;
}
