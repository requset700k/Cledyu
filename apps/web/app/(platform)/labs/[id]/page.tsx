'use client';

import { Suspense, useState } from 'react';
import Link from 'next/link';
import { useParams } from 'next/navigation';
import { useQuery, useMutation } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { DIFFICULTY_CONFIG } from '@/components/lab/difficulty';
import { LabSession } from '@/components/lab/LabSession';
import type { Lab } from '@/lib/types';

function LabDetail() {
  const { id } = useParams<{ id: string }>();
  const [sessionId, setSessionId] = useState<string | null>(null);

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
    onSuccess: (s) => setSessionId(s.id),
  });

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
        <LabSession sessionId={sessionId} lab={lab} />
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
                disabled={start.isPending}
                className="bg-brand-500 hover:bg-brand-600 disabled:opacity-50 text-white text-sm font-medium px-5 py-2.5 rounded-lg transition-colors"
              >
                {start.isPending ? '세션 준비 중...' : '실습 시작'}
              </button>
              {start.isError && (
                <span className="ml-3 text-red-400 text-xs">세션을 시작하지 못했습니다.</span>
              )}
            </>
          ) : (
            <p className="text-slate-500 text-sm">이 Lab은 아직 실습 콘텐츠가 준비되지 않았습니다.</p>
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
    <Link href="/labs" className={`text-slate-400 hover:text-white text-sm transition-colors ${className}`}>
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
  return (
    <Suspense fallback={<DetailSkeleton />}>
      <LabDetail />
    </Suspense>
  );
}
