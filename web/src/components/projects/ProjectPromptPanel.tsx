import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react';
import { CircleAlert, FileText, Pencil, Plus, RefreshCw } from 'lucide-react';
import { ApiError, api } from '../../api/client';
import Dialog from '../Dialog';
import type { AgentDockNode } from '../runtime/AgentDockNodes';
import type { ProjectDeployment } from './ProjectsPage';
import { deploymentIsAvailable } from './projectUiModel';

type PromptSource = {
  path: string;
  scope: string;
  bytes: number;
  content: string;
};

type ProjectPrompt = {
  complete: boolean;
  bytes: number;
  sources: PromptSource[];
};

type PromptResult = {
  deployment_id: string;
  cwd_rel: string;
  prompt: ProjectPrompt;
};

type PromptResponse = {
  ok: boolean;
  project_id: string;
  deployment: ProjectDeployment;
  working_folder: string;
  result: PromptResult;
};

type PromptWriteResult = {
  deployment_id: string;
  scope: string;
  source: PromptSource;
  prompt: ProjectPrompt;
  created: boolean;
};

type PromptWriteResponse = Omit<PromptResponse, 'result'> & { result: PromptWriteResult };

const maxPromptFileBytes = 64 * 1024;
const encoder = new TextEncoder();

function messageOf(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.code === 'REVISION_CONFLICT') return '规则文件已变化。当前草稿已保留；请刷新后人工合并。';
    if (error.code === 'PROJECT_PROMPT_TOO_LARGE') return 'AGENTS.md 超过 64 KiB，无法保存或完整读取。';
    if (error.code === 'PROMPT_SCOPE_ESCAPE') return '所选目录不在该 Deployment 的 Project Prompt 边界内。';
    if (error.code === 'DEPLOYMENT_NOT_READY') return '该 Deployment 尚未 applied 或目标 Node 不在线，不能读取真实 Project Prompt。';
    return `${error.code || error.status}：${error.message}`;
  }
  return error instanceof Error ? error.message : 'Project Prompt 操作失败';
}

