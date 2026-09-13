import { formatTime } from '../../lib/time';
import type { RecallWorkspaceViewModel } from './types';

type Props = Pick<RecallWorkspaceViewModel, 'state' | 'changedCount' | 'dirty' | 'actions'>;

export default function RecallVersionHistoryPage({ state, changedCount, dirty, actions }: Props) {
  const gitEnabled = Boolean(state.gitDiff?.git_repo);
  return <section className="recall-version-page">
    <article className="recall-tool-panel recall-version-panel">
      <div className="recall-panel-head"><div><h2>本地版本</h2><p>Recall 仅记录本地 Git 版本，数据保护由宿主机负责。</p></div></div>
      <div className="recall-version-state">
        <div><span>版本库</span><strong>{gitEnabled ? '已启用' : '未启用'}</strong></div>
        <div><span>本地变更</span><strong>{dirty ? `${changedCount} 项未记录` : '无'}</strong></div>
        <div><span>最近版本</span><strong>{state.commits[0]?.short_hash || '—'}</strong></div>
      </div>
      {dirty && <div className="recall-version-actions">
        <button type="button" className="primary" onClick={actions.recordVersion} disabled={state.busy || !gitEnabled}>记录当前版本</button>
      </div>}
    </article>

    <article className="recall-tool-panel recall-history-panel">
      <div className="recall-panel-head"><div><h2>版本历史</h2><p>展示最近的本地版本记录。</p></div></div>
      <div className="recall-commits">
        {state.commits.length === 0 ? <p className="recall-empty">暂无版本记录。</p> : state.commits.map((commit) => (
          <div key={commit.hash}>
            <span />
            <div><strong>{commit.subject || '(无说明)'}</strong><small>{commit.short_hash} · {commit.author} · {formatTime(commit.date)}</small></div>
          </div>
        ))}
      </div>
    </article>
  </section>;
}
