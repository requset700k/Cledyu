'use client';

import { Suspense } from 'react';
import { useRouter, useSearchParams } from 'next/navigation';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { LabCard } from '@/components/lab/LabCard';
import type { Difficulty } from '@/lib/types';

const DIFFICULTIES: { value: Difficulty | 'all'; label: string }[] = [
  { value: 'all', label: '전체' },
  { value: 'beginner', label: '입문' },
  { value: 'intermediate', label: '중급' },
  { value: 'advanced', label: '고급' },
];

function LabsCatalog() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const filter = (searchParams.get('difficulty') ?? 'all') as Difficulty | 'all';

  function setFilter(value: Difficulty | 'all') {
    const params = new URLSearchParams(searchParams.toString());
    if (value === 'all') {
      params.delete('difficulty');
    } else {
      params.set('difficulty', value);
    }
    router.replace(`/labs?${params.toString()}`);
  }

  const { data, isLoading, isError } = useQuery({
    queryKey: ['labs'],
    queryFn: () => api.labs.list(),
  });

  const labs = data?.items ?? [];
  const filtered = filter === 'all' ? labs : labs.filter((l) => l.difficulty === filter);

  return (
    <div>
      <div className="mb-8">
        <h1 className="text-2xl font-bold text-white">Lab 카탈로그</h1>
        <p className="text-slate-400 mt-1 text-sm">
          실제 VM 환경에서 클라우드 엔지니어링 기술을 실습하세요
        </p>
      </div>

      <div className="flex gap-2 mb-6">
        {DIFFICULTIES.map((d) => (
          <button
            key={d.value}
            onClick={() => setFilter(d.value)}
            className={`px-4 py-1.5 rounded-full text-sm font-medium transition-colors ${
              filter === d.value
                ? 'bg-brand-500 text-white'
                : 'bg-slate-800 text-slate-400 hover:text-white hover:bg-slate-700'
            }`}
          >
            {d.label}
          </button>
        ))}
      </div>

      {isLoading && (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
          {[...Array(4)].map((_, i) => (
            <LabCardSkeleton key={i} />
          ))}
        </div>
      )}

      {isError && (
        <div className="text-center py-20 text-slate-500">
          <p>Lab 목록을 불러오지 못했습니다. 백엔드 서버를 확인하세요.</p>
        </div>
      )}

      {!isLoading && !isError && filtered.length === 0 && (
        <div className="text-center py-20 text-slate-500">
          <p>해당 난이도의 Lab이 없습니다.</p>
        </div>
      )}

      {!isLoading && !isError && filtered.length > 0 && (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
          {filtered.map((lab) => (
            <LabCard key={lab.id} lab={lab} />
          ))}
        </div>
      )}
    </div>
  );
}

export default function LabsPage() {
  return (
    <Suspense>
      <LabsCatalog />
    </Suspense>
  );
}

function LabCardSkeleton() {
  return (
    <div className="bg-slate-800/50 border border-slate-700 rounded-xl p-6 animate-pulse">
      <div className="flex justify-between mb-4">
        <div className="h-5 w-12 bg-slate-700 rounded-md" />
        <div className="h-5 w-10 bg-slate-700 rounded-md" />
      </div>
      <div className="h-5 w-3/4 bg-slate-700 rounded mb-2" />
      <div className="h-4 w-full bg-slate-700 rounded mb-1" />
      <div className="h-4 w-2/3 bg-slate-700 rounded mb-4" />
      <div className="flex gap-1 mb-4">
        <div className="h-5 w-12 bg-slate-700 rounded" />
        <div className="h-5 w-16 bg-slate-700 rounded" />
      </div>
      <div className="flex justify-between items-center">
        <div className="h-4 w-14 bg-slate-700 rounded" />
        <div className="h-8 w-20 bg-slate-700 rounded-lg" />
      </div>
    </div>
  );
}
