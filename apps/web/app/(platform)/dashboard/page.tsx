'use client';

import Link from 'next/link';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { buildActiveSessionResumeHref } from '@/lib/active-session-resume.mjs';
import { labStatusLabel } from '@/lib/dashboard.mjs';
import type { DashboardLab, Difficulty } from '@/lib/types';

const DIFFICULTY_LABEL: Record<Difficulty, string> = {
  beginner: '입문',
  intermediate: '중급',
  advanced: '고급',
};

const DIFFICULTY_MONO: Record<Difficulty, string> = {
  beginner: 'BEGINNER',
  intermediate: 'INTERMEDIATE',
  advanced: 'ADVANCED',
};

function LabRows({ labs }: { labs: DashboardLab[] }) {
  if (labs.length === 0) {
    return (
      <div className="border border-white/15 bg-white/[0.02] p-8">
        <p className="text-sm text-white/55">아직 진행 중이거나 완료한 실습이 없습니다.</p>
        <Link
          href="/labs"
          className="mt-4 inline-flex rounded-full border border-white/35 px-5 py-2 text-[13px] font-semibold text-white transition-colors hover:bg-white hover:text-black"
        >
          Lab 카탈로그에서 시작하기 →
        </Link>
      </div>
    );
  }
  return (
    <div className="border-t border-white/20">
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
          <Link
            key={l.lab_id}
            href={href}
            className="group grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-6 gap-y-2 border-b border-white/20 px-3 py-7 transition-colors hover:bg-[#E8E8E3] hover:text-black sm:grid-cols-[minmax(0,1fr)_auto_auto] sm:gap-10 sm:px-5"
          >
            <div className="min-w-0">
              <div className="truncate text-lg font-bold tracking-[-0.025em] text-white transition-colors group-hover:text-black">
                {l.title}
              </div>
              <div className="mt-1 text-[13px] text-white/45 transition-colors group-hover:text-black/55">
                {DIFFICULTY_LABEL[l.difficulty]}
              </div>
            </div>
            <span className="whitespace-nowrap font-jbmono text-xs tracking-[0.1em] text-white/60 transition-colors group-hover:text-black/60">
              [ {labStatusLabel(l.status)} ]
            </span>
            <span className="col-span-2 whitespace-nowrap text-sm font-semibold text-white transition-colors group-hover:text-black sm:col-span-1">
              {actionLabel} →
            </span>
          </Link>
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

  if (isLoading) return <p className="text-white/50">불러오는 중...</p>;
  if (isError || !data) return <p className="text-red-400">대시보드를 불러오지 못했습니다.</p>;

  const s = data.summary;
  const activeLabs = data.labs.filter((lab) => lab.status !== 'not_started');

  return (
    <div>
      <h1 className="font-michroma text-[clamp(34px,4.2vw,60px)] leading-none tracking-[0.05em] text-white">
        MY LEARNING
      </h1>
      <p className="mt-5 text-base tracking-[-0.02em] text-white/55">
        진행 중이거나 완료한 실습을 확인하세요
      </p>

      {/* 상단 지표 밴드 — 점수/완료율/난이도별 진행 */}
      <div className="mt-12 grid grid-cols-1 border-y border-white/25 sm:grid-cols-[1fr_1fr_1.3fr]">
        <div className="border-b border-white/25 py-10 pr-9 sm:border-b-0 sm:border-r">
          <div className="font-jbmono text-xs tracking-[0.12em] text-white/45">SCORE</div>
          <div className="mt-4 font-chakra text-[clamp(42px,4.6vw,68px)] font-bold leading-none text-white">
            {s.score.toLocaleString()}
          </div>
        </div>
        <div className="border-b border-white/25 py-10 sm:border-b-0 sm:border-r sm:px-9">
          <div className="font-jbmono text-xs tracking-[0.12em] text-white/45">COMPLETION</div>
          <div className="mt-4 font-chakra text-[clamp(42px,4.6vw,68px)] font-bold leading-none text-white">
            {s.completion_pct}%
          </div>
          <div className="mt-3 font-jbmono text-xs text-white/40">
            {s.labs_completed} / {s.total_labs} LABS
          </div>
        </div>
        <div className="flex flex-col justify-center gap-4 py-10 sm:pl-9">
          {(Object.keys(s.by_difficulty) as Difficulty[]).map((d) => {
            const dp = s.by_difficulty[d];
            if (dp.total === 0) return null;
            const pct = Math.round((dp.done / dp.total) * 100);
            return (
              <div key={d}>
                <div className="mb-2 flex justify-between font-jbmono text-xs tracking-[0.08em] text-white/55">
                  <span>{DIFFICULTY_MONO[d]}</span>
                  <span>
                    {dp.done}/{dp.total}
                  </span>
                </div>
                <div className="h-0.5 bg-white/[0.18]">
                  <div className="h-full bg-white" style={{ width: `${pct}%` }} />
                </div>
              </div>
            );
          })}
        </div>
      </div>

      <div className="mt-16 font-jbmono text-xs tracking-[0.12em] text-white/45">
        IN PROGRESS / COMPLETED
      </div>
      <div className="mt-5">
        <LabRows labs={activeLabs} />
      </div>
    </div>
  );
}
