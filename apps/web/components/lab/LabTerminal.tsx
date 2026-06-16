'use client';

import { useEffect, useRef } from 'react';
import '@xterm/xterm/css/xterm.css';

// LabTerminal은 lab VM의 serial console에 연결된 실시간 xterm.js 터미널이다.
// WebSocket은 Next 프록시 대상이 아니므로 Go API에 직접 연결한다(NEXT_PUBLIC_WS_URL).
// xterm은 DOM/WebSocket에 의존하므로 모든 초기화를 useEffect(클라이언트) 안에서 동적 import한다.
// heightClass: 좌(문제)/우(터미널) 2분할 레이아웃이 화면 높이에 맞춰 키울 때 사용.
export function LabTerminal({
  terminalPath,
  heightClass = 'h-80',
}: {
  terminalPath: string;
  heightClass?: string;
}) {
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let disposed = false;
    let ws: WebSocket | null = null;
    let dispose: (() => void) | null = null;

    void (async () => {
      const [{ Terminal }, { FitAddon }] = await Promise.all([
        import('@xterm/xterm'),
        import('@xterm/addon-fit'),
      ]);
      if (disposed || !containerRef.current) return;

      const term = new Terminal({
        fontSize: 13,
        fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
        cursorBlink: true,
        theme: { background: '#020617', foreground: '#e2e8f0' },
      });
      const fit = new FitAddon();
      term.loadAddon(fit);
      term.open(containerRef.current);
      fit.fit();

      const base = process.env.NEXT_PUBLIC_WS_URL ?? 'ws://localhost:8080';
      // dev 모드에서는 stub JWT를 통과하기 위해 dev-token을 쿼리로 전달(WS는 헤더를 못 보냄).
      const token = process.env.NODE_ENV === 'development' ? 'dev-token' : '';
      const url = `${base}${terminalPath}${token ? `?token=${token}` : ''}`;

      ws = new WebSocket(url);
      ws.binaryType = 'arraybuffer';
      ws.onopen = () => term.writeln('\x1b[90m[VM 터미널 연결됨]\x1b[0m');
      ws.onmessage = (e) => {
        term.write(typeof e.data === 'string' ? e.data : new Uint8Array(e.data));
      };
      ws.onclose = () => term.writeln('\r\n\x1b[90m[연결이 종료되었습니다]\x1b[0m');
      ws.onerror = () => term.writeln('\r\n\x1b[31m[연결 오류 — API/VM 상태를 확인하세요]\x1b[0m');

      term.onData((d) => {
        if (ws && ws.readyState === WebSocket.OPEN) ws.send(d);
      });

      const onResize = () => fit.fit();
      window.addEventListener('resize', onResize);

      dispose = () => {
        window.removeEventListener('resize', onResize);
        term.dispose();
      };
    })();

    return () => {
      disposed = true;
      ws?.close();
      dispose?.();
    };
  }, [terminalPath]);

  return (
    <div className="rounded-lg border border-slate-700 bg-slate-950 overflow-hidden">
      <div className="flex items-center gap-1.5 px-3 py-2 border-b border-slate-800 bg-slate-900">
        <span className="w-2.5 h-2.5 rounded-full bg-red-500/70" />
        <span className="w-2.5 h-2.5 rounded-full bg-amber-500/70" />
        <span className="w-2.5 h-2.5 rounded-full bg-emerald-500/70" />
        <span className="ml-2 text-slate-500 text-xs">terminal — Ubuntu (KubeVirt VM)</span>
      </div>
      <div ref={containerRef} className={`${heightClass} w-full p-2`} />
    </div>
  );
}
