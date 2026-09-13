import { Archive, Clock3, Folder, GitBranch } from 'lucide-react';
import type { RecallWorkspaceViewModel } from './types';

type Props = Pick<RecallWorkspaceViewModel, 'libraryFileCount' | 'directoryCount' | 'changedCount' | 'state'>;

export default function RecallStats({ libraryFileCount, directoryCount, changedCount, state }: Props) {
  return <section className="recall-stats">
    <div><Archive size={18} /><span>文件</span><strong>{libraryFileCount}</strong></div>
    <div><Folder size={18} /><span>目录</span><strong>{directoryCount}</strong></div>
    <div><GitBranch size={18} /><span>本地变更</span><strong>{changedCount}</strong></div>
    <div><Clock3 size={18} /><span>最近版本</span><strong>{state.commits.length}</strong></div>
  </section>;
}
