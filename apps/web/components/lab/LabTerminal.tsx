'use client';

import { useEffect, useRef, useState } from 'react';
import '@xterm/xterm/css/xterm.css';
import {
  browserWebSocketOrigin,
  connectionWasStable,
  reconnectDelayMs,
  shouldAttemptAuthRefresh,
  shouldReconnect,
} from '@/lib/runtime-api-origin.mjs';
import { refreshSession } from '@/lib/auth-session.mjs';
import {
  createTerminalReadinessGate,
  TERMINAL_READY_REDRAW,
} from '@/lib/terminal-readiness.mjs';

type ConnectionState = 'connecting' | 'connected' | 'reconnecting' | 'error';

// 리사이즈 제어 프로토콜 지원을 알리는 WS subprotocol. service-web/service-api 는 순서 보장 없는 별도
// ArgoCD 앱이라 배포 스큐가 생기므로, 서버가 이 subprotocol 을 echo 한 경우(=새 API)에만 resize 제어
// 프레임을 보낸다. 옛 API 와 붙으면 echo 가 없어 제어 프레임을 보내지 않아 셸 입력 주입을 막는다.
const TERMINAL_SUBPROTOCOL_V2 = 'cledyu-term-v2';

// LabTerminal은 lab VM의 serial console에 연결된 실시간 xterm.js 터미널이다.
// WebSocket은 Next HTTP route handler의 프록시 대상이 아니므로 Go API에 직접 연결한다.
// 운영/로컬 API origin은 IDE와 공유하고, 터미널 backoff 정책도 runtime-api-origin.mjs에서 관리한다.
// xterm은 DOM/WebSocket에 의존하므로 모든 초기화를 useEffect(클라이언트) 안에서 동적 import한다.
// 연결이 끊겨도 xterm 인스턴스와 출력은 유지하고 WebSocket만 교체해 학습 흐름을 보존한다.
// heightClass: 좌(문제)/우(터미널) 2분할 레이아웃이 화면 높이에 맞춰 키울 때 사용.
export function LabTerminal({
  terminalPath,
  heightClass = 'h-80',
  onOutput,
  redrawOnConnect = false,
}: {
  terminalPath: string;
  heightClass?: string;
  onOutput?: (chunk: string) => void;
  redrawOnConnect?: boolean;
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
    // 연속 실패 횟수. 30초 이상 안정적으로 유지된 연결만 0으로 초기화한다.
    let retryAttempt = 0;
    // 같은 장애 구간에서 refresh token을 반복 회전하지 않되 실패 시 1분 뒤 다시 시도한다.
    let authRefreshedForOutage = false;
    let lastAuthRefreshAttemptAt: number | null = null;
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
      const decoder = new TextDecoder();
      const encoder = new TextEncoder();

      // serial 콘솔처럼 백엔드가 권위 크기를 통보하면 fit 을 끄고 그 크기를 고정한다(serial 은 winsize 채널 없음).
      let serverPinnedSize = false;
      // 서버가 subprotocol 을 echo 한 경우(=새 API)에만 true. 옛 API 와의 스큐에서 resize 주입을 막는다.
      let serverSupportsResize = false;

      // 현재 xterm 크기를 백엔드에 통보한다. EC2 PTY 는 이 값으로 SSH window-change 를 적용한다.
      const sendResize = () => {
        if (ws?.readyState === WebSocket.OPEN) {
          ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
        }
      };

      // 백엔드가 보낸 TextMessage 가 리사이즈 제어 프레임이면 적용하고 true. 아니면(에러 텍스트 등) false.
      const applyControlFrame = (text: string): boolean => {
        if (text.charCodeAt(0) !== 0x7b /* '{' */) return false;
        try {
          const msg = JSON.parse(text);
          if (
            msg?.type === 'resize' &&
            typeof msg.cols === 'number' &&
            typeof msg.rows === 'number'
          ) {
            serverPinnedSize = true;
            term.resize(Math.max(1, msg.cols), Math.max(1, msg.rows));
            return true;
          }
        } catch {
          // JSON 이 아니면 일반 서버 텍스트(연결 실패 메시지 등)이므로 그대로 출력하게 둔다.
        }
        return false;
      };

      // 재연결 가능한 종료를 영구 실패로 표시하지 않고 exponential backoff 후 다시 연결한다.
      // retryTimer guard가 error/close 중복 callback에 의한 동시 socket 생성을 방지한다.
      const scheduleReconnect = () => {
        if (disposed || retryTimer) return;
        setConnectionState('reconnecting');
        retryTimer = setTimeout(() => {
          void (async () => {
            const now = Date.now();
            if (
              process.env.NODE_ENV !== 'development' &&
              !authRefreshedForOutage &&
              shouldAttemptAuthRefresh(lastAuthRefreshAttemptAt, now)
            ) {
              lastAuthRefreshAttemptAt = now;
              authRefreshedForOutage = await refreshSession();
            }
            if (disposed) {
              retryTimer = null;
              return;
            }
            retryAttempt += 1;
            retryTimer = null;
            connect();
          })();
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
          // v2 리사이즈 프로토콜을 요청한다. 옛 API 는 echo 하지 않아 socket.protocol 이 빈 값이 된다.
          socket = new WebSocket(url, [TERMINAL_SUBPROTOCOL_V2]);
        } catch {
          // 잘못된 URL 같은 생성 단계 오류는 같은 값으로 재시도해도 복구되지 않으므로 오류를 유지한다.
          setConnectionState('error');
          return;
        }

        ws = socket;
        socket.binaryType = 'arraybuffer';
        let openedAt: number | null = null;
        socket.onopen = () => {
          // HTTP upgrade 성공만으로 KubeVirt SerialConsole 연결 성공을 보장할 수 없다.
          openedAt = Date.now();
          setConnectionState('connected');
          term.focus();
          // 서버가 v2 subprotocol 을 echo 했을 때만(=새 API) 크기를 통보한다. 옛 API 면 보내지 않아
          // 제어 JSON 이 셸 입력으로 주입되는 배포 스큐 버그를 막는다. 새 PTY(EC2)는 이 통보로 초기
          // 고정 크기를 실제 xterm 크기로 교정한다.
          serverSupportsResize = socket.protocol === TERMINAL_SUBPROTOCOL_V2;
          if (serverSupportsResize) sendResize();
          if (redrawOnConnect) {
            socket.send(encoder.encode(TERMINAL_READY_REDRAW));
          }
        };
        socket.onmessage = (event) => {
          // 문자열 프레임 = 제어(리사이즈) 또는 서버 텍스트. 바이너리 프레임 = VM 출력.
          if (typeof event.data === 'string') {
            if (applyControlFrame(event.data)) return;
            term.write(event.data);
            if (onOutput && event.data) onOutput(event.data);
            return;
          }
          const bytes = new Uint8Array(event.data);
          term.write(bytes);
          if (onOutput) {
            const chunk = decoder.decode(bytes, { stream: true });
            if (chunk) onOutput(chunk);
          }
        };
        socket.onerror = () => {
          // 브라우저는 error에 상세 원인을 노출하지 않는다. close callback에서 code를 받아 재연결한다.
          setConnectionState('error');
          socket.close();
        };
        socket.onclose = (event) => {
          // 오래된 socket callback이 새 활성 socket을 null로 덮어쓰지 않게 동일 객체일 때만 비운다.
          if (ws === socket) ws = null;
          // API의 10초 SerialConsole timeout보다 긴 30초 유지 연결만 안정 상태로 인정한다.
          if (connectionWasStable(openedAt, Date.now())) {
            retryAttempt = 0;
            authRefreshedForOutage = false;
            lastAuthRefreshAttemptAt = null;
          }
          // component dispose와 정상 종료(1000)는 의도된 종료이므로 재연결하지 않는다.
          if (shouldReconnect(disposed, event.code)) scheduleReconnect();
        };
      };

      connect();

      // 재연결 중에는 입력을 버리고, OPEN 상태인 현재 socket에만 키 입력을 전달한다.
      // 키 입력은 BinaryMessage 로 보낸다 — 백엔드가 TextMessage(JSON 리사이즈 제어)와 구분하기 위함이다.
      term.onData((d) => {
        if (ws?.readyState === WebSocket.OPEN) ws.send(encoder.encode(d));
      });

      // 창 크기가 바뀌면 다시 fit 하고 새 크기를 백엔드에 통보한다. 단, 백엔드가 권위 크기를 고정한
      // serial 콘솔에서는 fit 을 끄고 통보된 크기를 유지한다(serial 은 동적 리사이즈 불가).
      const onResize = () => {
        if (serverPinnedSize) return;
        fit.fit();
        // 새 API 와 연결됐을 때만 새 크기를 통보한다(옛 API 면 로컬 fit 만, 제어 프레임 미전송).
        if (serverSupportsResize) sendResize();
      };
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
  }, [terminalPath, onOutput, redrawOnConnect]);

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

// SessionBoot 이 화면을 가리고 있는 동안 serial console에 먼저 붙어 VM 내부 login shell 준비 여부만 확인한다.
// sentinel을 잡으면 boot grace를 즉시 종료하고, 실제 화면 터미널은 새 연결에서 prompt redraw만 다시 수행한다.
export function TerminalReadinessProbe({
  terminalPath,
  onReady,
}: {
  terminalPath: string;
  onReady: () => void;
}) {
  useEffect(() => {
    let disposed = false;
    let readyTimer: ReturnType<typeof setTimeout> | null = null;
    const encoder = new TextEncoder();
    const decoder = new TextDecoder();
    const readinessGate = createTerminalReadinessGate();
    const base = browserWebSocketOrigin();
    const token = process.env.NODE_ENV === 'development' ? 'dev-token' : '';
    const url = `${base}${terminalPath}${token ? `?token=${token}` : ''}`;
    let socket: WebSocket;
    try {
      socket = new WebSocket(url, [TERMINAL_SUBPROTOCOL_V2]);
    } catch {
      return undefined;
    }

    socket.binaryType = 'arraybuffer';
    socket.onopen = () => {
      socket.send(encoder.encode(TERMINAL_READY_REDRAW));
    };
    socket.onmessage = (event) => {
      if (disposed) return;
      if (typeof event.data === 'string') {
        readinessGate.consume(event.data);
      } else {
        readinessGate.consume(decoder.decode(new Uint8Array(event.data), { stream: true }));
      }
      if (readinessGate.isReady() && !readyTimer) {
        socket.send(encoder.encode(TERMINAL_READY_REDRAW));
        readyTimer = setTimeout(() => {
          if (disposed) return;
          onReady();
          socket.close(1000, 'terminal ready');
        }, 100);
      }
    };

    return () => {
      disposed = true;
      if (readyTimer) clearTimeout(readyTimer);
      socket.close(1000, 'probe disposed');
    };
  }, [terminalPath, onReady]);

  return null;
}
