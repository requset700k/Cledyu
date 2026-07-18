'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { Lab, Session, StepProgress, StepStatus } from '@/lib/types';
import { StepList } from './StepList';
import { TerminalPlaceholder } from './TerminalPlaceholder';
import { LabTerminal, TerminalReadinessProbe } from './LabTerminal';
import { LabWorkspace } from './LabWorkspace';
import { AiTutorPanel } from './AiTutorPanel';
import { SessionTimer } from './SessionTimer';
import {
  bootGraceViewState,
  bootStageViewStates,
  shouldShowSessionBoot,
} from '@/lib/lab-session-boot.mjs';
import { isStepSelectable } from '@/lib/lab-step-access.mjs';
import { appendTerminalTail } from '@/lib/terminal-tail.mjs';

// VM이 Running으로 보고된 이후에도 cloud-init final stage(랩 init + getty 재시작 + autologin 활성)
// 까지 최대 1~2분이 더 필요할 수 있다. 그 사이 학생에게 login 프롬프트가 보이지 않도록
// status=ready 시점부터 BOOT_GRACE_MS 동안 로딩 카드를 유지한다.
const BOOT_GRACE_MS = 120_000;

// 세션 진행 화면: 좌측 단계 목록 + 우측 현재 단계 지시문/터미널/검증.
// VM 부팅·자동 로그인 활성화가 끝나기 전까지는 SessionBoot 로딩 카드만 노출.
export function LabSession({
  sessionId,
  lab,
  skipBootGrace = false,
}: {
  sessionId: string;
  lab: Lab;
  skipBootGrace?: boolean;
}) {
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
  const terminalTailRef = useRef('');
  const [bootGraceComplete, setBootGraceComplete] = useState(false);
  const [, forceTick] = useState(0);
  useEffect(() => {
    terminalTailRef.current = '';
  }, [sessionId]);
  const appendTerminalOutput = useCallback((chunk: string) => {
    terminalTailRef.current = appendTerminalTail(terminalTailRef.current, chunk);
  }, []);
  const getTerminalTail = useCallback(() => terminalTailRef.current, []);
  const completeBootGrace = useCallback(() => setBootGraceComplete(true), []);
  useEffect(() => {
    const s = session?.status;
    if (skipBootGrace) return;
    if ((s === 'ready' || s === 'active') && readyAtRef.current === null) {
      readyAtRef.current = Date.now();
      // ref 변경만으로는 렌더되지 않으므로 grace 진행 상태를 즉시 반영한다.
      forceTick((n) => n + 1);
      // EC2(tailnet SSH) 세션은 KubeVirt 전용 TerminalReadinessProbe 가 안 돌아 grace 종료를
      // 앞당길 주체가 없다. grace 경과 후 재렌더가 없으면 booting 이 영구히 true 로 남아 SessionBoot
      // 화면에서 멈추고 LabTerminal 이 마운트되지 않는다. provider 불문 grace 만료 시 확실히 종료한다.
      const graceTimer = setTimeout(completeBootGrace, BOOT_GRACE_MS);
      return () => clearTimeout(graceTimer);
    }
  }, [session?.status, skipBootGrace, completeBootGrace]);

  // 스텝 진행 — boot 단계에선 실제로 사용하지 않지만 hook 순서 일관성을 위해 항상 호출.
  // 검증 중(validating)인 단계가 있으면 검증엔진 결과가 올 때까지 2초 간격으로 폴링한다.
  const { data: progressData } = useQuery({
    queryKey: ['session', sessionId, 'steps'],
    queryFn: () => api.sessions.steps(sessionId),
    refetchInterval: (q) =>
      q.state.data?.items?.some((p) => p.status === 'validating') ? 2000 : false,
  });

  const [selectedId, setSelectedId] = useState<number | null>(null);
  // 클라이언트 카운트다운이 0에 도달하면 true — 서버 reaper 가 VM 을 회수하므로 만료 UI 로 전환.
  const [expired, setExpired] = useState(false);

  const validate = useMutation({
    mutationFn: (stepId: number) => api.sessions.validate(sessionId, stepId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['session', sessionId, 'steps'] });
      qc.invalidateQueries({ queryKey: ['my-lab-statuses'] });
      qc.invalidateQueries({ queryKey: ['my-dashboard'] });
      setSelectedId(null);
    },
  });

  // ── 분기 계산 ──────────────────────────────────────────────────────────
  const status = session?.status;
  const booting = shouldShowSessionBoot(
    status,
    readyAtRef.current,
    Date.now(),
    BOOT_GRACE_MS,
    skipBootGrace || bootGraceComplete,
  );
  const wantsLiveTerminal = lab.environment === 'ubuntu';

  // 프로비저닝 실패 — booting grace 보다 먼저 확인해, 실패 상태가 부팅 카드에 가려지지 않게 한다.
  if (status === 'failed') {
    return <SessionFailed labId={lab.id} />;
  }

  // 라이브 터미널 랩은 부팅 동안 학생에게 로그인 프롬프트가 노출되지 않도록 SessionBoot로 가린다.
  if (booting && wantsLiveTerminal) {
    return (
      <>
        {session?.terminal_url && (
          <TerminalReadinessProbe terminalPath={session.terminal_url} onReady={completeBootGrace} />
        )}
        <SessionBoot
          status={status}
          provisioningStage={session?.provisioning_stage}
          vmProvider={session?.vm_provider}
          graceStartedAt={readyAtRef.current}
          graceMs={BOOT_GRACE_MS}
          onGraceComplete={completeBootGrace}
        />
      </>
    );
  }

  // 세션 TTL 만료 — 서버가 VM 을 회수하므로 터미널을 가리고 재시작을 안내한다.
  if (expired) {
    return <SessionExpired labId={lab.id} />;
  }

  // ── 메인 UI ────────────────────────────────────────────────────────────
  const progress: StepProgress[] = progressData?.items ?? [];
  const statusOf = (id: number): StepStatus =>
    progress.find((p) => p.step_id === id)?.status ?? 'pending';
  // 진행 중인 단계에 초점: 검증 중 → 활성 → 실패(결과 확인용) → 첫 단계 순.
  const activeStepId =
    progress.find((p) => p.status === 'validating')?.step_id ??
    progress.find((p) => p.status === 'active')?.step_id ??
    progress.find((p) => p.status === 'failed')?.step_id ??
    steps[0]?.id;
  // URL/클라이언트 상태에 이전 선택값이 남아도, 아직 열 수 없는 미래 단계면 현재 진행 단계로 되돌린다.
  // 실제 통과 여부는 서버 StepProgress가 진실 원천이고, Web은 학습자 화면에서 선행 단계 흐름을 보조한다.
  const selectedStepAllowed = selectedId !== null && isStepSelectable(steps, selectedId, statusOf);
  const currentId = selectedStepAllowed ? selectedId : activeStepId;
  const currentStep = steps.find((s) => s.id === currentId) ?? steps[0];

  if (!currentStep) {
    return <p className="mt-6 text-sm text-white/60">표시할 단계가 없습니다.</p>;
  }

  const allPassed = steps.length > 0 && steps.every((s) => statusOf(s.id) === 'passed');
  const currentStatus = statusOf(currentStep.id);
  const [taskDescription, completionCriteria] = currentStep.description.split('\n\n완료 조건\n', 2);
  // 검증 진행 중: 요청이 날아가는 중이거나(isPending) 결과 대기 중(validating).
  const validating = currentStatus === 'validating' || validate.isPending;
  // 실패한 체크의 상세(사유)는 결과 수신 시 진행 상태에 함께 담겨 온다.
  const failedChecks = (progress.find((p) => p.step_id === currentStep.id)?.checks ?? []).filter(
    (ck) => !ck.passed,
  );
  const terminalUrl = session?.terminal_url ?? null;

  // KodeKloud 스타일 2분할 — 좌측: 문제(스텝 목록·지시문·검증·AI 도우미), 우측: 터미널/IDE.
  // 우측은 sticky 로 고정해 긴 지시문을 스크롤해도 터미널이 화면에 남는다.
  return (
    <>
      {/* 세션 만료 카운트다운 — 우측 정렬 헤더 바. */}
      {session?.expires_at && (
        <div className="flex justify-end mt-4">
          <SessionTimer expiresAt={session.expires_at} onExpire={() => setExpired(true)} />
        </div>
      )}
      <div className="mt-4 grid grid-cols-1 items-start gap-6 xl:grid-cols-[minmax(380px,420px)_1fr]">
        <div className="space-y-4 xl:max-h-[calc(100vh-9rem)] xl:overflow-y-auto xl:pr-1">
          {allPassed && (
            <div className="border border-white/30 bg-white/[0.05] px-4 py-3 text-sm text-white">
              🎉 모든 단계를 완료했습니다. 수고하셨습니다!
            </div>
          )}

          <section aria-labelledby="lab-progress-title">
            <div className="mb-3 flex items-end justify-between gap-3 px-1">
              <div>
                <p className="font-jbmono text-[10px] tracking-[0.14em] text-white/50">진행 단계</p>
                <h2
                  id="lab-progress-title"
                  className="mt-1 font-chakra text-lg font-semibold text-white"
                >
                  하나씩 직접 해결하세요
                </h2>
              </div>
              <span className="font-jbmono text-xs text-white/55">
                {steps.filter((step) => statusOf(step.id) === 'passed').length} / {steps.length}
              </span>
            </div>
            <StepList
              steps={steps}
              statusOf={statusOf}
              currentId={currentStep.id}
              onSelect={setSelectedId}
              isSelectable={(id) => isStepSelectable(steps, id, statusOf)}
            />
          </section>

          <section
            className="border border-white/25 bg-white/[0.025]"
            aria-labelledby="current-step-title"
          >
            <header className="border-b border-white/15 px-6 py-5">
              <div className="flex items-center justify-between gap-3 font-jbmono text-[10px] tracking-[0.12em] text-white/50">
                <span>현재 작업</span>
                <span>
                  {String(currentStep.id).padStart(2, '0')} /{' '}
                  {String(steps.length).padStart(2, '0')}
                </span>
              </div>
              <h3
                id="current-step-title"
                className="mt-2 font-chakra text-xl font-semibold tracking-[-0.025em] text-white"
              >
                {currentStep.title}
              </h3>
            </header>
            <div className="p-6">
              <p className="whitespace-pre-line text-[15px] leading-[1.75] text-white/75">
                {taskDescription}
              </p>

              {completionCriteria && (
                <div className="mt-5 border-t border-white/15 pt-5">
                  <p className="font-jbmono text-[10px] tracking-[0.12em] text-white/45">
                    완료 조건
                  </p>
                  <p className="mt-3 whitespace-pre-line text-sm leading-[1.75] text-white/65">
                    {completionCriteria.trim()}
                  </p>
                </div>
              )}

              {currentStatus === 'failed' && failedChecks.length > 0 && (
                <div className="mt-5 border border-red-500/30 bg-red-500/10 px-4 py-3">
                  <p className="mb-1.5 text-xs font-medium text-red-300">
                    아직 통과하지 못한 항목입니다
                  </p>
                  <ul className="space-y-1">
                    {failedChecks.map((ck, i) => (
                      <li key={i} className="text-xs text-red-200/90">
                        <span className="font-jbmono text-red-300">{ck.type}</span>
                        {ck.detail ? `: ${ck.detail}` : ''}
                      </li>
                    ))}
                  </ul>
                </div>
              )}
            </div>

            <footer className="flex flex-wrap items-center justify-between gap-4 border-t border-white/15 px-6 py-5">
              <div>
                <p className="text-sm font-medium text-white/85">터미널 작업을 마쳤나요?</p>
                <p className="mt-0.5 text-xs text-white/50">
                  현재 환경을 확인해 다음 단계로 안내합니다.
                </p>
              </div>
              <button
                type="button"
                onClick={() => validate.mutate(currentStep.id)}
                disabled={validating || currentStatus === 'passed'}
                className="rounded-full bg-white px-6 py-2.5 text-sm font-bold text-black transition-colors hover:bg-white/85 disabled:cursor-not-allowed disabled:opacity-40 disabled:hover:bg-white"
              >
                {currentStatus === 'passed'
                  ? '이 단계 완료'
                  : validating
                    ? '환경 확인 중...'
                    : '이 단계 검증하기 →'}
              </button>
              <div className="w-full text-xs sm:w-auto">
                {currentStatus === 'passed' && <span className="text-emerald-400">검증 완료</span>}
                {validating && (
                  <span className="text-amber-400">검증 결과를 기다리고 있습니다.</span>
                )}
                {currentStatus === 'failed' && failedChecks.length === 0 && (
                  <span className="text-red-400">조건을 다시 확인한 뒤 재시도해 주세요.</span>
                )}
                {validate.isError && (
                  <span className="text-red-400">검증을 요청하지 못했습니다.</span>
                )}
              </div>
            </footer>
          </section>

          {/* AI 학습 도우미 — 정적 hint 표시를 대체. key=stepId 로 스텝 전환 시 힌트 초기화. */}
          <AiTutorPanel
            key={currentStep.id}
            sessionId={sessionId}
            stepId={currentStep.id}
            getTerminalTail={getTerminalTail}
          />
        </div>

        {/* 우측 — 작업 영역(터미널/IDE). xl 미만에서는 문제 아래로 쌓인다. */}
        <div className="xl:sticky xl:top-24">
          {terminalUrl ? (
            session?.ide_url ? (
              <LabWorkspace
                sessionId={sessionId}
                terminalPath={terminalUrl}
                idePath={session.ide_url}
                heightClass="h-[520px]"
                onTerminalOutput={appendTerminalOutput}
                redrawTerminalOnConnect={session?.vm_provider === 'kubevirt'}
              />
            ) : (
              <LabTerminal
                terminalPath={terminalUrl}
                heightClass="h-[520px]"
                onOutput={appendTerminalOutput}
                redrawOnConnect={session?.vm_provider === 'kubevirt'}
              />
            )
          ) : (
            <TerminalPlaceholder />
          )}
        </div>
      </div>
    </>
  );
}

