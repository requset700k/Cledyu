'use client';

import { useState } from 'react';
import { useMutation } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { HintResponse } from '@/lib/types';

const MAX_LEVEL = 3;

// AI 학습 도우미 패널 — 소크라테스식 힌트를 레벨 1(개념)→2(방향)→3(구체) 순으로 제공.
// 레벨 진행은 백엔드(stepStore)가 관리하므로 여기서는 누적 표시만 한다.
// 부모가 key={stepId} 로 마운트를 갈아끼워 스텝 전환 시 힌트 목록이 초기화된다.
export function AiTutorPanel({
  sessionId,
  stepId,
  getTerminalTail,
}: {
  sessionId: string;
  stepId: number;
  getTerminalTail?: () => string;
}) {
  const [hints, setHints] = useState<HintResponse[]>([]);
  const [limitMsg, setLimitMsg] = useState<string | null>(null);

  const fetchHint = useMutation({
    mutationFn: () => api.sessions.hint(sessionId, stepId, undefined, getTerminalTail?.() ?? ''),
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
    <aside className="border border-white/15 bg-white/[0.015] p-5" aria-labelledby="ai-tutor-title">
      <div className="mb-1 flex items-center justify-between gap-3">
        <p id="ai-tutor-title" className="font-jbmono text-xs tracking-[0.1em] text-white/70">
          AI COACH
        </p>
        {hints.length > 0 && (
          <span className="font-jbmono text-[11px] text-white/60">
            힌트 {lastLevel}/{MAX_LEVEL} 단계
          </span>
        )}
      </div>
      <p className="mb-4 mt-2 text-sm leading-relaxed text-white/65">
        막혔다면 다음 한 걸음만 확인하세요. 정답은 보여주지 않습니다.
      </p>

      {hints.length > 0 && (
        <ul className="space-y-2 mb-3">
          {hints.map((h, i) => (
            <li key={i} className="border-l border-white/35 bg-black/40 px-4 py-3">
              <div className="flex items-center gap-2 mb-1">
                <span className="font-jbmono text-[11px] font-semibold text-white/80">
                  LV.{h.hint_level}
                </span>
                <span className="font-jbmono text-[11px] text-white/55">
                  {h.source === 'ai' ? h.model : '기본 힌트'}
                </span>
              </div>
              <p className="mt-1 whitespace-pre-line text-sm leading-relaxed text-white/85">
                {h.hint}
              </p>
              {h.sources && h.sources.length > 0 && (
                <div className="mt-1.5 flex flex-wrap gap-x-3 gap-y-1">
                  {h.sources.map((s, j) =>
                    s.url ? (
                      <a
                        key={j}
                        href={s.url}
                        target="_blank"
                        rel="noreferrer"
                        className="text-[11px] text-white/60 underline underline-offset-2 transition-colors hover:text-white"
                      >
                        {s.title || s.url}
                      </a>
                    ) : (
                      <span key={j} className="text-[11px] text-white/55">
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

      {limitMsg && <p className="mb-2 text-xs text-amber-400">{limitMsg}</p>}

      <button
        type="button"
        onClick={() => fetchHint.mutate()}
        disabled={fetchHint.isPending || exhausted}
        className="rounded-full border border-white/40 px-4 py-2 text-xs font-semibold text-white transition-colors hover:bg-white hover:text-black disabled:cursor-not-allowed disabled:opacity-40 disabled:hover:bg-transparent disabled:hover:text-white"
      >
        {fetchHint.isPending
          ? '힌트 생성 중...'
          : exhausted
            ? '마지막 단계 힌트까지 받았습니다'
            : hints.length === 0
              ? '첫 힌트 보기'
              : '조금 더 구체적인 힌트'}
      </button>
    </aside>
  );
}
