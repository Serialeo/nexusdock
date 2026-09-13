import { ArrowLeft } from 'lucide-react';

type MobileDrilldownBarProps = {
  label: string;
  title: string;
  meta?: string;
  backLabel?: string;
  onBack: () => void;
};

export default function MobileDrilldownBar({ label, title, meta, backLabel, onBack }: MobileDrilldownBarProps) {
  return <header className="mobile-drilldown-bar">
    <button type="button" onClick={onBack} aria-label={backLabel || `返回${label}列表`}><ArrowLeft size={18} /></button>
    <div><span>{label}</span><strong>{title}</strong></div>
    {meta && <em>{meta}</em>}
  </header>;
}
