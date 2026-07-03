import Link from 'next/link';
import type { Lab } from '@/lib/types';
import { DIFFICULTY_CONFIG } from './difficulty';

export function LabCard({ lab }: { lab: Lab }) {
  const diff = DIFFICULTY_CONFIG[lab.difficulty];

  return (
    <div className="group flex min-h-[196px] flex-col rounded-xl border border-slate-700 bg-slate-800/50 p-5 transition-colors hover:border-brand-500/50">
      <div className="mb-3.5 flex items-center justify-between">
        <span className={`text-xs font-medium px-2 py-1 rounded-md border ${diff.classes}`}>
          {diff.label}
        </span>
        <span className="text-xs text-slate-500">{lab.duration_min}분</span>
      </div>

      <h3 className="mb-2 text-base font-semibold text-white transition-colors group-hover:text-brand-400">
        {lab.title}
      </h3>
      <p className="mb-3 line-clamp-2 text-sm leading-5 text-slate-400">{lab.description}</p>

      <div className="mb-4 flex flex-wrap gap-1">
        {lab.tags.map((tag) => (
          <span
            key={tag}
            className="rounded bg-slate-700 px-2 py-0.5 text-xs font-medium text-slate-200"
          >
            {tag}
          </span>
        ))}
      </div>

      <div className="mt-auto flex items-center justify-between gap-4">
        <span className="text-xs font-medium text-slate-500">{lab.step_count}단계</span>
        <Link
          href={`/labs/${lab.id}`}
          className="rounded-lg bg-brand-500 px-3.5 py-1.5 text-sm font-medium text-white transition-colors hover:bg-brand-600"
        >
          실습 시작
        </Link>
      </div>
    </div>
  );
}
