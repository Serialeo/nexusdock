import { useCallback, useEffect, useMemo, useState, type FormEvent } from 'react';
import { CircleAlert, CirclePlus, FolderKanban, Pencil, Power, RefreshCw, Trash2 } from 'lucide-react';
import { ApiError, api } from '../../api/client';
import { formatTime } from '../../lib/time';
import Dialog from '../Dialog';
import type { AgentDockNode } from '../runtime/AgentDockNodes';
import ProjectDetailPage from './ProjectDetailPage';
import { deploymentIsAvailable, projectIDFromHash } from './projectUiModel';

export type FileCapability = 'none' | 'read_only' | 'read_write';

export type DeploymentPermissions = {
  full_access: boolean;
  files: FileCapability;
  shell: boolean;
  browser: boolean;
  dynamic_mcp: boolean;
  acp: boolean;
};

export type ProjectDeployment = {
  id: string;
  project_id: string;
  node_id: string;
  working_folder: string;
  role: string;
  purpose: string;
  permissions: DeploymentPermissions;
  desired_revision: string;
  applied_revision: string;
  enabled: boolean;
  apply_status: 'draft' | 'pending' | 'applied' | 'failed' | 'disabled' | string;
  last_error?: string;
  created_at: string;
  updated_at: string;
};

export type ProjectRecord = {
  id: string;
  name: string;
  orchestration_policy: string;
  revision: string;
  enabled: boolean;
  created_at: string;
  updated_at: string;
  deployments?: ProjectDeployment[];
};

type ProjectListResponse = { ok: boolean; projects: ProjectRecord[]; count: number };
type ProjectResponse = { ok: boolean; project: ProjectRecord };
type DeploymentListResponse = { ok: boolean; deployments: ProjectDeployment[]; count: number };

type ProjectSummary = {
  project: ProjectRecord;
  deployments: ProjectDeployment[];
  available: number;
  failed: number;
  pending: number;
};

type ProjectDraft = {
  name: string;
  orchestrationPolicy: string;
  enabled: boolean;
};

const emptyDraft: ProjectDraft = { name: '', orchestrationPolicy: '', enabled: true };

function messageOf(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 409 || error.code === 'REVISION_CONFLICT') return '配置已被其他操作更新。页面已保留当前草稿，请刷新后重新确认再保存。';
    return `${error.code || error.status}：${error.message}`;
  }
  return error instanceof Error ? error.message : 'Project 操作失败';
}

function statusLabel(summary: ProjectSummary): { label: string; className: string } {
  if (!summary.project.enabled) return { label: '已停用', className: 'is-muted' };
  if (summary.failed > 0) return { label: `${summary.failed} 个部署失败`, className: 'is-danger' };
  if (summary.pending > 0) return { label: `${summary.pending} 个等待应用`, className: 'is-warning' };
  if (summary.deployments.length === 0) return { label: '尚未部署', className: 'is-muted' };
  if (summary.available === 0) return { label: '暂无可用 Target', className: 'is-warning' };
  return { label: `${summary.available} 个可用 Target`, className: 'is-ok' };
}

