import { useEffect, useMemo, useRef, useState, type CSSProperties, type KeyboardEvent } from 'react';
import { ChevronDown, ChevronRight, FileText, Folder, FolderOpen, Plus, Search, X } from 'lucide-react';
import type { RecallEntry, RecallWorkspaceViewModel } from './types';
import { nameOf, normalizePath } from './utils';

type Props = Pick<RecallWorkspaceViewModel, 'state' | 'fileEntries' | 'actions'>;

type RecallTreeNode = {
  name: string;
  path: string;
  folders: RecallTreeNode[];
  files: RecallEntry[];
  fileCount: number;
};

type MutableRecallTreeNode = RecallTreeNode & {
  folderMap: Map<string, MutableRecallTreeNode>;
  folders: MutableRecallTreeNode[];
};

function createFolderNode(name: string, path: string): MutableRecallTreeNode {
  return { name, path, folders: [], files: [], fileCount: 0, folderMap: new Map() };
}

function comparePath(a: string, b: string): number {
  return a.localeCompare(b, 'zh-CN');
}

function buildRecallTree(entries: RecallEntry[]): { root: RecallTreeNode; folderCount: number } {
  const root = createFolderNode('召回库', '');
  let folderCount = 0;

  for (const entry of entries) {
    const parts = normalizePath(entry.path).split('/').filter(Boolean);
    parts.pop();
    let parent = root;
    let currentPath = '';

    for (const part of parts) {
      currentPath = currentPath ? `${currentPath}/${part}` : part;
      let folder = parent.folderMap.get(part);
      if (!folder) {
        folder = createFolderNode(part, currentPath);
        parent.folderMap.set(part, folder);
        parent.folders.push(folder);
        folderCount += 1;
      }
      parent = folder;
    }
    parent.files.push(entry);
  }

  function finalize(node: MutableRecallTreeNode): RecallTreeNode {
    const childFolders = [...node.folders].sort((a, b) => comparePath(a.name, b.name));
    const files = [...node.files].sort((a, b) => comparePath(nameOf(a.path), nameOf(b.path)));

    let fileCount = files.length;
    const folders = childFolders.map((folder) => {
      const finalized = finalize(folder);
      fileCount += finalized.fileCount;
      return finalized;
    });

    return { name: node.name, path: node.path, folders, files, fileCount };
  }

  return { root: finalize(root), folderCount };
}

function treeIndent(depth: number): CSSProperties {
  return { '--tree-depth': depth } as CSSProperties;
}

function collectFolderPaths(node: RecallTreeNode): string[] {
  return node.folders.flatMap((folder) => [folder.path, ...collectFolderPaths(folder)]);
}

function defaultCollapsedFolders(root: RecallTreeNode): Set<string> {
  const collapsed = new Set<string>();
  function visit(folder: RecallTreeNode, depth: number) {
    if (depth >= 1) collapsed.add(folder.path);
    folder.folders.forEach((child) => visit(child, depth + 1));
  }
  root.folders.forEach((folder) => visit(folder, 0));
  return collapsed;
}

function parentFolderPaths(path: string): string[] {
  const parts = normalizePath(path).split('/').filter(Boolean);
  parts.pop();
  let current = '';
  return parts.map((part) => {
    current = current ? `${current}/${part}` : part;
    return current;
  });
}

function parentPath(path: string): string {
  const parts = normalizePath(path).split('/').filter(Boolean);
  parts.pop();
  return parts.join('/') || '根目录';
}

