'use client';

import { Suspense, useEffect, useState } from 'react';
import Link from 'next/link';
import { useParams, useRouter, useSearchParams } from 'next/navigation';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api, ApiRequestError } from '@/lib/api';
import {
  buildActiveSessionResumeHref,
  readActiveSessionResumeId,
  resolveActiveSessionResume,
} from '@/lib/active-session-resume.mjs';
import { DIFFICULTY_CONFIG } from '@/components/lab/difficulty';
import { LabSession } from '@/components/lab/LabSession';
import type { Lab } from '@/lib/types';

interface ActiveSessionConflict {
  sessionId: string;
  labId?: string;
}

function LabDetail() {
  const { id } = useParams<{ id: string }>();
  const router = useRouter();
  const searchParams = useSearchParams();
  const queryClient = useQueryClient();
  const requestedResumeId = readActiveSessionResumeId(searchParams);

  // 세션 시작 전 화면에서 관리하는 로컬 상태다. 실제 세션 진행/TTL 처리는 LabSession이 맡는다.
  const [sessionId, setSessionId] = useState<string | null>(null);
  const [resumedExisting, setResumedExisting] = useState(false);
  const [skipBootGrace, setSkipBootGrace] = useState(false);
  const [activeSessionConflict, setActiveSessionConflict] = useState<ActiveSessionConflict | null>(
    null,
  );
  const [existingSessionAction, setExistingSessionAction] = useState<
    'checking' | 'terminating' | null
  >(null);
  const [replaceError, setReplaceError] = useState<string | null>(null);

  const {
    data: lab,
    isLoading,
    isError,
    error,
  } = useQuery({
    queryKey: ['lab', id],
    queryFn: () => api.labs.get(id),
  });

  useEffect(() => {
    if (!requestedResumeId) return;

    let cancelled = false;
    setExistingSessionAction('checking');
    setActiveSessionConflict(null);
    setReplaceError(null);
    setSkipBootGrace(false);

    // URL의 session_id는 힌트일 뿐이다. Session API의 소유권 검사와 lab_id 일치 판정을
    // 모두 통과한 경우에만 기존 실습 화면을 연다.
    void api.sessions
      .get(requestedResumeId)
      .then((existing) => {
        if (cancelled) return;
        const result = resolveActiveSessionResume(id, existing);
        if (result.status === 'resume') {
          setResumedExisting(true);
          setSkipBootGrace(result.skipBootGrace);
          setSessionId(result.sessionId);
          return;
        }
        setReplaceError('진행 중인 세션이 현재 Lab과 일치하지 않습니다.');
      })
      .catch(() => {
        if (!cancelled) {
          setReplaceError('진행 중인 세션을 확인하지 못했습니다. 다시 시도해주세요.');
        }
      })
      .finally(() => {
        if (!cancelled) {
          setExistingSessionAction(null);
          // 처리된 내부 식별자가 주소창과 이후 Lab 이동 상태에 남지 않도록 정리한다.
          router.replace(`/labs/${encodeURIComponent(id)}`, { scroll: false });
        }
      });

    return () => {
      cancelled = true;
    };
  }, [id, requestedResumeId, router]);

  const start = useMutation({
    mutationFn: () => api.sessions.create(id),
    // terminal_url은 LabSession이 자체 polling으로 derive하므로 sessionId만 보관.
    onSuccess: (s) => {
      void queryClient.invalidateQueries({ queryKey: ['my-lab-statuses'] });
      void queryClient.invalidateQueries({ queryKey: ['my-dashboard'] });
      setResumedExisting(false);
      setSkipBootGrace(false);
      setActiveSessionConflict(null);
      setReplaceError(null);
      setSessionId(s.id);
    },
    onError: (err) => {
      // 세션 생성은 사용자당 1개만 허용된다. 409는 단순 실패가 아니라 기존 세션으로 복구 가능한 상태다.
      if (
        err instanceof ApiRequestError &&
        err.status === 409 &&
        err.code === 'session_exists' &&
        err.sessionId
      ) {
        void handleExistingSession(err.sessionId);
      }
    },
  });

  // session_exists 응답에는 session_id만 있으므로, 세션 상세를 다시 읽어 현재 Lab과의 관계를 판정한다.
  async function handleExistingSession(existingSessionId: string) {
    setExistingSessionAction('checking');
    setActiveSessionConflict(null);
    setReplaceError(null);
    try {
      const existing = await api.sessions.get(existingSessionId);
      const result = resolveActiveSessionResume(id, existing);
      if (result.status === 'resume') {
        setActiveSessionConflict(null);
        setResumedExisting(true);
        setSkipBootGrace(result.skipBootGrace);
        setSessionId(result.sessionId);
        return;
      }
      setActiveSessionConflict({ sessionId: existing.id, labId: existing.lab_id });
    } catch {
      setReplaceError('기존 세션 정보를 확인하지 못했습니다. 잠시 후 다시 시도해주세요.');
      setActiveSessionConflict(null);
    } finally {
      setExistingSessionAction(null);
    }
  }

  // 진행 중인 다른 Lab 세션은 사용자가 명시적으로 포기할 때만 새 세션으로 교체한다.
  async function replaceExistingSession(existingSessionId: string) {
    setExistingSessionAction('terminating');
    setReplaceError(null);
    // delete 성공 후 create가 실패한 경우와 delete 자체가 실패한 경우의 사용자 안내가 달라야 한다.
    let deleted = false;
    try {
      try {
        await api.sessions.delete(existingSessionId);
      } catch (err) {
        // 삭제 요청 전에 reaper가 먼저 정리할 수 있다. 404는 원하는 최종 상태와 같으므로 성공으로 취급한다.
        if (!(err instanceof ApiRequestError && err.status === 404)) {
          throw err;
        }
      }
      deleted = true;
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['my-lab-statuses'] }),
        queryClient.invalidateQueries({ queryKey: ['my-dashboard'] }),
      ]);
      const next = await api.sessions.create(id);
      // 교체가 성공하면 이후 화면은 새 세션의 TTL/진행 상태를 기준으로 다시 시작한다.
      setActiveSessionConflict(null);
      setResumedExisting(false);
      setSkipBootGrace(false);
      setReplaceError(null);
      setSessionId(next.id);
    } catch {
      if (deleted) {
        setActiveSessionConflict(null);
        setReplaceError(
          '기존 세션은 종료됐지만 새 세션을 시작하지 못했습니다. 잠시 후 다시 시도해주세요.',
        );
      } else {
        setReplaceError('기존 세션을 종료하지 못했습니다. 잠시 후 다시 시도해주세요.');
      }
    } finally {
      setExistingSessionAction(null);
    }
  }

  if (isLoading) return <DetailSkeleton />;

  if (isError) {
    const notFound = error instanceof Error && error.message === 'NOT_FOUND';
    return (
      <div className="py-20 text-center text-white/45">
        <p>{notFound ? '존재하지 않는 Lab입니다.' : 'Lab을 불러오지 못했습니다.'}</p>
        <BackLink className="mt-4 inline-block" />
      </div>
    );
  }
  if (!lab) return null;

  const steps = lab.steps ?? [];
  const hasContent = steps.length > 0;

  return (
    <div>
      <BackLink className="mb-6 inline-block" />
      <LabHeader lab={lab} />

      {sessionId ? (
        <>
          {resumedExisting && (
            <div className="mt-6 border border-white/25 bg-white/[0.04] px-4 py-3 text-sm text-white/80">
              ✔ 이미 진행 중인 실습 세션이 있어 기존 세션으로 이어서 열었습니다.
            </div>
          )}
          <LabSession sessionId={sessionId} lab={lab} skipBootGrace={skipBootGrace} />
        </>
      ) : (
        <div className="mt-10 border border-white/20 bg-white/[0.02] p-8">
          {hasContent ? (
            <>
              <h2 className="mb-5 font-jbmono text-xs tracking-[0.12em] text-white/45">
                STEPS ({steps.length})
              </h2>
              <ol className="mb-8 border-t border-white/15">
                {steps.map((s) => (
                  <li key={s.id} className="flex gap-5 border-b border-white/15 px-2 py-4 text-sm">
                    <span className="flex-shrink-0 pt-0.5 font-michroma text-[13px] text-white/35">
                      {String(s.id).padStart(2, '0')}
                    </span>
                    <div>
                      <p className="font-semibold tracking-[-0.02em] text-white">{s.title}</p>
                      <p className="mt-1 leading-relaxed text-white/50">{s.description}</p>
                    </div>
                  </li>
                ))}
              </ol>
              <button
                type="button"
                onClick={() => start.mutate()}
                disabled={
                  start.isPending || existingSessionAction !== null || requestedResumeId !== null
                }
                className="rounded-full bg-white px-8 py-3 text-sm font-bold text-black transition-colors hover:bg-white/85 disabled:cursor-not-allowed disabled:bg-white/30"
              >
                {existingSessionAction === 'checking'
                  ? requestedResumeId
                    ? '진행 중인 세션 확인 중...'
                    : '세션 확인 중...'
                  : start.isPending
                    ? '세션 확인 중...'
                    : existingSessionAction === 'terminating'
                      ? '기존 세션 종료 중...'
                      : '실습 시작'}
              </button>
              {activeSessionConflict && (
                <div className="mt-5 max-w-2xl border border-white/30 bg-white/[0.04] px-5 py-4 text-sm">
                  {/* 다른 Lab 세션은 사용자가 이어가기나 종료를 명시적으로 선택해야 한다. */}
                  <p className="font-semibold text-white">
                    이미 진행 중인 다른 실습 세션이 있습니다.
                  </p>
                  <p className="mt-1.5 leading-relaxed text-white/60">
                    한 계정은 동시에 하나의 실습 세션만 사용할 수 있습니다. 기존 실습으로
                    돌아가거나, 기존 세션을 종료하고 이 실습을 새로 시작할 수 있습니다.
                  </p>
                  {replaceError && <p className="mt-2 text-xs text-red-400">{replaceError}</p>}
                  <div className="flex flex-wrap gap-2 mt-3">
                    {activeSessionConflict.labId && (
                      <Link
                        href={buildActiveSessionResumeHref(
                          activeSessionConflict.labId,
                          activeSessionConflict.sessionId,
                        )}
                        className="inline-flex items-center rounded-full border border-white/40 px-4 py-2 text-white transition-colors hover:bg-white hover:text-black"
                      >
                        진행 중인 실습 이어가기
                      </Link>
                    )}
                    <button
                      type="button"
                      onClick={() => void replaceExistingSession(activeSessionConflict.sessionId)}
                      disabled={existingSessionAction !== null}
                      className="inline-flex items-center rounded-full bg-white px-4 py-2 font-semibold text-black transition-colors hover:bg-white/85 disabled:opacity-50"
                    >
                      {existingSessionAction === 'terminating'
                        ? '기존 세션 종료 중...'
                        : '기존 세션 종료 후 이 실습 시작'}
                    </button>
                  </div>
                </div>
              )}
              {replaceError && !activeSessionConflict && (
                <div className="mt-5 max-w-2xl border border-red-500/40 bg-red-500/10 px-5 py-4 text-sm text-red-300">
                  {replaceError}
                </div>
              )}
              {start.isError &&
                existingSessionAction === null &&
                !activeSessionConflict &&
                !replaceError && (
                  <span className="ml-3 text-xs text-red-400">세션을 시작하지 못했습니다.</span>
                )}
            </>
          ) : (
            <p className="text-sm text-white/45">
              이 Lab은 아직 실습 콘텐츠가 준비되지 않았습니다.
            </p>
          )}
        </div>
      )}
    </div>
  );
}