export default function ProjectPromptPanel({ projectID, deployments, nodes }: {
  projectID: string;
  deployments: ProjectDeployment[];
  nodes: AgentDockNode[];
}) {
  const selectable = useMemo(() => deployments.filter((item) => item.enabled), [deployments]);
  const [deploymentID, setDeploymentID] = useState(() => selectable[0]?.id || '');
  const [cwdRel, setCWDRel] = useState('.');
  const [result, setResult] = useState<PromptResponse | null>(null);
  const [selectedSourcePath, setSelectedSourcePath] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [editor, setEditor] = useState<{ source?: PromptSource; scope: string; content: string; create: boolean } | null>(null);
  const [saving, setSaving] = useState(false);
  const [editorError, setEditorError] = useState('');
  const requestIDRef = useRef(0);
  const requestRef = useRef<AbortController | null>(null);

  const deployment = deployments.find((item) => item.id === deploymentID);
  const node = nodes.find((item) => item.id === deployment?.node_id);
  const hasProjectFolder = !!deployment?.working_folder?.trim();
  const selectedSource = result?.result.prompt.sources.find((source) => source.path === selectedSourcePath)
    || result?.result.prompt.sources.at(-1);
  const currentScopeSource = result?.result.prompt.sources.find((source) => source.scope === result.result.cwd_rel);

  useEffect(() => {
    if (deploymentID && deployments.some((item) => item.id === deploymentID)) return;
    setDeploymentID(selectable[0]?.id || '');
  }, [deploymentID, deployments, selectable]);

  useEffect(() => () => requestRef.current?.abort(), []);

  async function loadPrompt(targetDeploymentID = deploymentID, targetCWD = cwdRel) {
    if (!targetDeploymentID) {
      setResult(null);
      setError('先为 Project 添加一个 Deployment。');
      return;
    }
    const requestID = ++requestIDRef.current;
    requestRef.current?.abort();
    const controller = new AbortController();
    requestRef.current = controller;
    setLoading(true);
    setError('');
    setNotice('');
    try {
      const response = await api<PromptResponse>(`/v1/projects/${encodeURIComponent(projectID)}/deployments/${encodeURIComponent(targetDeploymentID)}/prompt?cwd_rel=${encodeURIComponent(targetCWD.trim() || '.')}`, {
        signal: controller.signal,
        timeoutMs: 20_000,
      });
      if (requestID !== requestIDRef.current) return;
      setResult(response);
      setCWDRel(response.result.cwd_rel);
      const exact = response.result.prompt.sources.find((source) => source.scope === response.result.cwd_rel);
      setSelectedSourcePath(exact?.path || response.result.prompt.sources.at(-1)?.path || '');
    } catch (cause) {
      if (requestID !== requestIDRef.current) return;
      if (cause instanceof Error && cause.message === '请求已取消') return;
      setResult(null);
      setError(messageOf(cause));
    } finally {
      if (requestID === requestIDRef.current) setLoading(false);
    }
  }

  useEffect(() => {
    setResult(null);
    setSelectedSourcePath('');
    setCWDRel('.');
    if (deploymentID) void loadPrompt(deploymentID, '.');
    // loadPrompt intentionally reads current state but restarts only on Deployment identity.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [deploymentID]);

  function submitCWD(event: FormEvent) {
    event.preventDefault();
    void loadPrompt();
  }

  function openEdit(source: PromptSource) {
    setEditorError('');
    setEditor({ source, scope: source.scope, content: source.content, create: false });
  }

  function openCreate() {
    if (!result) return;
    setEditorError('');
    setEditor({ scope: result.result.cwd_rel, content: '# Project rules\n\n', create: true });
  }

  async function saveRule(event: FormEvent) {
    event.preventDefault();
    if (!editor || !deploymentID || saving) return;
    const bytes = encoder.encode(editor.content).byteLength;
    if (bytes > maxPromptFileBytes) {
      setEditorError(`UTF-8 正文 ${bytes.toLocaleString()} bytes，超过 64 KiB。`);
      return;
    }
    setSaving(true);
    setEditorError('');
    try {
      const response = await api<PromptWriteResponse>(`/v1/projects/${encodeURIComponent(projectID)}/deployments/${encodeURIComponent(deploymentID)}/prompt`, {
        method: 'PUT',
        timeoutMs: 20_000,
        body: JSON.stringify({
          scope: editor.scope,
          content: editor.content,
          expected_content: editor.source?.content || '',
          create: editor.create,
        }),
      });
      const nextResult: PromptResponse = {
        ok: response.ok,
        project_id: response.project_id,
        deployment: response.deployment,
        working_folder: response.working_folder,
        result: {
          deployment_id: response.result.deployment_id,
          cwd_rel: response.result.scope,
          prompt: response.result.prompt,
        },
      };
      setResult(nextResult);
      setCWDRel(response.result.scope);
      setSelectedSourcePath(response.result.source.path);
      setNotice(response.result.created ? `${response.result.source.path} 已创建。活动 Target 在下次使用前会刷新规则。` : `${response.result.source.path} 已保存。活动 Target 在下次使用前会刷新规则。`);
      setEditor(null);
    } catch (cause) {
      setEditorError(messageOf(cause));
    } finally {
      setSaving(false);
    }
  }

  const editorBytes = editor ? encoder.encode(editor.content).byteLength : 0;
  const deploymentReady = deployment ? deploymentIsAvailable(deployment, nodes) : false;

  return <section className="project-prompt-panel">
    <header className="project-prompt-toolbar">
      <label><span>Deployment</span><select value={deploymentID} onChange={(event) => setDeploymentID(event.target.value)}><option value="">选择 Deployment</option>{selectable.map((item) => { const itemNode = nodes.find((candidate) => candidate.id === item.node_id); return <option key={item.id} value={item.id}>{itemNode?.name || item.node_id} · {item.role || item.id}{deploymentIsAvailable(item, nodes) ? '' : ' · 当前不可读取'}</option>; })}</select></label>
      <form onSubmit={submitCWD}><label><span>项目内子目录</span><input value={cwdRel} onChange={(event) => setCWDRel(event.target.value)} placeholder="." spellCheck={false} /></label><button type="submit" className="nx-button is-secondary" disabled={loading || !deploymentID}><RefreshCw size={15} />刷新规则</button></form>
      <div className="project-prompt-target"><small>目标</small><strong>{node?.name || '未选择 Node'}</strong><code>{deployment?.working_folder || '未配置 Project Folder'}</code><span>{deploymentReady ? '已应用且在线' : '未就绪'}</span></div>
    </header>

    {error && <div className="nx-alert is-error" role="alert"><CircleAlert size={16} />{error}</div>}
    {notice && <div className="nx-alert is-success" role="status">{notice}</div>}
    {loading && <div className="project-prompt-empty">正在从目标 AgentDock 读取完整规则链…</div>}

    {!loading && result && <>
      <section className="project-prompt-meta" aria-label="Project Prompt 状态">
        <span><small>实际 cwd</small><code>{result.result.cwd_rel}</code></span>
        <span><small>完整正文</small><strong>{result.result.prompt.complete ? '是' : '否'}</strong></span>
        <span><small>总字节</small><strong>{result.result.prompt.bytes.toLocaleString()}</strong></span>
      </section>
      {result.result.prompt.sources.length === 0 ? <section className="project-prompt-empty"><FileText size={25} /><strong>{hasProjectFolder ? '当前适用链没有 AGENTS.md' : '未配置 Project Folder'}</strong><span>{hasProjectFolder ? '缺失规则与读取失败是不同状态；这里表示 Node 已完整读取并确认没有适用规则文件。' : 'Folder 留空时不自动发现 Node 默认目录、HOME 或系统目录中的 AGENTS.md；Full Access 也不会扩大 Project Prompt 搜索范围。'}</span>{hasProjectFolder && <button type="button" className="nx-button" onClick={openCreate}><Plus size={15} />在 {result.result.cwd_rel} 创建 AGENTS.md</button>}</section> : <section className="project-prompt-workspace">
        <aside className="project-prompt-sources"><header><strong>适用顺序</strong><small>root → cwd</small></header>{result.result.prompt.sources.map((source, index) => <button type="button" className={(selectedSource?.path === source.path) ? 'is-active' : ''} key={`${source.path}:${source.scope}:${index}`} onClick={() => setSelectedSourcePath(source.path)}><span>{index + 1}</span><span><strong>{source.path}</strong><small>scope {source.scope} · {source.bytes} bytes</small></span></button>)}{!currentScopeSource && <button type="button" className="project-prompt-create-source" onClick={openCreate}><Plus size={14} /><span><strong>创建当前 scope 规则</strong><small>{result.result.cwd_rel}/AGENTS.md</small></span></button>}</aside>
        <article className="project-prompt-preview">{selectedSource ? <><header><div><strong>{selectedSource.path}</strong><small>scope {selectedSource.scope}</small></div><button type="button" className="nx-button is-secondary is-small" onClick={() => openEdit(selectedSource)}><Pencil size={14} />编辑实际文件</button></header><dl><div><dt>Bytes</dt><dd>{selectedSource.bytes}</dd></div></dl><pre>{selectedSource.content}</pre></> : <div className="project-prompt-empty">选择一个规则来源查看完整正文。</div>}</article>
      </section>}
    </>}

    {editor && <Dialog title={editor.create ? '创建 AGENTS.md' : `编辑 ${editor.source?.path || 'AGENTS.md'}`} description={`${node?.name || 'Node'} · ${deployment?.working_folder || '未配置 Project Folder'} · scope ${editor.scope}。正文直接写入目标 Node 项目目录，不复制到 Nexus 数据库。`} onClose={() => { if (!saving) setEditor(null); }} closeDisabled={saving} wide>
      <form className="project-prompt-editor" onSubmit={saveRule}>
        {editorError && <div className="nx-alert is-error" role="alert">{editorError}</div>}
        <div className="project-prompt-editor-path"><span>目标文件</span><code>{editor.scope === '.' ? 'AGENTS.md' : `${editor.scope}/AGENTS.md`}</code></div>
        <label><span>完整正文</span><textarea data-dialog-initial-focus rows={18} value={editor.content} onChange={(event) => setEditor((value) => value ? { ...value, content: event.target.value } : value)} spellCheck={false} /></label>
        <div className="project-prompt-editor-meta"><span>保存后已存在的 WorkSession/Target 不会被静默改写；执行前需刷新 context。</span><strong className={editorBytes > maxPromptFileBytes ? 'is-over-limit' : ''}>{editorBytes.toLocaleString()} / {maxPromptFileBytes.toLocaleString()} bytes</strong></div>
        <footer><button type="button" className="nx-button is-secondary" disabled={saving} onClick={() => setEditor(null)}>取消</button><button type="submit" className="nx-button" disabled={saving || editorBytes > maxPromptFileBytes}>{saving ? '保存中…' : editor.create ? '创建规则文件' : '保存规则文件'}</button></footer>
      </form>
    </Dialog>}
  </section>;
}