export default function RecallFileBrowser({ state, fileEntries, actions }: Props) {
  const searchActive = Boolean(state.appliedQuery);
  const { root, folderCount } = useMemo(() => buildRecallTree(fileEntries), [fileEntries]);
  const folderPaths = useMemo(() => collectFolderPaths(root), [root]);
  const [collapsedFolders, setCollapsedFolders] = useState<Set<string>>(() => new Set());
  const initializedTree = useRef(false);
  const treeRef = useRef<HTMLDivElement | null>(null);
  const allCollapsed = folderPaths.length > 0 && folderPaths.every((path) => collapsedFolders.has(path));
  const resultSummary = searchActive
    ? `“${state.appliedQuery}” · ${fileEntries.length} 个结果`
    : `${folderCount} 个文件夹 / ${fileEntries.length} 个文件`;

  useEffect(() => {
    if (initializedTree.current || searchActive || fileEntries.length === 0) return;
    setCollapsedFolders(defaultCollapsedFolders(root));
    initializedTree.current = true;
  }, [fileEntries.length, root, searchActive]);

  useEffect(() => {
    if (searchActive || !state.current?.path) return;
    const ancestors = parentFolderPaths(state.current.path);
    if (ancestors.length === 0) return;
    setCollapsedFolders((current) => {
      const next = new Set(current);
      let changed = false;
      for (const path of ancestors) {
        if (!next.delete(path)) continue;
        changed = true;
      }
      return changed ? next : current;
    });
  }, [searchActive, state.current?.path]);

  useEffect(() => {
    if (searchActive || !state.current?.path) return;
    const timer = window.setTimeout(() => {
      const tree = treeRef.current;
      const active = Array.from(tree?.querySelectorAll<HTMLButtonElement>('[data-recall-path]') || [])
        .find((item) => item.dataset.recallPath === state.current?.path);
      const scroller = tree?.closest<HTMLElement>('.recall-files');
      if (!active || !scroller) return;

      const itemRect = active.getBoundingClientRect();
      const scrollerRect = scroller.getBoundingClientRect();
      if (itemRect.top < scrollerRect.top + 8) {
        scroller.scrollTop -= scrollerRect.top + 8 - itemRect.top;
      } else if (itemRect.bottom > scrollerRect.bottom - 8) {
        scroller.scrollTop += itemRect.bottom - scrollerRect.bottom + 8;
      }
    }, 50);
    return () => window.clearTimeout(timer);
  }, [searchActive, state.current?.path]);

  function handleTreeKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    const target = event.target;
    if (!(target instanceof HTMLButtonElement) || target.getAttribute('role') !== 'treeitem') return;

    const items = Array.from(event.currentTarget.querySelectorAll<HTMLButtonElement>('[role="treeitem"]'))
      .filter((item) => !item.disabled && item.offsetParent !== null);
    const index = items.indexOf(target);
    if (index < 0) return;

    const focusItem = (nextIndex: number) => {
      const item = items[nextIndex];
      if (!item) return;
      event.preventDefault();
      item.focus();
    };

    if (event.key === 'ArrowDown') {
      focusItem(Math.min(index + 1, items.length - 1));
      return;
    }
    if (event.key === 'ArrowUp') {
      focusItem(Math.max(index - 1, 0));
      return;
    }
    if (event.key === 'Home') {
      focusItem(0);
      return;
    }
    if (event.key === 'End') {
      focusItem(items.length - 1);
      return;
    }

    const level = Number(target.getAttribute('aria-level') || '1');
    const expanded = target.getAttribute('aria-expanded');
    if (event.key === 'ArrowRight') {
      if (expanded === 'false') {
        event.preventDefault();
        target.click();
        return;
      }
      if (expanded === 'true') {
        const childIndex = items.findIndex((item, itemIndex) => itemIndex > index && Number(item.getAttribute('aria-level') || '1') === level + 1);
        if (childIndex >= 0) focusItem(childIndex);
      }
      return;
    }
    if (event.key !== 'ArrowLeft') return;
    if (expanded === 'true') {
      event.preventDefault();
      target.click();
      return;
    }
    for (let itemIndex = index - 1; itemIndex >= 0; itemIndex -= 1) {
      if (Number(items[itemIndex].getAttribute('aria-level') || '1') !== level - 1) continue;
      focusItem(itemIndex);
      return;
    }
  }

  function toggleFolder(path: string) {
    setCollapsedFolders((current) => {
      const next = new Set(current);
      if (next.has(path)) next.delete(path); else next.add(path);
      return next;
    });
  }

  function toggleAllFolders() {
    setCollapsedFolders(allCollapsed ? new Set() : new Set(folderPaths));
  }

  function renderFiles(files: RecallEntry[], depth: number) {
    return files.map((entry) => {
      const active = state.current?.path === entry.path;
      return <button
        type="button"
        key={entry.path}
        className={`recall-tree-row ${active ? 'active' : ''}`}
        style={treeIndent(depth)}
        role="treeitem"
        aria-level={depth + 1}
        aria-selected={active}
        aria-current={active ? 'page' : undefined}
        title={entry.path}
        data-recall-path={entry.path}
        onClick={() => actions.openRecall(entry.path)}
      >
        <span className="recall-tree-spacer" />
        <FileText size={15} />
        <span className="recall-tree-label"><strong>{nameOf(entry.path)}</strong></span>
      </button>;
    });
  }

  function renderFolder(folder: RecallTreeNode, depth: number) {
    const collapsed = collapsedFolders.has(folder.path);
    const ToggleIcon = collapsed ? ChevronRight : ChevronDown;
    const FolderIcon = collapsed ? Folder : FolderOpen;

    return <div className="recall-tree-folder" key={folder.path} style={treeIndent(depth)}>
      <button
        type="button"
        className="recall-tree-row recall-tree-folder-row"
        role="treeitem"
        aria-level={depth + 1}
        aria-expanded={!collapsed}
        aria-label={`${folder.name} 文件夹，${folder.fileCount} 个文件`}
        title={folder.path}
        onClick={() => toggleFolder(folder.path)}
      >
        <ToggleIcon size={14} />
        <FolderIcon size={16} />
        <span className="recall-tree-label"><strong>{folder.name}</strong></span>
        <em>{folder.fileCount}</em>
      </button>
      {!collapsed && <div className="recall-tree-children" role="group">
        {folder.folders.map((child) => renderFolder(child, depth + 1))}
        {renderFiles(folder.files, depth + 1)}
      </div>}
    </div>;
  }

  function renderSearchResults() {
    return <ul className="recall-search-results" aria-label={`“${state.appliedQuery}”的搜索结果`}>
      {fileEntries.map((entry) => {
        const active = state.current?.path === entry.path;
        return <li key={entry.path}>
          <button
            type="button"
            className={active ? 'active' : ''}
            aria-current={active ? 'page' : undefined}
            title={entry.path}
            onClick={() => actions.openRecall(entry.path)}
          >
            <span className="recall-search-result-icon"><FileText size={16} /></span>
            <span><strong>{nameOf(entry.path)}</strong><small>{parentPath(entry.path)}</small></span>
            <ChevronRight size={16} />
          </button>
        </li>;
      })}
    </ul>;
  }

  const canClearSearch = Boolean(state.query || state.appliedQuery);
  const initializingLibrary = state.loading && state.libraryEntries.length === 0;

  return <aside className="recall-browser" aria-busy={state.loading}>
    <div className="recall-panel-head recall-browser-head">
      <div><h2>召回内容</h2><p aria-live="polite">{resultSummary}</p></div>
      <button type="button" className="recall-new" onClick={actions.startNew} title="新建召回条目"><Plus size={16} /><span>新建</span></button>
    </div>
    <div className="recall-browser-tools">
      <form
        className="recall-search"
        role="search"
        aria-label="搜索召回内容"
        onSubmit={(event) => {
          if (initializingLibrary) {
            event.preventDefault();
            return;
          }
          actions.searchMemories(event);
        }}
      >
        <Search size={15} />
        <input
          aria-label="搜索召回内容"
          value={state.query}
          onChange={(event) => actions.setQuery(event.target.value)}
          onKeyDown={(event) => {
            if (event.key !== 'Escape' || !canClearSearch) return;
            event.preventDefault();
            actions.clearSearch();
          }}
          placeholder="搜索召回内容"
        />
        <span className="recall-search-actions">
          {canClearSearch && <button type="button" className="recall-search-clear" aria-label="清除搜索" title="清除搜索" disabled={initializingLibrary} onClick={actions.clearSearch}><X size={14} /></button>}
          <button type="submit" disabled={initializingLibrary} aria-label={state.loading ? '重新执行搜索' : '执行搜索'}>{state.loading ? '搜索中' : '搜索'}</button>
        </span>
      </form>
      <div className="recall-tree-toolbar">
        <span>{searchActive ? <><Search size={14} />搜索结果</> : <><FolderOpen size={14} />目录</>}</span>
        {searchActive
          ? <small role="status">{fileEntries.length} 个结果</small>
          : <button type="button" onClick={toggleAllFolders} disabled={folderPaths.length === 0}>{allCollapsed ? '展开全部' : '收起全部'}</button>}
      </div>
    </div>
    <div className="recall-files">
      {state.loading ? <p className="recall-empty">正在读取召回内容…</p>
        : fileEntries.length === 0 ? <div className="recall-search-empty">
          <Search size={22} />
          <strong>{searchActive ? '没有匹配的召回内容' : '召回库还是空的'}</strong>
          <span>{searchActive ? `没有找到与“${state.appliedQuery}”匹配的文件。` : '创建第一条召回内容，之后可以在这里浏览。'}</span>
          {searchActive && <button type="button" onClick={actions.clearSearch}>查看全部文件</button>}
        </div>
        : searchActive ? renderSearchResults()
          : <div ref={treeRef} className="recall-tree" role="tree" aria-label="召回库文件树" onKeyDown={handleTreeKeyDown}><div className="recall-tree-root">
            {root.folders.map((folder) => renderFolder(folder, 0))}
            {renderFiles(root.files, 0)}
          </div></div>}
    </div>
  </aside>;
}
