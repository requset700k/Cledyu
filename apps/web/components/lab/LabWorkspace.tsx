'use client';

import { useEffect, useState } from 'react';
import { LabTerminal } from './LabTerminal';
import { apiHttpOrigin } from '@/lib/runtime-api-origin.mjs';

// LabWorkspace는 IDE 랩의 작업 영역 — [터미널 | IDE] 탭을 제공한다.
// 두 패널 모두 마운트를 유지한 채 CSS 로만 전환한다(언마운트 시 WS/에디터 세션이 끊김).
export function LabWorkspace({
  sessionId,
  terminalPath,
  idePath,
  heightClass = 'h-[560px]',
  onTerminalOutput,
  redrawTerminalOnConnect = false,
}: {
  sessionId: string;
  terminalPath: string;
  idePath: string;
  heightClass?: string;
  onTerminalOutput?: (chunk: string) => void;
  redrawTerminalOnConnect?: boolean;
}) {
  const [tab, setTab] = useState<'terminal' | 'ide'>('terminal');

  return (
    <div>
      <div className="flex gap-1 mb-2">
        <TabButton active={tab === 'terminal'} onClick={() => setTab('terminal')}>
          터미널
        </TabButton>
        <TabButton active={tab === 'ide'} onClick={() => setTab('ide')}>
          IDE (VS Code)
        </TabButton>
      </div>
      <div className={tab === 'terminal' ? '' : 'hidden'}>
        <LabTerminal
          terminalPath={terminalPath}
          heightClass={heightClass}
          onOutput={onTerminalOutput}
          redrawOnConnect={redrawTerminalOnConnect}
        />
      </div>
      <div className={tab === 'ide' ? '' : 'hidden'}>
        <IdePane sessionId={sessionId} idePath={idePath} heightClass={heightClass} />
      </div>
    </div>
  );
}

function TabButton({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={`text-xs font-medium px-3 py-1.5 rounded-md transition-colors ${
        active ? 'bg-slate-700 text-white' : 'bg-slate-800/60 text-slate-400 hover:text-slate-200'
      }`}
    >
      {children}
    </button>
  );
}

// IdePane은 세션 VM 의 code-server 가 살아날 때까지 healthz 를 폴링한 뒤 iframe 을 띄운다.
// code-server 설치는 cloud-init 으로 부팅 후 1~2분 걸릴 수 있다(Lab DSL init).
function IdePane({
  sessionId,
  idePath,
  heightClass = 'h-[560px]',
}: {
  sessionId: string;
  idePath: string;
  heightClass?: string;
}) {
  const [ready, setReady] = useState(false);
  const [waitedLong, setWaitedLong] = useState(false);
  const [origin, setOrigin] = useState<string | null>(null);

  useEffect(() => {
    setOrigin(apiHttpOrigin());
  }, []);

  useEffect(() => {
    if (!origin) return;

    let cancelled = false;
    const startedAt = Date.now();

    const poll = async () => {
      try {
        // code-server 의 /healthz — 프록시(JWT 쿠키)가 503 을 주면 아직 준비 전.
        const res = await fetch(`${origin}${idePath}healthz`, { credentials: 'include' });
        if (!cancelled && res.ok) {
          setReady(true);
          return;
        }
      } catch {
        // 네트워크 오류도 '준비 전'으로 취급하고 재시도.
      }
      if (!cancelled) {
        if (Date.now() - startedAt > 120_000) setWaitedLong(true);
        setTimeout(poll, 3000);
      }
    };
    void poll();
    return () => {
      cancelled = true;
    };
  }, [origin, idePath, sessionId]);

  if (!ready || !origin) {
    return (
      <div
        className={`${heightClass} rounded-xl border border-slate-700 bg-slate-900/60 flex flex-col items-center justify-center gap-3`}
      >
        <div className="w-8 h-8 rounded-full border-2 border-slate-700 border-t-brand-400 animate-spin" />
        <p className="text-slate-400 text-sm">IDE(VS Code)를 준비하고 있습니다…</p>
        <p className="text-slate-500 text-xs">
          {waitedLong
            ? '평소보다 오래 걸리고 있습니다. 터미널 탭에서 실습을 먼저 진행하셔도 됩니다.'
            : '도구 설치에 1~2분 정도 걸릴 수 있습니다.'}
        </p>
      </div>
    );
  }

  return (
    <iframe
      src={`${origin}${idePath}?folder=/home/lab/workspace`}
      title="Lab IDE (VS Code)"
      className={`w-full ${heightClass} rounded-xl border border-slate-700 bg-slate-950`}
    />
  );
}
