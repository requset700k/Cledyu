// VM login shell이 준비됐음을 알리는 내부 신호다. 사용자가 읽는 prompt 문자열(Cledyu ~ ➜)과
// readiness 계약을 분리해, 프롬프트 디자인이 바뀌어도 부팅 화면 전환 조건이 깨지지 않게 한다.
export const TERMINAL_READY_SENTINEL = 'CledyuTerminalReady=1';

// Enter는 빈 명령 실행·login prompt 입력으로 해석될 수 있다. Ctrl+L은 bash/readline에서 화면을
// 지우고 현재 prompt를 다시 그리는 입력이라, 연결 직후 prompt redraw 용도로 더 안전하다.
export const TERMINAL_READY_REDRAW = '\x0c';

/**
 * serial console 초반 출력에서 readiness sentinel이 나오기 전까지의 부팅 로그를 숨긴다.
 * sentinel은 chunk 경계에서 잘릴 수 있으므로 마지막 일부 문자열을 carry로 보존한다.
 *
 * @param {{ enabled?: boolean }} [options]
 * @returns {{ consume: (chunk: string) => { ready: boolean, becameReady: boolean, output: string }, isReady: () => boolean }}
 */
export function createTerminalReadinessGate(options = {}) {
  const enabled = options.enabled !== false;
  let ready = !enabled;
  let carry = '';

  return {
    consume(chunk) {
      const text = String(chunk ?? '');
      if (ready) return { ready: true, becameReady: false, output: text };

      const combined = carry + text;
      if (!combined.includes(TERMINAL_READY_SENTINEL)) {
        carry = combined.slice(-TERMINAL_READY_SENTINEL.length + 1);
        return { ready: false, becameReady: false, output: '' };
      }

      ready = true;
      carry = '';
      // readiness 이전 출력은 cloud-init/login noise일 수 있으므로 버린다. 이후 prompt는 Ctrl+L로 다시 그린다.
      return { ready: true, becameReady: true, output: '' };
    },
    isReady() {
      return ready;
    },
  };
}
