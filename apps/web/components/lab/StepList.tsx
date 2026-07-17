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
    <ol className="border-y border-white/15">
      {steps.map((s) => {
        const status = statusOf(s.id);
        const active = s.id === currentId;
        const selectable = isSelectable(s.id);
        return (
          <li key={s.id} className="border-b border-white/10 last:border-b-0">
            <button
              type="button"
              onClick={() => {
                // disabled 버튼은 브라우저가 클릭을 막지만, 정책이 바뀌어도 onSelect가 우회 호출되지 않게 한 번 더 방어한다.
                if (selectable) onSelect(s.id);
              }}
              disabled={!selectable}
              title={selectable ? undefined : '이전 단계를 먼저 통과해야 열 수 있습니다.'}
              aria-current={active ? 'step' : undefined}
              className={`group grid min-h-14 w-full grid-cols-[36px_minmax(0,1fr)_auto] items-center gap-3 px-3 py-2.5 text-left transition-colors ${
                active
                  ? 'bg-white text-black'
                  : selectable
                    ? 'hover:bg-white/[0.05]'
                    : 'cursor-not-allowed'
              }`}
            >
              <span
                className={`flex h-7 w-7 items-center justify-center border font-jbmono text-[10px] ${
                  active ? 'border-black/25 text-black/70' : 'border-white/20 text-white/55'
                }`}
              >
                {String(s.id).padStart(2, '0')}
              </span>
              <span
                className={`min-w-0 text-sm ${
                  status === 'passed'
                    ? active
                      ? 'text-black/55 line-through'
                      : 'text-white/60 line-through'
                    : selectable
                      ? active
                        ? 'font-semibold text-black'
                        : 'text-white'
                      : 'text-white/55'
                }`}
              >
                {s.title}
              </span>
              <span
                className={`flex items-center gap-1.5 whitespace-nowrap font-jbmono text-[10px] ${
                  active ? 'text-black/55' : status === 'failed' ? 'text-red-400' : 'text-white/45'
                }`}
              >
                <span
                  aria-hidden="true"
                  className={`h-1.5 w-1.5 rounded-full ${active ? 'bg-black' : STATUS_DOT[status]}`}
                />
                {STATUS_LABEL[status]}
              </span>
            </button>
          </li>
        );
      })}
    </ol>
  );
}
