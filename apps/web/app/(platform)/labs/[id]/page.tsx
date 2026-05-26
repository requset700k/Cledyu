'use client';

import Link from 'next/link';
import { useParams } from 'next/navigation';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { Difficulty, Lab } from '@/lib/types';

const DIFFICULTY_CONFIG: Record<Difficulty, { label: string; classes: string }> = {
  beginner: { label: '입문', classes: 'bg-emerald-500/15 text-emerald-400 border-emerald-500/30' },
  intermediate: { label: '중급', classes: 'bg-amber-500/15 text-amber-400 border-amber-500/30' },
  advanced: { label: '고급', classes: 'bg-red-500/15 text-red-400 border-red-500/30' },
};

export default function LabDetailPage() {
  const params = useParams<{ id: string }>();
  const labId = params.id;

  const { data: lab, isLoading, error } = useQuery({
    queryKey: ['labs', labId],
    queryFn: () => api.labs.get(labId),
  });

  if (isLoading) return <LabDetailSkeleton />;

  if (error) {
    const message =
      error instanceof Error && error.message === 'NOT_FOUND'
        ? '요청한 Lab을 찾을 수 없습니다.'
        : 'Lab 상세 정보를 불러오지 못했습니다.';

    return (
      <div className="max-w-3xl mx-auto py-20 text-center">
        <p className="text-slate-500 text-sm mb-4">{message}</p>
        <Link
          href="/labs"
          className="inline-flex items-center rounded-lg bg-slate-800 px-4 py-2 text-sm font-medium text-slate-200 hover:bg-slate-700"
        >
          Lab 목록으로 돌아가기
        </Link>
      </div>
    );
  }

  if (!lab) return null;

  return (
    <div className="max-w-5xl mx-auto">
      <Link href="/labs" className="text-sm text-slate-400 hover:text-white">
        ← Lab 목록
      </Link>

      <section className="mt-6 grid gap-6 lg:grid-cols-[1fr_280px]">
        <LabOverview lab={lab} />
        <LabStartPanel lab={lab} />
      </section>
    </div>
  );
}

function LabOverview({ lab }: { lab: Lab }) {
  const diff = DIFFICULTY_CONFIG[lab.difficulty];

  return (
    <div className="min-w-0">
      <div className="mb-5 flex flex-wrap items-center gap-2">
        <span className={`rounded-md border px-2 py-1 text-xs font-medium ${diff.classes}`}>
          {diff.label}
        </span>
        <span className="rounded-md border border-slate-700 bg-slate-800 px-2 py-1 text-xs text-slate-300">
          {lab.vm_type}
        </span>
      </div>

      <h1 className="text-3xl font-bold tracking-tight text-white">{lab.title}</h1>
      <p className="mt-3 max-w-3xl text-sm leading-6 text-slate-400">{lab.description}</p>

      <div className="mt-8 grid gap-3 sm:grid-cols-3">
        <Metric label="예상 시간" value={`${lab.duration_min}분`} />
        <Metric label="진행 단계" value={`${lab.step_count}단계`} />
        <Metric label="세션 제한" value="3시간" />
      </div>

      <div className="mt-8">
        <h2 className="text-sm font-semibold text-slate-200">학습 주제</h2>
        <div className="mt-3 flex flex-wrap gap-2">
          {lab.tags.map((tag) => (
            <span key={tag} className="rounded bg-slate-800 px-2.5 py-1 text-xs text-slate-300">
              {tag}
            </span>
          ))}
        </div>
      </div>
    </div>
  );
}

function LabStartPanel({ lab }: { lab: Lab }) {
  return (
    <aside className="h-fit rounded-lg border border-slate-700 bg-slate-800/50 p-5">
      <div className="mb-5">
        <p className="text-xs font-medium text-slate-500">실습 준비</p>
        <p className="mt-1 text-sm leading-6 text-slate-300">
          {lab.vm_type} 환경에서 단계별 실습을 진행합니다.
        </p>
      </div>

      <button
        type="button"
        disabled
        className="w-full cursor-not-allowed rounded-lg bg-slate-700 px-4 py-2.5 text-sm font-semibold text-slate-400"
      >
        준비 중
      </button>
    </aside>
  );
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-lg border border-slate-700 bg-slate-800/40 p-4">
      <p className="text-xs text-slate-500">{label}</p>
      <p className="mt-2 text-sm font-semibold text-white">{value}</p>
    </div>
  );
}

function LabDetailSkeleton() {
  return (
    <div className="max-w-5xl mx-auto animate-pulse">
      <div className="h-5 w-24 rounded bg-slate-800" />
      <div className="mt-6 grid gap-6 lg:grid-cols-[1fr_280px]">
        <div>
          <div className="mb-5 flex gap-2">
            <div className="h-7 w-14 rounded-md bg-slate-800" />
            <div className="h-7 w-20 rounded-md bg-slate-800" />
          </div>
          <div className="h-9 w-64 rounded bg-slate-800" />
          <div className="mt-3 h-4 w-full max-w-2xl rounded bg-slate-800" />
          <div className="mt-2 h-4 w-3/4 rounded bg-slate-800" />
          <div className="mt-8 grid gap-3 sm:grid-cols-3">
            <div className="h-20 rounded-lg bg-slate-800" />
            <div className="h-20 rounded-lg bg-slate-800" />
            <div className="h-20 rounded-lg bg-slate-800" />
          </div>
        </div>
        <div className="h-44 rounded-lg bg-slate-800" />
      </div>
    </div>
  );
}
