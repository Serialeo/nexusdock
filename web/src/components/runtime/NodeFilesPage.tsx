import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from 'react';
import {
  ChevronUp, Copy, File as FileIcon, Folder, FolderOpen, Link2, LoaderCircle, RefreshCw, Server,
} from 'lucide-react';
import { ApiError, api } from '../../api/client';
import { formatTime } from '../../lib/time';
import type { AgentDockNode } from './AgentDockNodes';

type BrowseEntryType = 'directory' | 'file' | 'symlink' | 'other';

type BrowseEntry = {
  name: string;
  path: string;
  type: BrowseEntryType;
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
  source?: string;
};

type LoadOverrides = {
  append?: boolean;
  offset?: number;
  includeHidden?: boolean;
};

const browsePageSize = 200;
const entryOrder: Record<BrowseEntryType, number> = { directory: 0, symlink: 1, file: 2, other: 3 };
const nameCollator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });

function messageOf(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.upstreamCode === 'NOT_FOUND') return '目标 AgentDock 还没有文件浏览 Runtime API，请先升级该节点。';
    return `${error.upstreamCode || error.code || error.status}：${error.message}`;
  }
  return error instanceof Error ? error.message : '目录读取失败';
}

function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value < 0) return '—';
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB`;
  if (value < 1024 * 1024 * 1024) return `${(value / 1024 / 1024).toFixed(1)} MiB`;
  return `${(value / 1024 / 1024 / 1024).toFixed(1)} GiB`;
}

function entryIcon(type: BrowseEntryType) {
  if (type === 'directory') return <Folder size={17} />;
  if (type === 'symlink') return <Link2 size={17} />;
  return <FileIcon size={17} />;
}

function runtimeLabel(node: AgentDockNode): string {
  if (node.os === 'windows') return 'Windows Host';
  if (node.os === 'darwin') return 'macOS Host';
  if (node.os === 'linux') return 'Linux Host';
  return 'Host';
}

export default function NodeFilesPage({ node, refreshToken }: { node: AgentDockNode; refreshToken: number }) {
  const [pathInput, setPathInput] = useState('');
  const [currentPath, setCurrentPath] = useState('');
  const [parentPath, setParentPath] = useState('');
  const [entries, setEntries] = useState<BrowseEntry[]>([]);
  const [nextOffset, setNextOffset] = useState<number | undefined>();
  const [includeHidden, setIncludeHidden] = useState(false);
  const [partial, setPartial] = useState(false);
  const [skippedCount, setSkippedCount] = useState(0);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const requestRef = useRef<AbortController | null>(null);
  const requestIDRef = useRef(0);

  const loadDirectory = useCallback(async (targetPath: string, overrides: LoadOverrides = {}) => {
    const append = Boolean(overrides.append);
    const offset = overrides.offset ?? 0;
    const nextHidden = overrides.includeHidden ?? includeHidden;
    const requestID = ++requestIDRef.current;
    requestRef.current?.abort();
    const controller = new AbortController();
    requestRef.current = controller;
    if (append) setLoadingMore(true);
    else {
      setLoading(true);
      setEntries([]);
      setNextOffset(undefined);
    }
    setError('');
    setNotice('');

    if (!node.online) {
      setError('目标 AgentDock 当前离线。');
      setLoading(false);
      setLoadingMore(false);
      return;
    }

    const params = new URLSearchParams();
    if (targetPath !== '') params.set('path', targetPath);
    params.set('limit', String(browsePageSize));
    if (offset > 0) params.set('offset', String(offset));
    if (nextHidden) params.set('include_hidden', 'true');

    try {
      const response = await api<BrowseResponse>(`/v1/runtime/nodes/${encodeURIComponent(node.id)}/files?${params.toString()}`, {
        signal: controller.signal,
        timeoutMs: 12_000,
      });
      if (requestID !== requestIDRef.current) return;
      setCurrentPath(response.path || targetPath);
      setPathInput(response.path || targetPath);
      setParentPath(response.parent_path || '');
      setPartial(Boolean(response.partial));
      setSkippedCount(Number(response.skipped_count) || 0);
      setNextOffset(response.truncated ? response.next_offset : undefined);
      setEntries((current) => {
        const combined = append ? [...current, ...(response.entries || [])] : (response.entries || []);
        const unique = new Map<string, BrowseEntry>();
        for (const entry of combined) unique.set(entry.path, entry);
        return [...unique.values()];
      });
    } catch (cause) {
      if (requestID !== requestIDRef.current) return;
      if (cause instanceof Error && cause.message === '请求已取消') return;
      setError(messageOf(cause));
    } finally {
      if (requestID === requestIDRef.current) {
        setLoading(false);
        setLoadingMore(false);
      }
    }
  }, [includeHidden, node.id, node.online]);

  useEffect(() => {
    setPathInput('');
    setCurrentPath('');
    setParentPath('');
    setIncludeHidden(false);
    void loadDirectory('', { includeHidden: false });
    return () => requestRef.current?.abort();
    // loadDirectory intentionally changes with node identity.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [node.id, refreshToken]);

  const sortedEntries = useMemo(() => [...entries].sort((left, right) => {
    const typeDifference = entryOrder[left.type] - entryOrder[right.type];
    return typeDifference !== 0 ? typeDifference : nameCollator.compare(left.name, right.name);
  }), [entries]);

  function submitPath(event: FormEvent) {
    event.preventDefault();
    void loadDirectory(pathInput);
  }

  function toggleHidden(next: boolean) {
    setIncludeHidden(next);
    void loadDirectory(currentPath, { includeHidden: next, offset: 0 });
  }

  async function copyPath(path: string) {
    try {
      await navigator.clipboard.writeText(path);
      setNotice('路径已复制。');
    } catch {
      setNotice('无法自动复制，请手动选择路径。');
    }
  }

  const placeholder = node.os === 'windows' ? 'D:\\Project' : '/srv/project';

  return <section className="node-files-page">
    <section className="node-files-toolbar">
      <div className="node-files-runtime">
        <span><Server size={14} />{runtimeLabel(node)}</span>
        <label className="node-files-hidden"><input type="checkbox" checked={includeHidden} onChange={(event) => toggleHidden(event.target.checked)} disabled={loading} /><span>显示隐藏项</span></label>
      </div>
      <form className="node-files-pathbar" onSubmit={submitPath}>
        <button type="button" className="nx-button is-secondary is-small" title="上一级目录" disabled={!parentPath || loading} onClick={() => void loadDirectory(parentPath)}><ChevronUp size={15} />上一级</button>
        <label><span className="sr-only">目录路径</span><input value={pathInput} onChange={(event) => setPathInput(event.target.value)} placeholder={placeholder} spellCheck={false} autoCapitalize="off" autoCorrect="off" /></label>
        <button type="submit" className="nx-button is-small" disabled={loading}>前往</button>
        <button type="button" className="nx-button is-secondary is-small" title="刷新当前目录" disabled={loading} onClick={() => void loadDirectory(currentPath)}><RefreshCw size={15} />刷新</button>
      </form>
    </section>

    {(error || notice || partial) && <div className={`nx-alert ${error ? 'is-error' : partial ? 'is-info' : 'is-success'}`} role={error ? 'alert' : 'status'}>{error || (partial ? `部分目录项无法读取，已跳过 ${skippedCount} 项。` : notice)}</div>}

    <section className="node-files-browser" aria-busy={loading || loadingMore}>
      <header><div><FolderOpen size={16} /><code title={currentPath}>{currentPath || '正在解析默认目录…'}</code></div><span>已加载 {entries.length} 项{nextOffset !== undefined ? ' · 还有更多' : ''}</span></header>
      {loading ? <div className="node-files-empty"><LoaderCircle size={20} className="is-spinning" />正在读取当前目录…</div> : sortedEntries.length === 0 ? <div className="node-files-empty">当前目录没有可显示的项目。</div> : <div className="node-files-list">
        {sortedEntries.map((entry) => <div className={`node-files-row is-${entry.type}`} key={entry.path}>
          <span className="node-files-kind">{entryIcon(entry.type)}</span>
          {entry.type === 'directory' ? <button type="button" className="node-files-name" title={entry.path} onClick={() => void loadDirectory(entry.path)}><strong>{entry.name}</strong><small>文件夹</small></button> : <span className="node-files-name" title={entry.path}><strong>{entry.name}</strong><small>{entry.type === 'symlink' ? '符号链接' : entry.type === 'other' ? '特殊文件' : formatBytes(entry.size_bytes)}</small></span>}
          <span className="node-files-modified">{formatTime(entry.modified, { compact: true })}</span>
          <button type="button" className="node-files-copy" title="复制完整路径" aria-label={`复制 ${entry.name} 的完整路径`} onClick={() => void copyPath(entry.path)}><Copy size={14} /></button>
        </div>)}
      </div>}
      {nextOffset !== undefined && <footer><button type="button" className="nx-button is-secondary" disabled={loadingMore || nextOffset > 10000} onClick={() => void loadDirectory(currentPath, { append: true, offset: nextOffset })}>{loadingMore ? <><LoaderCircle size={15} className="is-spinning" />加载中…</> : `继续加载（从第 ${nextOffset + 1} 项）`}</button>{nextOffset > 10000 && <span>目录过大，已达到浏览上限。</span>}</footer>}
    </section>

  </section>;
}
