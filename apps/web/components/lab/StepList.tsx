import type { StepContent, StepStatus } from '@/lib/types';

// 단계 진행 상태별 표시 점 색상.
const STATUS_DOT: Record<StepStatus, string> = {
  pending: 'bg-white/25',
  active: 'bg-white',
  validating: 'bg-amber-400 animate-pulse',
  passed: 'bg-emerald-400',
  failed: 'bg-red-400',
};

export function StepList({
  steps,
  statusOf,
  currentId,
  onSelect,
  isSelectable = () => true,
}: {
  steps: StepContent[];
  statusOf: (id: number) => StepStatus;
  currentId?: number;
  onSelect: (id: number) => void;
  // 호출자가 진행 상태를 기준으로 미래 단계 접근 가능 여부를 판단한다.
  // 기본값은 기존 사용처 호환용이며, LabSession에서는 순차 진행 정책을 명시적으로 주입한다.
  isSelectable?: (id: number) => boolean;
}) {
  return (
    <ol className="space-y-1">
      {steps.map((s) => {
        const status = statusOf(s.id);
        const active = s.id === currentId;
        const selectable = isSelectable(s.id);
        return (
          <li key={s.id}>
            <button
              type="button"
              onClick={() => {
                // disabled 버튼은 브라우저가 클릭을 막지만, 정책이 바뀌어도 onSelect가 우회 호출되지 않게 한 번 더 방어한다.
                if (selectable) onSelect(s.id);
              }}
              disabled={!selectable}
              title={selectable ? undefined : '이전 단계를 먼저 통과해야 열 수 있습니다.'}
              className={`flex w-full items-center gap-3 border px-3 py-2.5 text-left transition-colors ${
                active
                  ? 'border-white/50 bg-white/[0.06]'
                  : selectable
                    ? 'border-transparent hover:bg-white/[0.04]'
                    : 'cursor-not-allowed border-transparent'
              }`}
            >
              <span className={`w-2.5 h-2.5 rounded-full flex-shrink-0 ${STATUS_DOT[status]}`} />
              <span className="font-jbmono text-[11px] text-white/55">STEP {s.id}</span>
              <span
                className={`text-sm ${
                  status === 'passed'
                    ? 'text-white/60 line-through'
                    : selectable
                      ? 'text-white'
                      : 'text-white/55'
                }`}
              >
                {s.title}
              </span>
            </button>
          </li>
        );
      })}
    </ol>
  );
}
