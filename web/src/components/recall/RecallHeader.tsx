import { RefreshCw } from 'lucide-react';
import type { RecallWorkspaceViewModel } from './types';

type Props = Pick<RecallWorkspaceViewModel, 'state' | 'changedCount' | 'dirty' | 'actions'>;

export default function RecallHeader({ state, changedCount, dirty, actions }: Props) {
  return <header className="recall-header">
    <div>
      <span className="recall-kicker">NEXUS RECALL</span>
      <h1>召回库</h1>
    </div>
    <div className="recall-header-actions">
      <span className={`recall-health ${dirty ? 'warn' : 'ok'}`}>{dirty ? `${changedCount} 项未记录` : '版本已记录'}</span>
      <button type="button" aria-label="刷新召回库" title="刷新召回库" aria-busy={state.loading} onClick={actions.refreshAll} disabled={state.loading || state.busy}><RefreshCw size={15} /><span>刷新</span></button>
    </div>
  </header>;
}
