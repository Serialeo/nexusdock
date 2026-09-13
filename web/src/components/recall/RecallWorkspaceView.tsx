import type { RecallPage, RecallWorkspaceViewModel } from './types';
import RecallActionDialog from './RecallActionDialog';
import RecallEditor from './RecallEditor';
import RecallExperienceCardsPage from './RecallExperienceCardsPage';
import RecallEvolutionPage from './RecallEvolutionPage';
import RecallFileBrowser from './RecallFileBrowser';
import RecallHeader from './RecallHeader';
import RecallNoticeArea from './RecallNoticeArea';
import RecallStats from './RecallStats';
import RecallVersionHistoryPage from './RecallVersionHistoryPage';
import RecallVectorRecallPage from './RecallVectorRecallPage';

type Props = RecallWorkspaceViewModel & {
  page: RecallPage;
  refreshToken: number;
  onNavigate: (page: RecallPage) => void;
};

const recallNavigation: Array<{ id: RecallPage; label: string }> = [
  { id: 'library', label: '资料库' },
  { id: 'cards', label: '经验卡片' },
  { id: 'evolution', label: '进化' },
  { id: 'vectors', label: '向量召回' },
  { id: 'history', label: '版本历史' },
];

export default function RecallWorkspaceView(props: Props) {
  function openCardFromTools(path: string) {
    props.onNavigate('library');
    props.actions.openSimilarCard(path);
  }

  return <main className={`recall-workspace ${props.detailOpen ? 'is-detail-open' : ''}`}>
    <RecallHeader {...props} />
    <nav className="recall-subnav" aria-label="Recall 分类">
      {recallNavigation.map((item) => <button type="button" key={item.id} className={props.page === item.id ? 'is-active' : ''} aria-current={props.page === item.id ? 'page' : undefined} onClick={() => props.onNavigate(item.id)}><strong>{item.label}</strong></button>)}
    </nav>
    <RecallNoticeArea {...props} />
    <RecallActionDialog {...props} />

    {props.page === 'library' && <>
      <RecallStats {...props} />
      <section className="recall-grid">
        <RecallFileBrowser {...props} />
        <RecallEditor {...props} />
      </section>
    </>}
    {props.page === 'cards' && <RecallExperienceCardsPage entries={props.state.cardEntries} loading={props.state.loading} />}
    {props.page === 'evolution' && <RecallEvolutionPage refreshToken={props.refreshToken} />}
    {props.page === 'vectors' && <RecallVectorRecallPage state={props.state} actions={props.actions} onOpenCard={openCardFromTools} />}
    {props.page === 'history' && <RecallVersionHistoryPage state={props.state} changedCount={props.changedCount} dirty={props.dirty} actions={props.actions} />}
  </main>;
}
