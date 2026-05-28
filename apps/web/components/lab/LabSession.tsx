'use client';

import { useEffect, useRef, useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { Lab, Session, StepProgress, StepStatus } from '@/lib/types';
import { StepList } from './StepList';
import { TerminalPlaceholder } from './TerminalPlaceholder';
import { LabTerminal } from './LabTerminal';

// VM이 Running으로 보고된 이후에도 cloud-init final stage(getty 재시작 + autologin 활성)
// 까지 약 30–60초가 더 필요하다. 그 사이 학생에게 login 프롬프트가 보이지 않도록
// status=ready 시점부터 BOOT_GRACE_MS 동안 로딩 카드를 유지한다.
const BOOT_GRACE_MS = 60_000;

// 세션 진행 화면: 좌측 단계 목록 + 우측 현재 단계 지시문/터미널/검증.
// VM 부팅·자동 로그인 활성화가 끝나기 전까지는 SessionBoot 로딩 카드만 노출.
export function LabSession({ sessionId, lab }: { sessionId: string; lab: Lab }) {
  const qc = useQueryClient();
  const steps = lab.steps ?? [];

  // ── 훅(hook)은 조건부 분기 앞에 한 번씩만 호출한다 (react-hooks/rules-of-hooks).
  // 세션 상태 폴링 — provisioning 동안 2초 간격, ready/failed로 전환되면 멈춘다.
  const { data: session } = useQuery<Session>({
    queryKey: ['session', sessionId],
    queryFn: () => api.sessions.get(sessionId),
    refetchInterval: (q) => (q.state.data?.status === 'provisioning' ? 2000 : false),
  });

  // status=ready/active로 처음 전환된 시점을 기록 → 거기서 BOOT_GRACE_MS 추가 대기.
  const readyAtRef = useRef<number | null>(null);
  const [, forceTick] = useState(0);
  useEffect(() => {
    const s = session?.status;
    if ((s === 'ready' || s === 'active') && readyAtRef.current === null) {
      readyAtRef.current = Date.now();
      const t = setTimeout(() => forceTick((n) => n + 1), BOOT_GRACE_MS);
      return () => clearTimeout(t);
    }
  }, [session?.status]);

  // 스텝 진행 — boot 단계에선 실제로 사용하지 않지만 hook 순서 일관성을 위해 항상 호출.
  const { data: progressData } = useQuery({
    queryKey: ['session', sessionId, 'steps'],
    queryFn: () => api.sessions.steps(sessionId),
  });

  const [selectedId, setSelectedId] = useState<number | null>(null);

  const validate = useMutation({
    mutationFn: (stepId: number) => api.sessions.validate(sessionId, stepId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['session', sessionId, 'steps'] });
      setSelectedId(null);
    },
  });

  // ── 분기 계산 ──────────────────────────────────────────────────────────
  const status = session?.status;
  const inGrace =
    readyAtRef.current !== null && Date.now() - readyAtRef.current < BOOT_GRACE_MS;
  const booting = !status || status === 'provisioning' || inGrace;
  const wantsLiveTerminal = !!session?.terminal_url;

  // 라이브 터미널 랩은 부팅 동안 학생에게 로그인 프롬프트가 노출되지 않도록 SessionBoot로 가린다.
  if (booting && wantsLiveTerminal) {
    return (
      <SessionBoot
        status={status}
        graceStartedAt={readyAtRef.current}
        graceMs={BOOT_GRACE_MS}
      />
    );
  }

  // ── 메인 UI ────────────────────────────────────────────────────────────
  const progress: StepProgress[] = progressData?.items ?? [];
  const statusOf = (id: number): StepStatus =>
    progress.find((p) => p.step_id === id)?.status ?? 'pending';
  const activeStepId = progress.find((p) => p.status === 'active')?.step_id ?? steps[0]?.id;
  const currentId = selectedId ?? activeStepId;
  const currentStep = steps.find((s) => s.id === currentId) ?? steps[0];

  if (!currentStep) {
    return <p className="text-slate-500 text-sm mt-6">표시할 단계가 없습니다.</p>;
  }

  const allPassed = steps.length > 0 && steps.every((s) => statusOf(s.id) === 'passed');
  const currentStatus = statusOf(currentStep.id);
  const terminalUrl = session?.terminal_url ?? null;

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
              disabled={validate.isPending || currentStatus === 'passed'}
              className="bg-brand-500 hover:bg-brand-600 disabled:opacity-50 disabled:hover:bg-brand-500 text-white text-sm font-medium px-5 py-2 rounded-lg transition-colors"
            >
              {currentStatus === 'passed' ? '완료됨' : validate.isPending ? '검증 중...' : '검증'}
            </button>
            {currentStatus === 'passed' && <span className="text-emerald-400 text-xs">통과</span>}
            {validate.isError && (
              <span className="text-red-400 text-xs">검증 요청에 실패했습니다.</span>
            )}
          </div>
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

