import type { StepContent, StepStatus } from '@/lib/types';

// 단계 진행 상태별 표시 점 색상.
const STATUS_DOT: Record<StepStatus, string> = {
  pending: 'bg-white/25',
  active: 'bg-white',
  validating: 'bg-amber-400 animate-pulse',
  passed: 'bg-emerald-400',
  failed: 'bg-red-400',
};

const STATUS_LABEL: Record<StepStatus, string> = {
  pending: '대기',
  active: '진행 중',
  validating: '검증 중',
  passed: '완료',
  failed: '확인 필요',
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
    <ol className="grid grid-flow-col auto-cols-fr border-y border-white/15" aria-label="실습 단계">
      {steps.map((s) => {
        const status = statusOf(s.id);
        const active = s.id === currentId;
        const selectable = isSelectable(s.id);
        const stepNumber = String(s.id).padStart(2, '0');
        const accessibleLabel = `${stepNumber}. ${s.title}, ${STATUS_LABEL[status]}`;
        return (
          <li key={s.id} className="border-r border-white/10 last:border-r-0">
            <button
              type="button"
              onClick={() => {
                // disabled 버튼은 브라우저가 클릭을 막지만, 정책이 바뀌어도 onSelect가 우회 호출되지 않게 한 번 더 방어한다.
                if (selectable) onSelect(s.id);
              }}
              disabled={!selectable}
              title={
                selectable
                  ? `${stepNumber}. ${s.title} · ${STATUS_LABEL[status]}`
                  : `${s.title} · 이전 단계를 먼저 통과해야 열 수 있습니다.`
              }
              aria-label={accessibleLabel}
              aria-current={active ? 'step' : undefined}
              className={`flex min-h-14 w-full flex-col items-center justify-center gap-1.5 px-1 py-2 transition-colors ${
                active
                  ? 'bg-white text-black'
                  : selectable
                    ? 'hover:bg-white/[0.05]'
                    : 'cursor-not-allowed'
              }`}
            >
              <span
                aria-hidden="true"
                className={`font-jbmono text-[11px] tracking-[0.08em] ${
                  active
                    ? 'font-semibold text-black'
                    : status === 'passed'
                      ? 'text-white/75'
                      : selectable
                        ? 'text-white/55'
                        : 'text-white/30'
                }`}
              >
                {stepNumber}
              </span>
              <span
                aria-hidden="true"
                className={`h-1.5 w-1.5 rounded-full ${active ? 'bg-black' : STATUS_DOT[status]}`}
              />
            </button>
          </li>
        );
      })}
    </ol>
  );
}