export default function ProjectsPage({ nodes, refreshToken }: { nodes: AgentDockNode[]; refreshToken: number }) {
  const [selectedProjectID, setSelectedProjectID] = useState(() => projectIDFromHash(window.location.hash));
  const [summaries, setSummaries] = useState<ProjectSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [revision, setRevision] = useState(0);
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<ProjectRecord | null>(null);
  const [deleting, setDeleting] = useState<ProjectRecord | null>(null);
  const [draft, setDraft] = useState<ProjectDraft>(emptyDraft);
  const [busy, setBusy] = useState('');
  const [dialogError, setDialogError] = useState('');

  const reload = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const list = await api<ProjectListResponse>('/v1/projects');
      const projectSummaries = await Promise.all((list.projects || []).map(async (project) => {
        const deployments = await api<DeploymentListResponse>(`/v1/projects/${encodeURIComponent(project.id)}/deployments`);
        const items = deployments.deployments || [];
        return {
          project,
          deployments: items,
          available: items.filter((item) => deploymentIsAvailable(item, nodes)).length,
          failed: items.filter((item) => item.apply_status === 'failed').length,
          pending: items.filter((item) => item.enabled && (item.apply_status === 'pending' || item.desired_revision !== item.applied_revision)).length,
        } satisfies ProjectSummary;
      }));
      setSummaries(projectSummaries);
    } catch (cause) {
      setError(messageOf(cause));
    } finally {
      setLoading(false);
    }
  }, [nodes]);

  useEffect(() => { void reload(); }, [reload, refreshToken, revision]);
  useEffect(() => {
    const onHashChange = () => setSelectedProjectID(projectIDFromHash(window.location.hash));
    window.addEventListener('hashchange', onHashChange);
    return () => window.removeEventListener('hashchange', onHashChange);
  }, []);

  const totals = useMemo(() => ({
    enabled: summaries.filter((item) => item.project.enabled).length,
    deployments: summaries.reduce((sum, item) => sum + item.deployments.length, 0),
    available: summaries.reduce((sum, item) => sum + item.available, 0),
  }), [summaries]);

  function openCreate() {
    setDraft(emptyDraft);
    setDialogError('');
    setCreating(true);
  }

  function openEdit(project: ProjectRecord) {
    setDraft({ name: project.name, orchestrationPolicy: project.orchestration_policy || '', enabled: project.enabled });
    setDialogError('');
    setEditing(project);
  }

  async function submitProject(event: FormEvent) {
    event.preventDefault();
    if (busy || !draft.name.trim()) return;
    setBusy('project-save');
    setDialogError('');
    try {
      if (editing) {
        await api<ProjectResponse>(`/v1/projects/${encodeURIComponent(editing.id)}`, {
          method: 'PUT',
          body: JSON.stringify({
            expected_revision: editing.revision,
            name: draft.name.trim(),
            orchestration_policy: draft.orchestrationPolicy.trim(),
            enabled: draft.enabled,
          }),
        });
        setNotice(`Project「${draft.name.trim()}」已保存。`);
        setEditing(null);
      } else {
        await api<ProjectResponse>('/v1/projects', {
          method: 'POST',
          body: JSON.stringify({
            name: draft.name.trim(),
            orchestration_policy: draft.orchestrationPolicy.trim(),
            enabled: draft.enabled,
          }),
        });
        setNotice(`Project「${draft.name.trim()}」已创建。`);
        setCreating(false);
      }
      setRevision((value) => value + 1);
    } catch (cause) {
      setDialogError(messageOf(cause));
    } finally {
      setBusy('');
    }
  }

  async function toggleProject(project: ProjectRecord) {
    if (busy) return;
    setBusy(`toggle:${project.id}`);
    setError('');
    try {
      await api<ProjectResponse>(`/v1/projects/${encodeURIComponent(project.id)}`, {
        method: 'PUT',
        body: JSON.stringify({
          expected_revision: project.revision,
          name: project.name,
          orchestration_policy: project.orchestration_policy,
          enabled: !project.enabled,
        }),
      });
      setNotice(project.enabled ? `Project「${project.name}」已停用；现有 Target 将撤销。` : `Project「${project.name}」已启用。`);
      setRevision((value) => value + 1);
    } catch (cause) {
      setError(messageOf(cause));
    } finally {
      setBusy('');
    }
  }

  async function deleteProject() {
    if (!deleting || busy) return;
    setBusy('project-delete');
    setDialogError('');
    try {
      await api<{ ok: boolean; project_id: string; deleted: boolean }>(`/v1/projects/${encodeURIComponent(deleting.id)}`, {
        method: 'DELETE',
        body: JSON.stringify({ expected_revision: deleting.revision }),
      });
      setNotice(`Project「${deleting.name}」已删除；Target 已撤销，Node 上的源码目录未删除。`);
      setDeleting(null);
      setRevision((value) => value + 1);
    } catch (cause) {
      setDialogError(messageOf(cause));
    } finally {
      setBusy('');
    }
  }

  if (selectedProjectID) {
    return <ProjectDetailPage projectID={selectedProjectID} nodes={nodes} onBack={() => { window.location.hash = 'projects'; }} />;
  }

  return <section className="projects-page">
    <header className="projects-page-heading">
      <div><span className="nexus-eyebrow">WORK / PROJECTS</span><h2>Projects</h2><p>Project 组织跨节点工作；源码保留在各 Deployment 的原生目录中。</p></div>
      <div className="projects-page-actions"><button type="button" className="nx-button is-secondary" disabled={loading} onClick={() => void reload()}><RefreshCw size={15} />刷新</button><button type="button" className="nx-button" onClick={openCreate}><CirclePlus size={16} />新建 Project</button></div>
    </header>

    <section className="project-summary-strip" aria-label="Project 摘要">
      <span><small>启用 Project</small><strong>{totals.enabled}</strong></span>
      <span><small>Deployment</small><strong>{totals.deployments}</strong></span>
      <span><small>可用 Target</small><strong>{totals.available}</strong></span>
    </section>

    {error && <div className="nx-alert is-error" role="alert"><CircleAlert size={16} />{error}</div>}
    {notice && <div className="nx-alert is-success" role="status">{notice}</div>}

    <section className="project-list" aria-busy={loading}>
      {loading && summaries.length === 0 && <div className="project-empty">正在读取 Projects…</div>}
      {!loading && summaries.length === 0 && <div className="project-empty"><FolderKanban size={28} /><strong>还没有 Project</strong><span>创建 Project 后，再为它添加一个或多个 Node Deployment。</span><button type="button" className="nx-button" onClick={openCreate}>创建第一个 Project</button></div>}
      {summaries.map((summary) => {
        const status = statusLabel(summary);
        return <article className={`project-card ${summary.project.enabled ? '' : 'is-disabled'}`} key={summary.project.id}>
          <button type="button" className="project-card-main" onClick={() => { window.location.hash = `projects/${encodeURIComponent(summary.project.id)}`; }} aria-label={`打开 Project ${summary.project.name}`}>
            <span className="project-card-icon"><FolderKanban size={18} /></span>
            <span className="project-card-copy"><strong>{summary.project.name}</strong><small>{summary.project.orchestration_policy || '尚未填写协作说明'}</small><code>{summary.project.id}</code></span>
          </button>
          <div className="project-card-stats">
            <span><small>Deployment</small><strong>{summary.deployments.length}</strong></span>
            <span><small>可用 Target</small><strong>{summary.available}</strong></span>
            <span><small>最近更新</small><strong>{formatTime(summary.project.updated_at, { compact: true })}</strong></span>
          </div>
          <div className="project-card-actions">
            <span className={`project-state ${status.className}`}>{status.label}</span>
            <button type="button" className="nx-button is-secondary is-small" disabled={!!busy} onClick={() => openEdit(summary.project)}><Pencil size={14} />编辑</button>
            <button type="button" className="nx-button is-secondary is-small" disabled={!!busy} onClick={() => void toggleProject(summary.project)}><Power size={14} />{summary.project.enabled ? '停用' : '启用'}</button>
            <button type="button" className="nx-button is-danger is-small" disabled={!!busy} onClick={() => { setDialogError(''); setDeleting(summary.project); }}><Trash2 size={14} />删除</button>
          </div>
        </article>;
      })}
    </section>

    {(creating || editing) && <Dialog title={editing ? '编辑 Project' : '新建 Project'} description="Project 只保存逻辑协作配置，不复制 Node 上的源码。" onClose={() => { if (!busy) { setCreating(false); setEditing(null); } }} closeDisabled={!!busy}>
      <form className="project-dialog-form" onSubmit={submitProject}>
        {dialogError && <div className="nx-alert is-error" role="alert">{dialogError}</div>}
        <label><span>名称</span><input data-dialog-initial-focus value={draft.name} onChange={(event) => setDraft((value) => ({ ...value, name: event.target.value }))} maxLength={120} required disabled={!!busy} /></label>
        <label><span>协作说明</span><textarea value={draft.orchestrationPolicy} onChange={(event) => setDraft((value) => ({ ...value, orchestrationPolicy: event.target.value }))} rows={5} maxLength={4000} placeholder="说明多节点如何协作；这是软指导，不提升权限。" disabled={!!busy} /></label>
        <label className="project-checkbox"><input type="checkbox" checked={draft.enabled} onChange={(event) => setDraft((value) => ({ ...value, enabled: event.target.checked }))} disabled={!!busy} /><span>启用 Project</span></label>
        <footer><button type="button" className="nx-button is-secondary" disabled={!!busy} onClick={() => { setCreating(false); setEditing(null); }}>取消</button><button type="submit" className="nx-button" disabled={!!busy || !draft.name.trim()}>{busy ? '保存中…' : '保存'}</button></footer>
      </form>
    </Dialog>}

    {deleting && <Dialog title="删除 Project" description="删除 Project 会撤销它的 WorkSession/Target，但不会删除任何 AgentDock Node 上的源码目录。" onClose={() => { if (!busy) setDeleting(null); }} closeDisabled={!!busy}>
      <section className="project-delete-dialog">
        {dialogError && <div className="nx-alert is-error" role="alert">{dialogError}</div>}
        <p>将删除 <strong>{deleting.name}</strong> 的 Nexus 配置与 Deployment 记录。</p>
        <p>Node 上 <strong>不会</strong>执行目录删除、Git 清理或源码同步。</p>
        <footer><button type="button" className="nx-button is-secondary" disabled={!!busy} onClick={() => setDeleting(null)}>取消</button><button type="button" className="nx-button is-danger" disabled={!!busy} onClick={() => void deleteProject()}>{busy ? '删除中…' : '确认删除'}</button></footer>
      </section>
    </Dialog>}
  </section>;
}