// SessionBoot은 학생에게 부팅·자동 로그인 진행 상황을 단계 표시 + 스피너로 보여준다.
// VM 콘솔의 login 프롬프트가 노출되는 것을 차단하기 위해 LabTerminal은 마운트하지 않는다.
function SessionBoot({
  status,
  graceStartedAt,
  graceMs,
}: {
  status: string | undefined;
  graceStartedAt: number | null;
  graceMs: number;
}) {
  // 진행률 표시(0~100%). provisioning 단계는 첫 30%까지만 채워 시각적 진행 인상을 준다.
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 500);
    return () => clearInterval(t);
  }, []);

  let progress = 0;
  if (graceStartedAt === null) {
    progress = status === 'provisioning' ? 15 : 0;
  } else {
    const elapsed = now - graceStartedAt;
    progress = 30 + Math.min(70, (elapsed / graceMs) * 70);
  }

  const stage1Done = status !== undefined;
  const stage2Done = graceStartedAt !== null;

  return (
    <div className="mt-6 bg-slate-800/50 border border-slate-700 rounded-xl p-8">
      <div className="flex items-center gap-4 mb-6">
        <Spinner />
        <div>
          <p className="text-white font-semibold">실습 환경을 준비하고 있습니다</p>
          <p className="text-slate-400 text-sm mt-1">
            보통 1분 이내에 완료됩니다. 잠시만 기다려주세요.
          </p>
        </div>
      </div>

      <div className="h-1.5 w-full bg-slate-700 rounded-full overflow-hidden mb-6">
        <div
          className="h-full bg-brand-500 transition-[width] duration-500 ease-out"
          style={{ width: `${Math.round(progress)}%` }}
        />
      </div>

      <ul className="space-y-2 text-sm">
        <BootStage done={stage1Done} label="세션 생성" />
        <BootStage
          done={stage2Done}
          inProgress={!stage2Done && stage1Done}
          label="VM 프로비저닝"
        />
        <BootStage done={false} inProgress={stage2Done} label="자동 로그인 활성화" />
      </ul>
    </div>
  );
}

function BootStage({
  done,
  inProgress,
  label,
}: {
  done: boolean;
  inProgress?: boolean;
  label: string;
}) {
  return (
    <li className="flex items-center gap-3">
      <span
        className={`w-2.5 h-2.5 rounded-full flex-shrink-0 ${
          done ? 'bg-emerald-400' : inProgress ? 'bg-brand-400 animate-pulse' : 'bg-slate-600'
        }`}
      />
      <span className={`${done ? 'text-slate-400' : inProgress ? 'text-white' : 'text-slate-500'}`}>
        {label}
      </span>
    </li>
  );
}

function Spinner() {
  return (
    <div className="w-10 h-10 rounded-full border-2 border-slate-700 border-t-brand-400 animate-spin flex-shrink-0" />
  );
}
