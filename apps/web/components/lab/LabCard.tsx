import Link from 'next/link';
import type { Lab } from '@/lib/types';
import { DIFFICULTY_CONFIG } from './difficulty';

export function LabCard({ lab }: { lab: Lab }) {
  const diff = DIFFICULTY_CONFIG[lab.difficulty];

  return (
    <div className="group bg-slate-800/50 border border-slate-700 hover:border-brand-500/50 rounded-xl p-6 transition-colors flex flex-col">
      <div className="flex items-center justify-between mb-4">
        <span className={`text-xs font-medium px-2 py-1 rounded-md border ${diff.classes}`}>
          {diff.label}
        </span>
        <span className="text-slate-500 text-xs">{lab.duration_min}분</span>
      </div>

      <h3 className="text-white font-semibold text-base mb-2 group-hover:text-brand-400 transition-colors">
        {lab.title}
      </h3>
      <p className="text-slate-400 text-sm mb-4 line-clamp-2 flex-1">{lab.description}</p>

      <div className="flex flex-wrap gap-1 mb-4">
        {lab.tags.map((tag) => (
          <span key={tag} className="text-xs bg-slate-700 text-slate-300 px-2 py-0.5 rounded">
            {tag}
          </span>
        ))}
      </div>

      <div className="flex items-center justify-between mt-auto">
        <span className="text-slate-500 text-xs">{lab.step_count}단계</span>
        <Link
          href={`/labs/${lab.id}`}
          className="bg-brand-500 hover:bg-brand-600 text-white text-sm font-medium px-4 py-1.5 rounded-lg transition-colors"
        >
          실습 시작
        </Link>
      </div>
    </div>
  );
}
