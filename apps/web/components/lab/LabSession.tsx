'use client';

import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { Lab, StepProgress, StepStatus } from '@/lib/types';
import { StepList } from './StepList';
import { TerminalPlaceholder } from './TerminalPlaceholder';
import { LabTerminal } from './LabTerminal';

// 세션 진행 화면: 좌측 단계 목록 + 우측 현재 단계 지시문/터미널/검증.
// 세션·단계 상태는 백엔드(현재 stub) api.sessions.* 를 통해 가져온다.
export function LabSession({
  sessionId,
  lab,
  terminalUrl,
}: {
  sessionId: string;
  lab: Lab;
  terminalUrl?: string | null;
}) {
  const qc = useQueryClient();
  const steps = lab.steps ?? [];

  const { data: progressData } = useQuery({
    queryKey: ['session', sessionId, 'steps'],
    queryFn: () => api.sessions.steps(sessionId),
    // 검증 중(validating)인 단계가 있으면 검증엔진 결과가 올 때까지 2초 간격으로 폴링한다.
    refetchInterval: (query) =>
      query.state.data?.items?.some((p) => p.status === 'validating') ? 2000 : false,
  });
  const progress: StepProgress[] = progressData?.items ?? [];

  const statusOf = (id: number): StepStatus =>
    progress.find((p) => p.step_id === id)?.status ?? 'pending';

  // 사용자가 명시적으로 고른 단계가 없으면 진행 중인 단계를 보여준다:
  // 검증 중 → 활성 → 실패(결과 확인용) → 첫 단계 순으로 초점을 맞춘다.
  const activeStepId =
    progress.find((p) => p.status === 'validating')?.step_id ??
    progress.find((p) => p.status === 'active')?.step_id ??
    progress.find((p) => p.status === 'failed')?.step_id ??
    steps[0]?.id;
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const currentId = selectedId ?? activeStepId;
  const currentStep = steps.find((s) => s.id === currentId) ?? steps[0];

  const validate = useMutation({
    mutationFn: (stepId: number) => api.sessions.validate(sessionId, stepId),
    onSuccess: () => {
      // 진행 상태를 다시 불러오고, 선택을 해제해 다음 active 단계로 자동 이동.
      qc.invalidateQueries({ queryKey: ['session', sessionId, 'steps'] });
      setSelectedId(null);
    },
  });

  const allPassed = steps.length > 0 && steps.every((s) => statusOf(s.id) === 'passed');

  if (!currentStep) {
    return <p className="text-slate-500 text-sm mt-6">표시할 단계가 없습니다.</p>;
  }

  const currentStatus = statusOf(currentStep.id);
  // 검증 진행 중: 요청이 날아가는 중이거나(isPending) 결과 대기 중(validating).
  const validating = currentStatus === 'validating' || validate.isPending;
  // 실패한 체크의 상세(사유)는 결과 수신 시 진행 상태에 함께 담겨 온다.
  const failedChecks = (progress.find((p) => p.step_id === currentStep.id)?.checks ?? []).filter(
    (c) => !c.passed,
  );

  return (
    <div className="grid grid-cols-1 lg:grid-cols-[260px_1fr] gap-6 mt-6">
      <div>
        <h2 className="text-slate-400 text-xs font-medium mb-2 px-3">진행 단계</h2>
        <StepList
          steps={steps}
          statusOf={statusOf}
          currentId={currentStep.id}
          onSelect={setSelectedId}
        />
      </div>

      <div className="space-y-4">
        {allPassed && (
          <div className="rounded-xl border border-emerald-500/30 bg-emerald-500/10 px-4 py-3 text-emerald-300 text-sm">
            🎉 모든 단계를 완료했습니다. 수고하셨습니다!
          </div>
        )}

        <div className="bg-slate-800/50 border border-slate-700 rounded-xl p-6">
          <div className="flex items-center gap-2 mb-2">
            <span className="text-brand-400 text-xs font-semibold">STEP {currentStep.id}</span>
            <h3 className="text-white font-semibold">{currentStep.title}</h3>
          </div>
          <p className="text-slate-300 text-sm whitespace-pre-line mb-4">
            {currentStep.description}
          </p>

          {currentStep.commands && currentStep.commands.length > 0 && (
            <div className="mb-4">
              <p className="text-slate-400 text-xs mb-1">이 단계에서 실행할 명령</p>
              <div className="font-mono text-sm text-slate-300 bg-slate-950 border border-slate-700 rounded-md px-3 py-2 space-y-0.5">
                {currentStep.commands.map((cmd, i) => (
                  <div key={i}>
                    <span className="text-emerald-400 select-none">$ </span>
                    {cmd}
                  </div>
                ))}
              </div>
            </div>
          )}

          {currentStep.hint && <p className="text-xs text-slate-500 mb-4">💡 {currentStep.hint}</p>}

          <div className="flex items-center gap-3">
            <button
              type="button"
              onClick={() => validate.mutate(currentStep.id)}
              disabled={validating || currentStatus === 'passed'}
              className="bg-brand-500 hover:bg-brand-600 disabled:opacity-50 disabled:hover:bg-brand-500 text-white text-sm font-medium px-5 py-2 rounded-lg transition-colors"
            >
              {currentStatus === 'passed' ? '완료됨' : validating ? '검증 중...' : '검증'}
            </button>
            {currentStatus === 'passed' && <span className="text-emerald-400 text-xs">통과</span>}
            {validating && <span className="text-amber-400 text-xs">검증엔진 결과 대기 중…</span>}
            {currentStatus === 'failed' && failedChecks.length === 0 && (
              <span className="text-red-400 text-xs">검증에 실패했습니다. 다시 시도하세요.</span>
            )}
            {validate.isError && (
              <span className="text-red-400 text-xs">검증 요청에 실패했습니다.</span>
            )}
          </div>

          {currentStatus === 'failed' && failedChecks.length > 0 && (
            <div className="mt-4 rounded-lg border border-red-500/30 bg-red-500/10 px-4 py-3">
              <p className="text-red-300 text-xs font-medium mb-1.5">검증 실패 — 아래 항목을 확인하세요</p>
              <ul className="space-y-1">
                {failedChecks.map((c, i) => (
                  <li key={i} className="text-red-200/90 text-xs">
                    <span className="font-mono text-red-300">{c.type}</span>
                    {c.detail ? `: ${c.detail}` : ''}
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>

        {terminalUrl ? (
          <LabTerminal terminalPath={terminalUrl} />
        ) : (
          <div>
            <TerminalPlaceholder commands={currentStep.commands ?? []} />
            {lab.environment === 'k3s' && (
              <p className="mt-2 text-xs text-amber-400/80">
                ⚠ k3s 실습 환경은 준비 중입니다 — 현재는 콘텐츠 미리보기만 제공합니다.
              </p>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
