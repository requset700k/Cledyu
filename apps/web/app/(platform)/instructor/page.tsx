'use client';

import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="rounded-lg border border-slate-800 bg-slate-900/50 p-5">
      <h2 className="text-white font-semibold mb-3">{title}</h2>
      {children}
    </section>
  );
}

export default function InstructorPage() {
  const { data, isLoading, isError } = useQuery({
    queryKey: ['instructor-analytics'],
    queryFn: () => api.instructor.analytics(),
  });

  if (isLoading) return <p className="text-slate-400">불러오는 중...</p>;
  if (isError || !data) {
    return <p className="text-red-400">분석을 불러오지 못했습니다. (강사 권한 또는 분석 미설정)</p>;
  }

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-bold text-white">강사 분석</h1>
        <p className="text-slate-400 mt-1 text-sm">코호트 완료율·이탈 지점·힌트 사용 패턴.</p>
      </div>

      <Section title="랩별 완료율">
        {data.lab_completion.length === 0 ? (
          <p className="text-slate-500 text-sm">데이터 없음.</p>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="text-slate-400 text-left">
                <th className="py-2">랩</th>
                <th className="py-2 text-right">시작</th>
                <th className="py-2 text-right">완료</th>
                <th className="py-2 text-right">완료율</th>
              </tr>
            </thead>
            <tbody>
              {data.lab_completion.map((r) => (
                <tr key={r.lab_id} className="text-slate-300">
                  <td className="py-2">{r.lab_id}</td>
                  <td className="py-2 text-right">{r.started}</td>
                  <td className="py-2 text-right">{r.completed}</td>
                  <td className="py-2 text-right">{Math.round(r.completion_rate * 100)}%</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Section>

      <Section title="이탈 지점 (스텝별 검증 실패)">
        {data.step_funnel.length === 0 ? (
          <p className="text-slate-500 text-sm">데이터 없음.</p>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="text-slate-400 text-left">
                <th className="py-2">랩</th>
                <th className="py-2 text-right">스텝</th>
                <th className="py-2 text-right">검증 실패</th>
              </tr>
            </thead>
            <tbody>
              {data.step_funnel.map((r) => (
                <tr key={`${r.lab_id}-${r.step_id}`} className="text-slate-300">
                  <td className="py-2">{r.lab_id}</td>
                  <td className="py-2 text-right">{r.step_id}</td>
                  <td className="py-2 text-right">{r.validation_failures}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Section>

      <Section title="힌트 사용 패턴">
        {data.hint_usage.length === 0 ? (
          <p className="text-slate-500 text-sm">데이터 없음.</p>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="text-slate-400 text-left">
                <th className="py-2">랩</th>
                <th className="py-2 text-right">스텝</th>
                <th className="py-2">소스</th>
                <th className="py-2 text-right">횟수</th>
              </tr>
            </thead>
            <tbody>
              {data.hint_usage.map((r) => (
                <tr key={`${r.lab_id}-${r.step_id}-${r.hint_source}`} className="text-slate-300">
                  <td className="py-2">{r.lab_id}</td>
                  <td className="py-2 text-right">{r.step_id}</td>
                  <td className="py-2">{r.hint_source}</td>
                  <td className="py-2 text-right">{r.hint_count}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Section>
    </div>
  );
}