// SessionFailed는 VM 이 프로비저닝 타임아웃/실패 상태가 됐을 때 노출되는 안내 카드다.
function SessionFailed({ labId }: { labId: string }) {
  return (
    <div className="mt-6 border border-red-500/40 bg-white/[0.02] p-10 text-center">
      <div className="inline-flex items-center justify-center w-14 h-14 rounded-2xl bg-red-500/10 border border-red-500/30 mb-4">
        <svg className="w-7 h-7 text-red-400" fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth={1.6}
            d="M12 9v4m0 4h.01M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0Z"
          />
        </svg>
      </div>
      <h3 className="font-chakra text-lg font-bold text-white">실습 환경 준비에 실패했습니다</h3>
      <p className="mt-3 text-sm leading-relaxed text-white/50">
        VM 프로비저닝이 제한 시간 안에 완료되지 않았습니다. 새 세션을 시작하면 깨끗한 환경으로 다시
        준비합니다.
      </p>
      <a
        href={`/labs/${labId}`}
        className="mt-6 inline-block rounded-full bg-white px-6 py-2.5 text-sm font-bold text-black transition-colors hover:bg-white/85"
      >
        실습 다시 시작하기
      </a>
    </div>
  );
}

// SessionExpired는 TTL 만료(서버가 VM 회수) 시 터미널 대신 노출되는 안내 카드다.
function SessionExpired({ labId }: { labId: string }) {
  return (
    <div className="mt-6 border border-white/20 bg-white/[0.02] p-10 text-center">
      <div className="inline-flex items-center justify-center w-14 h-14 rounded-2xl bg-amber-500/10 border border-amber-500/30 mb-4">
        <svg
          className="w-7 h-7 text-amber-400"
          fill="none"
          viewBox="0 0 24 24"
          stroke="currentColor"
        >
          <circle cx="12" cy="12" r="9" strokeWidth={1.6} />
          <path strokeLinecap="round" strokeWidth={1.6} d="M12 7v5l3 2" />
        </svg>
      </div>
      <h3 className="font-chakra text-lg font-bold text-white">세션이 만료되었습니다</h3>
      <p className="mt-3 text-sm leading-relaxed text-white/50">
        실습 세션의 최대 사용 시간이 지나 환경이 종료되었습니다. 새 세션을 시작하면 처음부터 다시
        실습할 수 있습니다.
      </p>
      <a
        href={`/labs/${labId}`}
        className="mt-6 inline-block rounded-full bg-white px-6 py-2.5 text-sm font-bold text-black transition-colors hover:bg-white/85"
      >
        실습 다시 시작하기
      </a>
    </div>
  );
}

