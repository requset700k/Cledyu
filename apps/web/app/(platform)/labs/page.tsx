'use client';

import { Suspense } from 'react';
import Link from 'next/link';
import { useRouter, useSearchParams } from 'next/navigation';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { buildActiveSessionResumeHref } from '@/lib/active-session-resume.mjs';
import { DIFFICULTY_CONFIG } from '@/components/lab/difficulty';
import { ParticleFx } from '@/components/ui/ParticleFx';
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
  const {
    data: labStatuses,
    isFetching: isStatusFetching,
    isError: isStatusError,
  } = useQuery({
    queryKey: ['my-lab-statuses'],
    queryFn: () => api.me.labStatuses(),
    retry: false,
    staleTime: 0,
    refetchOnMount: 'always',
  });

  const labs = data?.items ?? [];
  const filtered = filter === 'all' ? labs : labs.filter((l) => l.difficulty === filter);
  const statusReady = !isStatusFetching && !isStatusError && labStatuses !== undefined;
  const progressByLab = new Map(
    statusReady ? labStatuses.items.map((lab) => [lab.lab_id, lab]) : [],
  );

  return (
    <>
      <ParticleFx kind="stars" className="pointer-events-none fixed inset-0 z-0 opacity-95" />
      <div className="relative z-10 pb-10 pt-8 sm:pt-14">
        <header>
          <h1 className="font-michroma text-[clamp(36px,5vw,68px)] leading-none tracking-[0.05em] text-white">
            LABS
          </h1>
          <p className="mt-5 max-w-[680px] text-[clamp(15px,1.5vw,18px)] leading-relaxed tracking-[-0.02em] text-white/55">
            실제 VM 환경에서 클라우드 엔지니어링 기술을 실습하세요
          </p>
        </header>

        <div className="mt-10 overflow-x-auto pb-1">
          <div className="flex w-max gap-1 rounded-full border border-white/25 bg-black/45 p-1.5 backdrop-blur-sm">
            {DIFFICULTIES.map((d) => (
              <button
                key={d.value}
                type="button"
                onClick={() => setFilter(d.value)}
                aria-pressed={filter === d.value}
                className={`rounded-full px-5 py-2 text-[13px] font-semibold tracking-[-0.01em] transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-white ${
                  filter === d.value ? 'bg-white text-black' : 'text-white/55 hover:text-white'
                }`}
              >
                {d.label}
              </button>
            ))}
          </div>
        </div>

        <div className="mt-12 border-t border-white/20">
          {isLoading && [...Array(4)].map((_, i) => <LabRowSkeleton key={i} />)}

          {isError && (
            <p className="py-20 text-center text-white/40">
              Lab 목록을 불러오지 못했습니다. 백엔드 서버를 확인하세요.
            </p>
          )}

          {!isLoading && !isError && filtered.length === 0 && (
            <p className="py-20 text-center text-white/40">해당 난이도의 Lab이 없습니다.</p>
          )}

          {!isLoading &&
            !isError &&
            filtered.map((lab, i) => {
              const progress = progressByLab.get(lab.id);
              const detailHref = `/labs/${encodeURIComponent(lab.id)}`;
              const href =
                statusReady && progress?.status === 'in_progress' && progress.session_id
                  ? buildActiveSessionResumeHref(lab.id, progress.session_id)
                  : detailHref;
              const actionLabel = isStatusFetching
                ? '상태 확인 중'
                : isStatusError
                  ? '상세 보기'
                  : progress?.status === 'in_progress'
                    ? '이어가기'
                    : progress?.status === 'completed'
                      ? '다시 하기'
                      : '시작하기';
              const difficulty = DIFFICULTY_CONFIG[lab.difficulty];

              return (
                <Link
                  key={lab.id}
                  href={isStatusFetching ? '#' : href}
                  aria-disabled={isStatusFetching || undefined}
                  tabIndex={isStatusFetching ? -1 : undefined}
                  onClick={(event) => {
                    if (isStatusFetching) event.preventDefault();
                  }}
                  className={`grid grid-cols-[44px_minmax(0,1fr)] gap-x-4 gap-y-5 border-b border-l-2 border-b-white/20 border-l-transparent px-3 py-7 transition-colors sm:grid-cols-[72px_minmax(0,1fr)_auto] sm:items-center sm:gap-8 sm:px-6 sm:py-9 ${
                    isStatusFetching
                      ? 'cursor-wait'
                      : 'group hover:border-l-[#E8E8E3] hover:bg-[#E8E8E3] focus-visible:bg-[#E8E8E3] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-[-3px] focus-visible:outline-black'
                  }`}
                >
                  <span className="font-michroma text-xl text-white/35 transition-colors group-hover:text-black/45 group-focus-visible:text-black/45 sm:text-[26px]">
                    {String(i + 1).padStart(2, '0')}
                  </span>
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
                      <h2 className="font-chakra text-[clamp(21px,2.2vw,30px)] font-semibold leading-tight tracking-[-0.025em] text-white transition-colors group-hover:text-black group-focus-visible:text-black">
                        {lab.title}
                      </h2>
                      <span
                        className={`whitespace-nowrap rounded-full border px-2.5 py-1 font-jbmono text-[11px] tracking-[0.08em] transition-colors group-hover:border-black/20 group-hover:bg-black/[0.04] group-hover:text-black/65 group-focus-visible:border-black/20 group-focus-visible:bg-black/[0.04] group-focus-visible:text-black/65 ${difficulty.classes}`}
                      >
                        {difficulty.label}
                      </span>
                    </div>
                    <p className="mt-2.5 max-w-[680px] break-keep text-[15px] leading-[1.7] tracking-[-0.015em] text-white/50 transition-colors group-hover:text-black/60 group-focus-visible:text-black/60 sm:text-base">
                      {lab.description}
                    </p>
                    {lab.tags.length > 0 && (
                      <div className="mt-4 flex flex-wrap gap-1.5">
                        {lab.tags.map((tag) => (
                          <span
                            key={tag}
                            className="rounded bg-white/[0.06] px-2 py-1 font-jbmono text-[11px] tracking-[0.04em] text-white/45 transition-colors group-hover:bg-black/[0.06] group-hover:text-black/55 group-focus-visible:bg-black/[0.06] group-focus-visible:text-black/55"
                          >
                            {tag}
                          </span>
                        ))}
                      </div>
                    )}
                  </div>
                  <div className="col-span-2 flex items-center justify-between gap-5 pl-[60px] sm:col-span-1 sm:min-w-[220px] sm:justify-end sm:pl-0">
                    <span className="whitespace-nowrap font-jbmono text-xs text-white/45 transition-colors group-hover:text-black/55 group-focus-visible:text-black/55 sm:text-sm">
                      {lab.duration_min}분 · {lab.step_count}단계
                    </span>
                    <span className="flex items-center gap-2 whitespace-nowrap text-[15px] font-semibold text-white/75 transition-colors group-hover:text-black group-focus-visible:text-black">
                      {actionLabel}
                      <span
                        aria-hidden="true"
                        className="transition-transform group-hover:translate-x-1"
                      >
                        →
                      </span>
                    </span>
                  </div>
                </Link>
              );
            })}
        </div>
      </div>
    </>
  );
}

export default function LabsPage() {
  return (
    <Suspense>
      <LabsCatalog />
    </Suspense>
  );
}

function LabRowSkeleton() {
  return (
    <div className="grid grid-cols-[44px_minmax(0,1fr)] gap-4 border-b border-white/20 px-3 py-7 motion-safe:animate-pulse sm:grid-cols-[72px_minmax(0,1fr)_190px] sm:items-center sm:gap-8 sm:px-6 sm:py-9">
      <div className="h-6 w-9 rounded bg-white/10" />
      <div>
        <div className="h-6 w-2/5 rounded bg-white/10" />
        <div className="mt-3 h-4 w-3/4 rounded bg-white/10" />
        <div className="mt-4 h-5 w-1/4 rounded bg-white/10" />
      </div>
      <div className="col-span-2 h-4 w-36 justify-self-end rounded bg-white/10 sm:col-span-1" />
    </div>
  );
}
