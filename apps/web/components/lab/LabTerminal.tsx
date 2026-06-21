'use client';

import { useEffect, useRef, useState } from 'react';
import '@xterm/xterm/css/xterm.css';
import {
  browserWebSocketOrigin,
  reconnectDelayMs,
  shouldReconnect,
} from '@/lib/runtime-api-origin.mjs';

type ConnectionState = 'connecting' | 'connected' | 'reconnecting' | 'error';

// LabTerminal은 lab VM의 serial console에 연결된 실시간 xterm.js 터미널이다.
// WebSocket은 Next HTTP route handler의 프록시 대상이 아니므로 Go API에 직접 연결한다.
// 운영/로컬 API origin 결정과 backoff 계산은 runtime-api-origin.mjs에 모아 IDE와 공유한다.
// xterm은 DOM/WebSocket에 의존하므로 모든 초기화를 useEffect(클라이언트) 안에서 동적 import한다.
// 연결이 끊겨도 xterm 인스턴스와 출력은 유지하고 WebSocket만 교체해 학습 흐름을 보존한다.
// heightClass: 좌(문제)/우(터미널) 2분할 레이아웃이 화면 높이에 맞춰 키울 때 사용.
export function LabTerminal({
  terminalPath,
  heightClass = 'h-80',
}: {
  terminalPath: string;
  heightClass?: string;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const [connectionState, setConnectionState] = useState<ConnectionState>('connecting');

  useEffect(() => {
    // effect cleanup 이후 비동기 import·socket callback이 새 연결을 만들지 못하게 막는 수명주기 플래그.
    let disposed = false;
    // 현재 입력을 전달할 활성 socket. 재연결 때 새 socket으로 교체되고 xterm은 그대로 유지된다.
    let ws: WebSocket | null = null;
    // onerror/onclose가 연달아 호출돼도 재연결 timer는 하나만 예약한다.
    let retryTimer: ReturnType<typeof setTimeout> | null = null;
    // 연속 실패 횟수. 연결 성공 시 0으로 초기화해 다음 장애는 다시 1초부터 재시도한다.
    let retryAttempt = 0;
    // xterm과 resize listener 정리를 비동기 초기화 완료 후 effect cleanup에 연결한다.
    let dispose: (() => void) | null = null;
    setConnectionState('connecting');

    void (async () => {
      // xterm은 브라우저 DOM에 의존하므로 SSR bundle 평가 시점이 아닌 mount 이후에 불러온다.
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

      // 재연결 가능한 종료를 영구 실패로 표시하지 않고 exponential backoff 후 다시 연결한다.
      // retryTimer guard가 error/close 중복 callback에 의한 동시 socket 생성을 방지한다.
      const scheduleReconnect = () => {
        if (disposed || retryTimer) return;
        setConnectionState('reconnecting');
        retryTimer = setTimeout(() => {
          retryTimer = null;
          retryAttempt += 1;
          connect();
        }, reconnectDelayMs(retryAttempt));
      };

      // 최초 연결과 재연결이 동일한 URL·인증·event handler 경로를 사용하게 한다.
      const connect = () => {
        if (disposed) return;

        const base = browserWebSocketOrigin();
        // dev 모드에서는 stub JWT를 통과하기 위해 dev-token을 쿼리로 전달(WS는 헤더를 못 보냄).
        const token = process.env.NODE_ENV === 'development' ? 'dev-token' : '';
        const url = `${base}${terminalPath}${token ? `?token=${token}` : ''}`;

        let socket: WebSocket;
        try {
          socket = new WebSocket(url);
        } catch {
          // 잘못된 URL 등 생성 단계의 동기 오류도 네트워크 종료와 같은 정책으로 복구한다.
          setConnectionState('error');
          scheduleReconnect();
          return;
        }

        ws = socket;
        socket.binaryType = 'arraybuffer';
        socket.onopen = () => {
          // 한 번 연결되면 이전 실패 횟수는 더 이상 현재 연결의 품질을 나타내지 않는다.
          retryAttempt = 0;
          setConnectionState('connected');
          term.focus();
        };
        socket.onmessage = (event) => {
          term.write(typeof event.data === 'string' ? event.data : new Uint8Array(event.data));
        };
        socket.onerror = () => {
          // 브라우저는 error에 상세 원인을 노출하지 않는다. close callback에서 code를 받아 재연결한다.
          setConnectionState('error');
          socket.close();
        };
        socket.onclose = (event) => {
          // 오래된 socket callback이 새 활성 socket을 null로 덮어쓰지 않게 동일 객체일 때만 비운다.
          if (ws === socket) ws = null;
          // component dispose와 정상 종료(1000)는 의도된 종료이므로 재연결하지 않는다.
          if (shouldReconnect(disposed, event.code)) scheduleReconnect();
        };
      };

      connect();

      // 재연결 중에는 입력을 버리고, OPEN 상태인 현재 socket에만 키 입력을 전달한다.
      term.onData((d) => {
        if (ws?.readyState === WebSocket.OPEN) ws.send(d);
      });

      const onResize = () => fit.fit();
      window.addEventListener('resize', onResize);

      dispose = () => {
        window.removeEventListener('resize', onResize);
        term.dispose();
      };
    })();

    return () => {
      // cleanup 순서가 중요하다. 먼저 disposed를 세워 close callback의 재연결 예약을 차단한다.
      disposed = true;
      if (retryTimer) clearTimeout(retryTimer);
      // 정상 종료 코드 1000으로 닫아 현재 effect가 의도적으로 연결을 끝냈음을 명시한다.
      ws?.close(1000, 'component disposed');
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
        <span
          className={`ml-auto text-[11px] ${
            connectionState === 'connected'
              ? 'text-emerald-400'
              : connectionState === 'reconnecting'
                ? 'text-amber-400'
                : connectionState === 'error'
                  ? 'text-red-400'
                  : 'text-slate-500'
          }`}
        >
          {connectionState === 'connected'
            ? '연결됨'
            : connectionState === 'reconnecting'
              ? '재연결 중…'
              : connectionState === 'error'
                ? '연결 오류'
                : '연결 중…'}
        </span>
      </div>
      <div ref={containerRef} className={`${heightClass} w-full p-2`} />
    </div>
  );
}
