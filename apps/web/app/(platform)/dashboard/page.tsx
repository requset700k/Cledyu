'use client';

import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
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
    return <p className="text-slate-500 text-sm">표시할 랩이 없습니다.</p>;
  }
  return (
    <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
      {labs.map((l) => (
        <div
          key={l.lab_id}
          className="rounded-lg border border-slate-800 bg-slate-900/50 p-4 flex items-center justify-between"
        >
          <div>
            <div className="text-white text-sm font-medium">{l.title}</div>
            <div className="text-slate-500 text-xs mt-0.5">{DIFFICULTY_LABEL[l.difficulty]}</div>
          </div>
          <span className={`px-2 py-1 rounded-md text-xs ${STATUS_CLASS[l.status] ?? ''}`}>
            {labStatusLabel(l.status)}
          </span>
        </div>
      ))}
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

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-bold text-white">내 학습</h1>
        <p className="text-slate-400 mt-1 text-sm">나의 학습 현황과 랩별 수료 상태를 확인하세요.</p>
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
        <h2 className="text-white font-semibold mb-3">랩별 현황</h2>
        <LabGrid labs={data.labs} />
      </section>
    </div>
  );
}
