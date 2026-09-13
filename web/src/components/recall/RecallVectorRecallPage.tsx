import { Cpu, Search } from 'lucide-react';
import type { RecallWorkspaceViewModel } from './types';
import { nameOf } from './utils';

type Props = Pick<RecallWorkspaceViewModel, 'state' | 'actions'> & {
  onOpenCard: (path: string) => void;
};

export default function RecallVectorRecallPage({ state, actions, onOpenCard }: Props) {
  const { embedding, busy } = state;
  return <section className="recall-tool-page">
    <article className="recall-tool-panel">
      <div className="recall-panel-head">
        <div><h2>向量召回</h2><p>只搜索经验卡片索引，用于高信噪比的语义召回。</p></div>
        <span className={`recall-health ${embedding.status?.reachable ? 'ok' : 'warn'}`}>{embedding.status?.reachable ? 'BGE-M3 可达' : '未就绪'}</span>
      </div>
      <div className="recall-vector-status">
        <div><span>模型</span><strong>{embedding.status?.index?.model || embedding.status?.model || '未知'}</strong></div>
        <div><span>维度</span><strong>{String(embedding.status?.index?.dimension ?? '—')}</strong></div>
        <div><span>索引</span><strong>{embedding.status?.index?.count === undefined ? '—' : `${embedding.status.index.count} 项`}</strong></div>
      </div>
      <form className="recall-vector-search" onSubmit={actions.searchCardEmbeddings}>
        <Search size={15} />
        <input aria-label="搜索经验卡片" value={embedding.query} onChange={(event) => actions.setEmbeddingQuery(event.target.value)} placeholder="输入自然语言搜索经验卡片" />
        <button type="submit" className="primary" disabled={busy}>向量搜索</button>
        <button type="button" onClick={actions.reindexCards} disabled={busy}><Cpu size={15} />重建索引</button>
      </form>
      <div className="recall-vector-results">
        {embedding.results.length === 0 ? <p className="recall-empty">暂无向量搜索结果。</p> : embedding.results.map((item) => <button key={item.path} type="button" onClick={() => onOpenCard(item.path)}><strong>{item.title || nameOf(item.path)}</strong><small>{item.path}</small><em>{item.score.toFixed(4)}</em></button>)}
      </div>
    </article>
  </section>;
}