function LabHeader({ lab }: { lab: Lab }) {
  const diff = DIFFICULTY_CONFIG[lab.difficulty];
  return (
    <div>
      <div className="mb-4 flex flex-wrap items-center gap-x-3 gap-y-2 font-jbmono text-xs tracking-[0.1em] text-white/50">
        <span className={`rounded-full border px-2.5 py-1 tracking-[0.08em] ${diff.classes}`}>
          {diff.label}
        </span>
        <span>{lab.duration_min} MIN</span>
        <span>· {lab.step_count} STEPS</span>
      </div>
      <h1 className="break-keep font-chakra text-[clamp(28px,3.6vw,52px)] font-bold leading-[1.1] tracking-[-0.02em] text-white">
        {lab.title}
      </h1>
      <p className="mt-4 max-w-[640px] break-keep text-[15px] leading-[1.7] tracking-[-0.015em] text-white/60">
        {lab.description}
      </p>
      <div className="mt-5 flex flex-wrap gap-2">
        {lab.tags.map((tag) => (
          <span
            key={tag}
            className="rounded-full border border-white/25 px-3.5 py-1 font-jbmono text-xs tracking-[0.08em] text-white/60"
          >
            {tag}
          </span>
        ))}
      </div>
    </div>
  );
}

function BackLink({ className = '' }: { className?: string }) {
  return (
    <Link
      href="/labs"
      className={`font-jbmono text-xs tracking-[0.1em] text-white/45 transition-colors hover:text-white ${className}`}
    >
      ← BACK TO LABS
    </Link>
  );
}

function DetailSkeleton() {
  return (
    <div className="animate-pulse">
      <div className="mb-6 h-4 w-28 rounded bg-white/10" />
      <div className="mb-3 h-5 w-20 rounded bg-white/10" />
      <div className="mb-3 h-10 w-1/2 rounded bg-white/10" />
      <div className="mb-8 h-4 w-2/3 rounded bg-white/10" />
      <div className="h-40 w-full border border-white/10 bg-white/[0.02]" />
    </div>
  );
}

export default function LabDetailPage() {
  const { id } = useParams<{ id: string }>();

  return (
    <Suspense fallback={<DetailSkeleton />}>
      {/* App Router가 같은 page 인스턴스를 재사용해도 Lab 간 세션/충돌 상태가 섞이지 않게 한다. */}
      <LabDetail key={id} />
    </Suspense>
  );
}
