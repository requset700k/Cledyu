import type { StepContent, StepStatus } from '@/lib/types';

// 단계 진행 상태별 표시 점 색상.
const STATUS_DOT: Record<StepStatus, string> = {
  pending: 'bg-slate-600',
  active: 'bg-brand-400',
  passed: 'bg-emerald-400',
  failed: 'bg-red-400',
};

export function StepList({
  steps,
  statusOf,
  currentId,
  onSelect,
}: {
  steps: StepContent[];
  statusOf: (id: number) => StepStatus;
  currentId?: number;
  onSelect: (id: number) => void;
}) {
  return (
    <ol className="space-y-1">
      {steps.map((s) => {
        const status = statusOf(s.id);
        const active = s.id === currentId;
        return (
          <li key={s.id}>
            <button
              type="button"
              onClick={() => onSelect(s.id)}
              className={`w-full text-left flex items-center gap-3 px-3 py-2.5 rounded-lg border transition-colors ${
                active
                  ? 'border-brand-500/50 bg-slate-800'
                  : 'border-transparent hover:bg-slate-800/50'
              }`}
            >
              <span className={`w-2.5 h-2.5 rounded-full flex-shrink-0 ${STATUS_DOT[status]}`} />
              <span className="text-slate-500 text-xs">STEP {s.id}</span>
              <span className={`text-sm ${status === 'passed' ? 'text-slate-400 line-through' : 'text-white'}`}>
                {s.title}
              </span>
            </button>
          </li>
        );
      })}
    </ol>
  );
}
