'use client';

import Link from 'next/link';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { buildActiveSessionResumeHref } from '@/lib/active-session-resume.mjs';
import { labStatusLabel } from '@/lib/dashboard.mjs';
import type { DashboardLab, Difficulty } from '@/lib/types';

const STATUS_CLASS: Record<string, string> = {
  completed: 'bg-emerald-500/15 text-emerald-300',
  in_progress: 'bg-amber-500/15 text-amber-300',
  not_started: 'bg-slate-700/40 text-slate-400',
};

const DIFFICULTY_LABEL: Record<Difficulty, string> = {
  beginner: '입문',
  intermediate: '중급',
  advanced: '고급',
};

function LabGrid({ labs }: { labs: DashboardLab[] }) {
  if (labs.length === 0) {
    return (
      <div className="rounded-lg border border-slate-800 bg-slate-900/40 p-5">
        <p className="text-slate-400 text-sm">아직 진행 중이거나 완료한 실습이 없습니다.</p>
        <Link
          href="/labs"
          className="mt-3 inline-flex rounded-md bg-slate-800 px-3 py-1.5 text-xs font-medium text-slate-200 transition-colors hover:bg-brand-500 hover:text-white"
        >
          Lab 카탈로그에서 시작하기
        </Link>
      </div>
    );
  }
  return (
    <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
      {labs.map((l) => {
        const href =
          l.status === 'in_progress' && l.session_id
            ? buildActiveSessionResumeHref(l.lab_id, l.session_id)
            : `/labs/${encodeURIComponent(l.lab_id)}`;
        const actionLabel =
          l.status === 'in_progress'
            ? '이어가기'
            : l.status === 'completed'
              ? '다시 보기'
              : '시작하기';

        return (
          <div
            key={l.lab_id}
            className="rounded-lg border border-slate-800 bg-slate-900/50 p-4"
          >
            <div className="flex items-start justify-between gap-3">
              <div>
                <div className="text-white text-sm font-medium">{l.title}</div>
                <div className="text-slate-500 text-xs mt-0.5">
                  {DIFFICULTY_LABEL[l.difficulty]}
                </div>
              </div>
              <span
                className={`px-2 py-1 rounded-md text-xs flex-shrink-0 ${
                  STATUS_CLASS[l.status] ?? ''
                }`}
              >
                {labStatusLabel(l.status)}
              </span>
            </div>

            <div className="mt-4 flex items-center justify-between gap-3">
              <p className="text-xs text-slate-500">
                {l.status === 'in_progress'
                  ? '진행 중인 실습 세션이 있습니다.'
                  : l.status === 'completed'
                    ? '완료한 실습입니다.'
                    : '아직 시작하지 않은 실습입니다.'}
              </p>
              <Link
                href={href}
                className="rounded-md bg-slate-800 px-3 py-1.5 text-xs font-medium text-slate-200 transition-colors hover:bg-brand-500 hover:text-white"
              >
                {actionLabel}
              </Link>
            </div>
          </div>
        );
      })}
    </div>
  );
}

export default function DashboardPage() {
  const { data, isLoading, isError } = useQuery({
    queryKey: ['my-dashboard'],
    queryFn: () => api.me.dashboard(),
  });

  if (isLoading) return <p className="text-slate-400">불러오는 중...</p>;
  if (isError || !data) return <p className="text-red-400">대시보드를 불러오지 못했습니다.</p>;

  const s = data.summary;
  const activeLabs = data.labs.filter((lab) => lab.status !== 'not_started');

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-bold text-white">내 학습</h1>
        <p className="text-slate-400 mt-1 text-sm">진행 중이거나 완료한 실습을 확인하세요.</p>
      </div>

      <section className="rounded-lg border border-slate-800 bg-slate-900/50 p-5">
        <div className="flex flex-wrap gap-6 text-sm">
          <div>
            <div className="text-slate-400">점수</div>
            <div className="text-white text-xl font-bold">{s.score}</div>
          </div>
          <div>
            <div className="text-slate-400">순위</div>
            <div className="text-white text-xl font-bold">
              {s.rank === 0 ? '비공개' : `#${s.rank}`}
            </div>
          </div>
          <div>
            <div className="text-slate-400">완료율</div>
            <div className="text-white text-xl font-bold">
              {s.completion_pct}% ({s.labs_completed}/{s.total_labs})
            </div>
          </div>
        </div>
        <div className="mt-4 space-y-2">
          {(Object.keys(s.by_difficulty) as Difficulty[]).map((d) => {
            const dp = s.by_difficulty[d];
            if (dp.total === 0) return null;
            const pct = Math.round((dp.done / dp.total) * 100);
            return (
              <div key={d}>
                <div className="flex justify-between text-xs text-slate-400">
                  <span>{DIFFICULTY_LABEL[d]}</span>
                  <span>
                    {dp.done}/{dp.total}
                  </span>
                </div>
                <div className="h-2 rounded bg-slate-800 overflow-hidden">
                  <div className="h-full bg-brand-500" style={{ width: `${pct}%` }} />
                </div>
              </div>
            );
          })}
        </div>
      </section>

      <section>
        <h2 className="text-white font-semibold mb-3">학습 현황</h2>
        <LabGrid labs={activeLabs} />
      </section>
    </div>
  );
}
