'use client';

import { Suspense, useState } from 'react';
import Link from 'next/link';
import { useParams } from 'next/navigation';
import { useQuery, useMutation } from '@tanstack/react-query';
import { api, ApiRequestError } from '@/lib/api';
import { DIFFICULTY_CONFIG } from '@/components/lab/difficulty';
import { LabSession } from '@/components/lab/LabSession';
import type { Lab } from '@/lib/types';

interface ActiveSessionConflict {
  sessionId: string;
  labId?: string;
}

function LabDetail() {
  const { id } = useParams<{ id: string }>();

  // 세션 시작 전 화면에서 관리하는 로컬 상태다. 실제 세션 진행/TTL 처리는 LabSession이 맡는다.
  const [sessionId, setSessionId] = useState<string | null>(null);
  const [resumedExisting, setResumedExisting] = useState(false);
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

  const start = useMutation({
    mutationFn: () => api.sessions.create(id),
    // terminal_url은 LabSession이 자체 polling으로 derive하므로 sessionId만 보관.
    onSuccess: (s) => {
      setResumedExisting(false);
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
      if (existing.lab_id === id) {
        setActiveSessionConflict(null);
        setResumedExisting(true);
        setSessionId(existing.id);
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
      const next = await api.sessions.create(id);
      // 교체가 성공하면 이후 화면은 새 세션의 TTL/진행 상태를 기준으로 다시 시작한다.
      setActiveSessionConflict(null);
      setResumedExisting(false);
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
      <div className="text-center py-20 text-slate-500">
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
      <BackLink className="mb-4 inline-block" />
      <LabHeader lab={lab} />

      {sessionId ? (
        <>
          {resumedExisting && (
            <div className="mt-6 rounded-xl border border-brand-500/30 bg-brand-500/10 px-4 py-3 text-brand-100 text-sm">
              이미 진행 중인 실습 세션이 있어 기존 세션으로 이어서 열었습니다.
            </div>
          )}
          <LabSession sessionId={sessionId} lab={lab} />
        </>
      ) : (
        <div className="mt-6 bg-slate-800/50 border border-slate-700 rounded-xl p-6">
          {hasContent ? (
            <>
              <h2 className="text-white font-semibold mb-3">실습 단계 ({steps.length})</h2>
              <ol className="space-y-2 mb-6">
                {steps.map((s) => (
                  <li key={s.id} className="flex gap-3 text-sm">
                    <span className="text-brand-400 font-semibold flex-shrink-0">{s.id}.</span>
                    <div>
                      <p className="text-white">{s.title}</p>
                      <p className="text-slate-400">{s.description}</p>
                    </div>
                  </li>
                ))}
              </ol>
              <button
                type="button"
                onClick={() => start.mutate()}
                disabled={start.isPending || existingSessionAction !== null}
                className="bg-brand-500 hover:bg-brand-600 disabled:opacity-50 text-white text-sm font-medium px-5 py-2.5 rounded-lg transition-colors"
              >
                {start.isPending || existingSessionAction === 'checking'
                  ? '세션 확인 중...'
                  : existingSessionAction === 'terminating'
                    ? '기존 세션 종료 중...'
                    : '실습 시작'}
              </button>
              {activeSessionConflict && (
                <div className="mt-4 rounded-lg border border-amber-500/30 bg-amber-500/10 px-4 py-3 text-sm max-w-2xl">
                  {/* 다른 Lab 세션은 자동 이동하지 않는다. 사용자가 이어갈지 종료할지 선택해야 한다. */}
                  <p className="text-amber-200 font-medium">
                    이미 진행 중인 다른 실습 세션이 있습니다.
                  </p>
                  <p className="text-amber-100/80 mt-1">
                    한 계정은 동시에 하나의 실습 세션만 사용할 수 있습니다. 기존 실습으로
                    돌아가거나, 기존 세션을 종료하고 이 실습을 새로 시작할 수 있습니다.
                  </p>
                  <p className="text-amber-100/60 mt-1 text-xs">
                    기존 세션 ID: {activeSessionConflict.sessionId}
                  </p>
                  {replaceError && <p className="text-red-200 mt-2 text-xs">{replaceError}</p>}
                  <div className="flex flex-wrap gap-2 mt-3">
                    {activeSessionConflict.labId && (
                      <Link
                        href={`/labs/${activeSessionConflict.labId}`}
                        className="inline-flex items-center rounded-md border border-amber-400/40 px-3 py-1.5 text-amber-100 hover:bg-amber-400/10 transition-colors"
                      >
                        기존 실습으로 이동
                      </Link>
                    )}
                    <button
                      type="button"
                      onClick={() => void replaceExistingSession(activeSessionConflict.sessionId)}
                      disabled={existingSessionAction !== null}
                      className="inline-flex items-center rounded-md bg-amber-400/20 px-3 py-1.5 text-amber-50 hover:bg-amber-400/30 disabled:opacity-50 transition-colors"
                    >
                      {existingSessionAction === 'terminating'
                        ? '기존 세션 종료 중...'
                        : '기존 세션 종료 후 이 실습 시작'}
                    </button>
                  </div>
                </div>
              )}
              {replaceError && !activeSessionConflict && (
                <div className="mt-4 rounded-lg border border-red-500/30 bg-red-500/10 px-4 py-3 text-sm text-red-200 max-w-2xl">
                  {replaceError}
                </div>
              )}
              {start.isError &&
                existingSessionAction === null &&
                !activeSessionConflict &&
                !replaceError && (
                  <span className="ml-3 text-red-400 text-xs">세션을 시작하지 못했습니다.</span>
                )}
            </>
          ) : (
            <p className="text-slate-500 text-sm">
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
      <div className="flex items-center gap-2 mb-2">
        <span className={`text-xs font-medium px-2 py-1 rounded-md border ${diff.classes}`}>
          {diff.label}
        </span>
        <span className="text-slate-500 text-xs">{lab.duration_min}분</span>
        <span className="text-slate-500 text-xs">· {lab.step_count}단계</span>
      </div>
      <h1 className="text-2xl font-bold text-white">{lab.title}</h1>
      <p className="text-slate-400 mt-1 text-sm">{lab.description}</p>
      <div className="flex flex-wrap gap-1 mt-3">
        {lab.tags.map((tag) => (
          <span key={tag} className="text-xs bg-slate-700 text-slate-300 px-2 py-0.5 rounded">
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
      className={`text-slate-400 hover:text-white text-sm transition-colors ${className}`}
    >
      ← Lab 카탈로그
    </Link>
  );
}

function DetailSkeleton() {
  return (
    <div className="animate-pulse">
      <div className="h-4 w-24 bg-slate-800 rounded mb-4" />
      <div className="h-5 w-16 bg-slate-800 rounded mb-2" />
      <div className="h-7 w-1/3 bg-slate-800 rounded mb-2" />
      <div className="h-4 w-2/3 bg-slate-800 rounded mb-6" />
      <div className="h-40 w-full bg-slate-800/50 rounded-xl" />
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
