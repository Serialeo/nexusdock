import { Check, X } from 'lucide-react';
import { clearRecallDraft } from '../../lib/drafts';
import type { RecallWorkspaceViewModel } from './types';

type Props = Pick<RecallWorkspaceViewModel, 'state' | 'actions'>;

export default function RecallNoticeArea({ state, actions }: Props) {
  return <>
    {state.notice && <div className={`recall-notice ${state.notice.danger ? 'danger' : ''}`} role={state.notice.danger ? 'alert' : 'status'} aria-live={state.notice.danger ? 'assertive' : 'polite'}>{state.notice.danger ? null : <Check size={16} aria-hidden="true" />}<span>{state.notice.text}</span><button type="button" className="recall-notice-close" aria-label="关闭提示" title="关闭提示" onClick={actions.clearNotice}><X size={14} /></button></div>}
    {state.draftAvailable && !state.editing && <div className="recall-notice" role="status" aria-live="polite"><span>检测到未提交草稿。</span><button type="button" className="recall-draft-restore" aria-busy={state.busy} disabled={state.loading || state.busy} onClick={actions.restoreDraft}>{state.busy ? '正在恢复…' : '恢复草稿'}</button><button type="button" className="recall-draft-discard" onClick={() => { clearRecallDraft(); actions.discardDraft(); }}>丢弃</button></div>}
  </>;
}