// SessionBoot은 학생에게 부팅·자동 로그인 진행 상황을 단계 표시 + 스피너로 보여준다.
// VM 콘솔의 login 프롬프트가 노출되는 것을 차단하기 위해 LabTerminal은 마운트하지 않는다.
function SessionBoot({
  status,
  provisioningStage,
  vmProvider,
  graceStartedAt,
  graceMs,
  onGraceComplete,
}: {
  status: string | undefined;
  provisioningStage?: string;
  vmProvider?: string;
  graceStartedAt: number | null;
  graceMs: number;
  onGraceComplete: () => void;
}) {
  // 진행률 표시(0~100%). provisioning 단계는 첫 30%까지만 채워 시각적 진행 인상을 준다.
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 500);
    return () => clearInterval(t);
  }, []);

  const graceState = bootGraceViewState(status, graceStartedAt, now, graceMs);
  useEffect(() => {
    if (graceState.complete) onGraceComplete();
  }, [graceState.complete, onGraceComplete]);

  const stages = bootStageViewStates(status, provisioningStage, vmProvider, graceStartedAt);
  const progress = Math.round(graceState.progress);

  return (
    <section
      className="mt-10 overflow-hidden border border-white/20 bg-white/[0.015]"
      aria-labelledby="environment-boot-title"
    >
      <div className="grid gap-8 border-b border-white/15 px-6 py-7 sm:grid-cols-[1fr_auto] sm:px-8 sm:py-8">
        <div>
          <div className="flex items-center gap-2 font-jbmono text-[11px] tracking-[0.14em] text-white/60">
            <span aria-hidden="true" className="h-1.5 w-1.5 bg-white motion-safe:animate-pulse" />
            ENVIRONMENT BOOT
          </div>
          <h2
            id="environment-boot-title"
            className="mt-4 break-keep font-chakra text-2xl font-bold tracking-[-0.025em] text-white sm:text-3xl"
          >
            실습 환경을 준비하고 있습니다
          </h2>
          <p className="mt-2 text-sm leading-relaxed text-white/60">
            전용 VM과 터미널을 연결하고 있습니다. 보통 1~2분 안에 완료됩니다.
          </p>
        </div>

        <div className="self-end sm:min-w-32 sm:text-right">
          <p className="font-jbmono text-[10px] tracking-[0.14em] text-white/60">PROGRESS</p>
          <p className="mt-1 font-michroma text-4xl leading-none tracking-[-0.06em] text-white sm:text-5xl">
            {progress}
            <span className="ml-1 text-base tracking-normal text-white/60">%</span>
          </p>
        </div>
      </div>

      <div
        className="mx-6 mt-6 h-px overflow-hidden bg-white/15 sm:mx-8"
        role="progressbar"
        aria-label="실습 환경 준비 진행률"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={progress}
      >
        <div
          className="h-full bg-white transition-[width] duration-500 ease-out motion-reduce:transition-none"
          style={{ width: `${progress}%` }}
        />
      </div>

      <ol
        className={`mt-7 grid border-t border-white/15 divide-y divide-white/15 sm:divide-x sm:divide-y-0 ${
          stages.length === 4 ? 'sm:grid-cols-4' : 'sm:grid-cols-3'
        }`}
      >
        {stages.map((stage, index) => (
          <BootStage
            key={stage.label}
            index={index + 1}
            done={stage.done}
            inProgress={stage.inProgress}
            label={stage.label}
          />
        ))}
      </ol>
    </section>
  );
}

