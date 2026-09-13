import Dialog from '../Dialog';
import type { RecallWorkspaceViewModel } from './types';

type Props = Pick<RecallWorkspaceViewModel, 'state' | 'actions'>;

export default function RecallActionDialog({ state, actions }: Props) {
  const pending = state.pendingAction;
  if (!pending) return null;
  const pendingError = 'error' in pending ? pending.error : undefined;
  return <Dialog
    title={pending.kind === 'move' ? '移动召回内容' : '删除召回内容'}
    description={pending.kind === 'move' ? '修改路径后会保留文件内容，并刷新当前打开的召回内容。' : '删除后会产生本地 Git 变更，需要同步后才会进入远端。'}
    onClose={() => { if (!state.busy) actions.closePendingAction(); }}
  >
    <div className="recall-dialog-body">
      {pending.kind === 'move' ? (
        <label htmlFor="recall-move-path">
          <span>新的召回路径</span>
          <input id="recall-move-path" name="path" autoComplete="off" spellCheck={false} aria-invalid={Boolean(pendingError)} aria-describedby={pendingError ? 'recall-action-error' : undefined} value={pending.nextPath} onChange={(event) => actions.updatePendingMovePath(event.target.value)} />
        </label>
      ) : (
        <div className="recall-danger-box">
          <strong>确认删除这条召回内容？</strong>
          <code>{pending.path}</code>
        </div>
      )}
      {pendingError && <p id="recall-action-error" className="recall-dialog-error" role="alert">{pendingError}</p>}
      <div className="recall-dialog-actions">
        <button type="button" onClick={actions.closePendingAction} disabled={state.busy}>取消</button>
        {pending.kind === 'move'
          ? <button className="primary" type="button" aria-busy={state.busy} onClick={actions.confirmMove} disabled={state.busy}>{state.busy ? '正在移动…' : '确认移动'}</button>
          : <button className="danger" type="button" aria-busy={state.busy} onClick={actions.confirmDelete} disabled={state.busy}>{state.busy ? '正在删除…' : '确认删除'}</button>}
      </div>
    </div>
  </Dialog>;
}
