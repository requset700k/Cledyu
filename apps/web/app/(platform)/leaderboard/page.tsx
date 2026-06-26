'use client';

import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { mergeMyRank } from '@/lib/leaderboard.mjs';
import type { LeaderboardItem } from '@/lib/types';

function RankTable({ rows, rankHeader = '#' }: { rows: (LeaderboardItem & { isMe?: boolean })[]; rankHeader?: string }) {
  if (rows.length === 0) {
    return <p className="text-slate-500 text-sm">아직 기록이 없습니다.</p>;
  }
  return (
    <table className="w-full text-sm">
      <thead>
        <tr className="text-slate-400 text-left">
          <th className="py-2 w-12">{rankHeader}</th>
          <th className="py-2">이름</th>
          <th className="py-2 text-right">점수</th>
          <th className="py-2 text-right">완료</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => (
          <tr
            key={`${r.rank}-${r.name}`}
            className={r.isMe ? 'bg-brand-500/10 text-white' : 'text-slate-300'}
          >
            <td className="py-2">{r.rank}</td>
            <td className="py-2">{r.name}</td>
            <td className="py-2 text-right">{r.score}</td>
            <td className="py-2 text-right">{r.labs_completed}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

export default function LeaderboardPage() {
  const queryClient = useQueryClient();
  const { data, isLoading, isError } = useQuery({
    queryKey: ['leaderboard'],
    queryFn: () => api.leaderboard.get(),
  });
  const toggleHidden = useMutation({
    mutationFn: (hidden: boolean) => api.me.setPreferences(hidden),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['leaderboard'] });
    },
  });

  if (isLoading) return <p className="text-slate-400">불러오는 중...</p>;
  if (isError || !data) return <p className="text-red-400">리더보드를 불러오지 못했습니다.</p>;

  const isHidden = data.me.rank === 0;
  const hofRows = mergeMyRank(data.hall_of_fame, data.me);

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-bold text-white">리더보드</h1>
        <p className="text-slate-400 mt-1 text-sm">랩을 완료하고 명예의 전당에 이름을 올리세요.</p>
      </div>

      <section className="rounded-lg border border-slate-800 bg-slate-900/50 p-5">
        <h2 className="text-white font-semibold mb-3">내 학습 현황</h2>
        <div className="flex gap-6 text-sm">
          <div>
            <div className="text-slate-400">점수</div>
            <div className="text-white text-xl font-bold">{data.me.score}</div>
          </div>
          <div>
            <div className="text-slate-400">순위</div>
            <div className="text-white text-xl font-bold">
              {data.me.rank === 0 ? '비공개' : `#${data.me.rank}`}
            </div>
          </div>
          <div>
            <div className="text-slate-400">완료한 랩</div>
            <div className="text-white text-xl font-bold">{data.me.labs_completed}</div>
          </div>
        </div>
        <label className="mt-4 flex items-center gap-2 text-sm text-slate-300">
          <input
            type="checkbox"
            checked={!isHidden}
            onChange={(e) => toggleHidden.mutate(!e.target.checked)}
            disabled={toggleHidden.isPending}
          />
          리더보드에 내 이름 표시
        </label>
      </section>

      <section className="rounded-lg border border-slate-800 bg-slate-900/50 p-5">
        <h2 className="text-white font-semibold mb-3">명예의 전당</h2>
        <RankTable rows={hofRows} />
      </section>

      <section className="rounded-lg border border-slate-800 bg-slate-900/50 p-5">
        <h2 className="text-white font-semibold mb-3">최근 7일 급상승</h2>
        <RankTable rows={data.recent_risers} rankHeader="7일 순위" />
      </section>
    </div>
  );
}
