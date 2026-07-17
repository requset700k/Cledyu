'use client';

import { useEffect, useState } from 'react';

// 만료 임박 경고 임계값(분). 이 시간 미만이면 호박색, 1분 미만이면 빨강.
const WARN_MIN = 10;

// SessionTimer는 세션 만료(expires_at)까지 남은 시간을 1초 간격으로 보여준다.
// 0 에 도달하면 onExpire 를 1회 호출한다(부모가 만료 UI 로 전환). 서버 reaper 가
// 실제 VM 을 회수하므로, 이 카운트다운은 학생에게 남은 시간을 알리는 UX 레이어다.
export function SessionTimer({
  expiresAt,
  onExpire,
}: {
  expiresAt: string;
  onExpire?: () => void;
}) {
  const [remainingMs, setRemainingMs] = useState(() => deadlineMs(expiresAt) - Date.now());

  useEffect(() => {
    const tick = () => setRemainingMs(deadlineMs(expiresAt) - Date.now());
    tick();
    const t = setInterval(tick, 1000);
    return () => clearInterval(t);
  }, [expiresAt]);

  useEffect(() => {
    if (remainingMs <= 0) onExpire?.();
  }, [remainingMs <= 0, onExpire]); // eslint-disable-line react-hooks/exhaustive-deps

  const expired = remainingMs <= 0;
  const mins = remainingMs / 60000;
  const tone = expired
    ? 'text-red-300 border-red-500/40 bg-red-500/10'
    : mins < 1
      ? 'text-red-300 border-red-500/40 bg-red-500/10'
      : mins < WARN_MIN
        ? 'text-amber-300 border-amber-500/40 bg-amber-500/10'
        : 'border-white/25 bg-white/[0.04] text-white/75';

  return (
    <span
      className={`inline-flex items-center gap-1.5 rounded-full border px-3 py-1 font-jbmono text-xs tabular-nums ${tone}`}
      title="세션 만료까지 남은 시간"
    >
      <ClockIcon />
      {expired ? '세션 만료됨' : `남은 시간 ${fmt(remainingMs)}`}
    </span>
  );
}

function deadlineMs(iso: string): number {
  const t = Date.parse(iso);
  return Number.isNaN(t) ? Date.now() : t;
}

// fmt 는 ms 를 H:MM:SS(또는 MM:SS)로 포맷한다.
function fmt(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  const pad = (n: number) => String(n).padStart(2, '0');
  return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${pad(m)}:${pad(s)}`;
}

function ClockIcon() {
  return (
    <svg className="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
      <circle cx="12" cy="12" r="9" strokeWidth={1.8} />
      <path strokeLinecap="round" strokeWidth={1.8} d="M12 7v5l3 2" />
    </svg>
  );
}