function BootStage({
  index,
  done,
  inProgress,
  label,
}: {
  index: number;
  done: boolean;
  inProgress?: boolean;
  label: string;
}) {
  const stateLabel = done ? '완료' : inProgress ? '진행 중' : '대기';

  return (
    <li className="px-6 py-5 sm:min-h-32 sm:px-5" aria-current={inProgress ? 'step' : undefined}>
      <div className="flex items-center justify-between gap-3">
        <span
          className={`font-michroma text-xs ${done || inProgress ? 'text-white/70' : 'text-white/55'}`}
        >
          {String(index).padStart(2, '0')}
        </span>
        <span
          className={`font-jbmono text-[10px] tracking-[0.08em] ${
            inProgress ? 'text-white' : done ? 'text-white/60' : 'text-white/55'
          }`}
        >
          {stateLabel}
        </span>
      </div>
      <div className="mt-6 flex items-center gap-2.5">
        <span
          aria-hidden="true"
          className={`h-2 w-2 shrink-0 border ${
            done
              ? 'border-white/70 bg-white/70'
              : inProgress
                ? 'border-white bg-white motion-safe:animate-pulse'
                : 'border-white/20'
          }`}
        />
        <span
          className={`text-sm font-medium ${
            inProgress ? 'text-white' : done ? 'text-white/65' : 'text-white/55'
          }`}
        >
          {label}
        </span>
      </div>
    </li>
  );
}
