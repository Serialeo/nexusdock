import { FileText, Pencil, Plus, Save, Trash2 } from 'lucide-react';
import type { RecallWorkspaceViewModel } from './types';
import { nameOf } from './utils';

type Props = Pick<RecallWorkspaceViewModel, 'state' | 'hasUnsavedChanges' | 'editorRef' | 'actions'>;

export default function RecallEditor({ state, hasUnsavedChanges, editorRef, actions }: Props) {
  const title = state.editing
    ? state.creating ? '新建召回条目' : '编辑召回条目'
    : state.current ? nameOf(state.current.path) : '选择一条召回内容';
  const subtitle = state.editing ? state.draftPath : state.current?.path || '从左侧文件列表打开，或新建一条召回内容。';
  return <article className="recall-editor" ref={editorRef} aria-labelledby="recall-editor-title" aria-busy={state.busy}>
    <div className="recall-panel-head">
      <div><h2 id="recall-editor-title" title={title}>{title}</h2><p title={subtitle}>{subtitle}</p></div>
      <button className="recall-mobile-back" type="button" onClick={actions.backToFileList}>返回文件</button>
      <div className="recall-editor-actions">
        {!state.editing && state.current && <button type="button" onClick={actions.startEdit}><Pencil size={15} />编辑</button>}
        {!state.editing && state.current && <button type="button" onClick={actions.requestMove}>移动</button>}
        {!state.editing && state.current && <button type="button" className="danger" onClick={actions.requestDelete}><Trash2 size={15} />删除</button>}
        {state.editing && <button type="button" onClick={actions.cancelEdit}>取消</button>}
        {state.editing && <button type="button" className="primary" onClick={actions.saveRecall} disabled={state.busy || !hasUnsavedChanges}><Save size={15} />保存</button>}
      </div>
    </div>
    {state.editing ? (
      <div className="recall-edit-body">
        <label htmlFor="recall-draft-path"><span>路径</span><input id="recall-draft-path" name="path" autoComplete="off" spellCheck={false} value={state.draftPath} onChange={(event) => actions.setDraftPath(event.target.value)} disabled={!state.creating || state.busy} /></label>
        <label className="content" htmlFor="recall-draft-content"><span>内容</span><textarea id="recall-draft-content" name="content" aria-describedby="recall-draft-meta" value={state.draftContent} onChange={(event) => actions.setDraftContent(event.target.value)} disabled={state.busy} spellCheck={false} /></label>
        <small id="recall-draft-meta" aria-live="polite">{state.draftContent.length.toLocaleString()} 字符 · 草稿自动保存在当前浏览器会话</small>
      </div>
    ) : state.current ? (
      <pre className="recall-preview">{state.current.content}</pre>
    ) : (
      <div className="recall-empty large"><FileText size={28} /><strong>没有打开的召回内容</strong><span>选择文件或创建一条新召回条目。</span><button type="button" className="primary" onClick={actions.startNew}><Plus size={15} />新建召回条目</button></div>
    )}
  </article>;
}
