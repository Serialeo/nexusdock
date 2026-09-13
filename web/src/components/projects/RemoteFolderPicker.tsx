import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from 'react';
import { ChevronRight, ChevronUp, Folder, FolderOpen, LoaderCircle, RefreshCw, Server } from 'lucide-react';
import { ApiError, api } from '../../api/client';
import Dialog from '../Dialog';
import type { AgentDockNode } from '../runtime/AgentDockNodes';
import { projectPathBreadcrumbs } from './projectUiModel';

type BrowseEntry = {
  name: string;
  path: string;
  type: 'directory' | 'file' | 'symlink' | 'other';
  size_bytes: number;
  modified: string;
  is_hidden: boolean;
};

type BrowseResponse = {
  ok: boolean;
  node_id: string;
  path: string;
  parent_path: string;
  entries: BrowseEntry[];
  offset: number;
  limit: number;
  next_offset?: number;
  truncated: boolean;
  partial: boolean;
  skipped_count: number;
};

const pageSize = 200;
const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });

function messageOf(error: unknown): string {
  if (error instanceof ApiError) return `${error.upstreamCode || error.code || error.status}：${error.message}`;
  return error instanceof Error ? error.message : '目录读取失败';
}

export default function RemoteFolderPicker({ node, initialPath, onSelect, onClose }: {
  node: AgentDockNode;
  initialPath: string;
  onSelect: (path: string) => void;
  onClose: () => void;
}) {
  const [pathInput, setPathInput] = useState(initialPath);
  const [currentPath, setCurrentPath] = useState('');
  const [parentPath, setParentPath] = useState('');
  const [entries, setEntries] = useState<BrowseEntry[]>([]);
  const [nextOffset, setNextOffset] = useState<number | undefined>();
  const [includeHidden, setIncludeHidden] = useState(false);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [partial, setPartial] = useState(false);
  const [skippedCount, setSkippedCount] = useState(0);
  const [error, setError] = useState('');
  const requestRef = useRef<AbortController | null>(null);
  const requestIDRef = useRef(0);

  const loadDirectory = useCallback(async (targetPath: string, options: { append?: boolean; offset?: number; includeHidden?: boolean } = {}) => {
    const requestID = ++requestIDRef.current;
    requestRef.current?.abort();
    const controller = new AbortController();
    requestRef.current = controller;
    const append = Boolean(options.append);
    const offset = options.offset ?? 0;
    const hidden = options.includeHidden ?? includeHidden;
    if (append) setLoadingMore(true);
    else { setLoading(true); setEntries([]); setNextOffset(undefined); }
    setError('');
    if (!node.online) {
      setError('目标 AgentDock 当前离线，无法浏览真实目录。');
      setLoading(false);
      setLoadingMore(false);
      return;
    }
    const params = new URLSearchParams();
    if (targetPath.trim()) params.set('path', targetPath.trim());
    params.set('limit', String(pageSize));
    if (offset > 0) params.set('offset', String(offset));
    if (hidden) params.set('include_hidden', 'true');
    try {
      const response = await api<BrowseResponse>(`/v1/runtime/nodes/${encodeURIComponent(node.id)}/files?${params.toString()}`, { signal: controller.signal, timeoutMs: 12_000 });
      if (requestID !== requestIDRef.current || response.node_id !== node.id) return;
      const resolved = response.path || targetPath;
      setCurrentPath(resolved);
      setPathInput(resolved);
      setParentPath(response.parent_path || '');
      setPartial(Boolean(response.partial));
      setSkippedCount(Number(response.skipped_count) || 0);
      setNextOffset(response.truncated ? response.next_offset : undefined);
      setEntries((current) => {
        const combined = append ? [...current, ...(response.entries || [])] : (response.entries || []);
        const unique = new Map(combined.map((entry) => [entry.path, entry]));
        return [...unique.values()];
      });
    } catch (cause) {
      if (requestID !== requestIDRef.current) return;
      if (cause instanceof Error && cause.message === '请求已取消') return;
      setError(messageOf(cause));
    } finally {
      if (requestID === requestIDRef.current) { setLoading(false); setLoadingMore(false); }
    }
  }, [includeHidden, node.id, node.online]);

  useEffect(() => {
    requestIDRef.current += 1;
    requestRef.current?.abort();
    setPathInput(initialPath);
    setCurrentPath('');
    setParentPath('');
    setEntries([]);
    setIncludeHidden(false);
    void loadDirectory(initialPath, { includeHidden: false });
    return () => requestRef.current?.abort();
    // loadDirectory changes on node identity; this intentionally restarts browsing.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [node.id]);

  const directories = useMemo(() => entries.filter((entry) => entry.type === 'directory').sort((a, b) => collator.compare(a.name, b.name)), [entries]);
  const crumbs = useMemo(() => projectPathBreadcrumbs(currentPath, node.os), [currentPath, node.os]);

  function submitPath(event: FormEvent) {
    event.preventDefault();
    void loadDirectory(pathInput);
  }

  function selectCurrent() {
    if (!currentPath || loading) return;
    onSelect(currentPath);
    onClose();
  }

  return <Dialog title="选择 Node 工作目录" description={`${node.name} · ${node.os || 'unknown'}/${node.arch || 'unknown'} · 只浏览真实目录，不创建或修改文件。`} onClose={onClose} closeDisabled={loadingMore} wide>
    <section className="remote-folder-picker">
      <div className="remote-folder-node"><Server size={15} /><strong>{node.name}</strong><code>{node.id}</code><span className={node.online ? 'is-online' : 'is-offline'}>{node.online ? '在线' : '离线'}</span></div>
      <form className="remote-folder-pathbar" onSubmit={submitPath}>
        <button type="button" className="nx-button is-secondary is-small" disabled={!parentPath || loading} onClick={() => void loadDirectory(parentPath)}><ChevronUp size={14} />上一级</button>
        <label><span className="sr-only">Node 目录路径</span><input data-dialog-initial-focus value={pathInput} onChange={(event) => setPathInput(event.target.value)} placeholder={node.os === 'windows' ? 'D:\\Project' : '/srv/project'} spellCheck={false} autoCapitalize="off" autoCorrect="off" /></label>
        <button type="submit" className="nx-button is-small" disabled={loading || !node.online}>前往</button>
        <button type="button" className="nx-button is-secondary is-small" disabled={loading || !node.online} onClick={() => void loadDirectory(currentPath)}><RefreshCw size={14} />刷新</button>
      </form>
      <div className="remote-folder-options"><label><input type="checkbox" checked={includeHidden} disabled={loading || !node.online} onChange={(event) => { const next = event.target.checked; setIncludeHidden(next); void loadDirectory(currentPath, { includeHidden: next }); }} />显示隐藏目录</label><span>只列出目录；文件不会作为 working folder 选项。</span></div>
      {crumbs.length > 0 && <nav className="remote-folder-crumbs" aria-label="当前目录面包屑">{crumbs.map((crumb, index) => <span key={`${crumb.path}:${index}`}>{index > 0 && <ChevronRight size={12} />}<button type="button" title={crumb.path} onClick={() => void loadDirectory(crumb.path)}>{crumb.label}</button></span>)}</nav>}
      {error && <div className="nx-alert is-error" role="alert">{error}</div>}
      {partial && !error && <div className="nx-alert is-info" role="status">部分目录无法读取，已跳过 {skippedCount} 项。</div>}
      <section className="remote-folder-browser" aria-busy={loading || loadingMore}>
        <header><FolderOpen size={16} /><code title={currentPath}>{currentPath || '正在解析目录…'}</code><span>{directories.length} 个目录{nextOffset !== undefined ? ' · 还有更多' : ''}</span></header>
        {loading ? <div className="remote-folder-empty"><LoaderCircle size={20} className="is-spinning" />正在读取 Node 目录…</div> : directories.length === 0 ? <div className="remote-folder-empty">当前路径下没有可显示的子目录。</div> : <div className="remote-folder-list">{directories.map((entry) => <button type="button" key={entry.path} title={entry.path} onClick={() => void loadDirectory(entry.path)}><Folder size={16} /><span><strong>{entry.name}</strong><small>{entry.path}</small></span><ChevronRight size={14} /></button>)}</div>}
        {nextOffset !== undefined && <footer><button type="button" className="nx-button is-secondary" disabled={loadingMore || nextOffset > 10000} onClick={() => void loadDirectory(currentPath, { append: true, offset: nextOffset ?? 0 })}>{loadingMore ? '加载中…' : `继续加载（第 ${nextOffset + 1} 项起）`}</button>{nextOffset > 10000 && <span>已达到浏览上限。</span>}</footer>}
      </section>
      <footer className="remote-folder-footer"><div><small>将选择</small><code>{currentPath || '—'}</code></div><div><button type="button" className="nx-button is-secondary" onClick={onClose}>取消</button><button type="button" className="nx-button" disabled={!currentPath || loading || !node.online} onClick={selectCurrent}>选择当前目录</button></div></footer>
    </section>
  </Dialog>;
}
