'use client';

import { useState } from 'react';
import { useMutation } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { HintResponse } from '@/lib/types';

const MAX_LEVEL = 3;

// AI 학습 도우미 패널 — 소크라테스식 힌트를 레벨 1(개념)→2(방향)→3(구체) 순으로 제공.
// 레벨 진행은 백엔드(stepStore)가 관리하므로 여기서는 누적 표시만 한다.
// 부모가 key={stepId} 로 마운트를 갈아끼워 스텝 전환 시 힌트 목록이 초기화된다.
export function AiTutorPanel({ sessionId, stepId }: { sessionId: string; stepId: number }) {
  const [hints, setHints] = useState<HintResponse[]>([]);
  const [limitMsg, setLimitMsg] = useState<string | null>(null);

  const fetchHint = useMutation({
    mutationFn: () => api.sessions.hint(sessionId, stepId),
    onSuccess: (h) => {
      setLimitMsg(null);
      setHints((prev) => [...prev, h]);
    },
    onError: (e: Error) => {
      // 429(힌트 한도)는 백엔드가 한국어 메시지를 내려준다. 그 외에는 일반 오류 문구.
      setLimitMsg(
        e.message === 'SERVER_ERROR' || e.message === 'NETWORK_ERROR'
          ? '힌트를 가져오지 못했습니다. 잠시 후 다시 시도하세요.'
          : e.message,
      );
    },
  });

  const lastLevel = hints[hints.length - 1]?.hint_level ?? 0;
  const exhausted = lastLevel >= MAX_LEVEL;

  return (
    <div className="rounded-xl border border-indigo-500/30 bg-indigo-500/5 p-4">
      <div className="flex items-center justify-between mb-1">
        <p className="text-indigo-300 text-sm font-semibold">AI 학습 도우미</p>
        {hints.length > 0 && (
          <span className="text-indigo-400/70 text-[11px]">
            힌트 {lastLevel}/{MAX_LEVEL} 단계
          </span>
        )}
      </div>
      <p className="text-slate-400 text-xs mb-3">
        막혔을 때 단계별 힌트를 받아보세요. 정답 대신 스스로 풀 수 있게 유도합니다.
      </p>

      {hints.length > 0 && (
        <ul className="space-y-2 mb-3">
          {hints.map((h, i) => (
            <li key={i} className="rounded-lg border border-slate-700 bg-slate-900/60 px-3 py-2">
              <div className="flex items-center gap-2 mb-1">
                <span className="text-indigo-300 text-[11px] font-semibold">
                  레벨 {h.hint_level}
                </span>
                <span className="text-slate-500 text-[11px]">
                  {h.source === 'ai' ? h.model : '기본 힌트'}
                </span>
              </div>
              <p className="text-slate-200 text-sm whitespace-pre-line">{h.hint}</p>
              {h.sources && h.sources.length > 0 && (
                <div className="mt-1.5 flex flex-wrap gap-x-3 gap-y-1">
                  {h.sources.map((s, j) =>
                    s.url ? (
                      <a
                        key={j}
                        href={s.url}
                        target="_blank"
                        rel="noreferrer"
                        className="text-indigo-400 hover:text-indigo-300 text-[11px] underline underline-offset-2"
                      >
                        {s.title || s.url}
                      </a>
                    ) : (
                      <span key={j} className="text-slate-500 text-[11px]">
                        {s.title}
                      </span>
                    ),
                  )}
                </div>
              )}
            </li>
          ))}
        </ul>
      )}

      {limitMsg && <p className="text-amber-400 text-xs mb-2">{limitMsg}</p>}

      <button
        type="button"
        onClick={() => fetchHint.mutate()}
        disabled={fetchHint.isPending || exhausted}
        className="bg-indigo-500/80 hover:bg-indigo-500 disabled:opacity-50 disabled:hover:bg-indigo-500/80 text-white text-xs font-medium px-4 py-1.5 rounded-lg transition-colors"
      >
        {fetchHint.isPending
          ? '힌트 생성 중...'
          : exhausted
            ? '마지막 단계 힌트까지 받았습니다'
            : hints.length === 0
              ? 'AI 힌트 받기'
              : '조금 더 구체적인 힌트'}
      </button>
    </div>
  );
}
