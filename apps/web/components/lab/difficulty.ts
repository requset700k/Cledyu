import type { Difficulty } from '@/lib/types';

// 난이도 배지의 라벨/색상. LabCard와 Lab 상세 페이지에서 공유.
export const DIFFICULTY_CONFIG: Record<Difficulty, { label: string; classes: string }> = {
  beginner: { label: '입문', classes: 'bg-emerald-500/15 text-emerald-400 border-emerald-500/30' },
  intermediate: { label: '중급', classes: 'bg-amber-500/15 text-amber-400 border-amber-500/30' },
  advanced: { label: '고급', classes: 'bg-red-500/15 text-red-400 border-red-500/30' },
};
