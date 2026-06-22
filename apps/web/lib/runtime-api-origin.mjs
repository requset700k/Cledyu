// 브라우저에서 Session API로 직접 연결할 때 사용할 origin과 재연결 정책을 모은다.
// 터미널(WebSocket)과 IDE(HTTP)가 서로 다른 주소 계산식을 쓰면 환경별로 한쪽만 깨질 수 있어
// 이 파일을 두 컴포넌트의 단일 진실 공급원으로 사용한다.

// localhost는 운영의 app.* → api.* 변환 대상이 아니다. Web을 로컬에서 3000 포트로 띄울 때
// API는 별도 Go 서버의 8080 포트를 사용하므로 IPv4·IPv6 loopback을 모두 명시한다.
const LOOPBACK_HOSTS = new Set(['localhost', '127.0.0.1', '::1']);
const STABLE_CONNECTION_MS = 30_000;
const AUTH_REFRESH_RETRY_MS = 60_000;

/**
 * 명시적 설정 또는 현재 페이지 주소에서 터미널 WebSocket origin을 계산한다.
 * 우선순위: NEXT_PUBLIC_WS_URL 같은 명시값 → 로컬 개발 주소 → 운영 app.*의 api.* 변환.
 * 경로는 호출부가 session별 terminalPath를 붙이므로 여기서는 origin만 반환한다.
 *
 * @param {string | undefined} explicitOrigin
 * @param {{ protocol: string, hostname: string }} location
 * @returns {string}
 */
export function resolveWebSocketOrigin(explicitOrigin, location) {
  if (explicitOrigin?.trim()) return explicitOrigin.replace(/\/+$/, '');
  if (LOOPBACK_HOSTS.has(location.hostname)) return 'ws://localhost:8080';

  // HTTPS 페이지에서 비보안 ws://를 열면 mixed content로 차단되므로 wss://를 사용한다.
  const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
  // 현재 배포 규칙(app.cledyu.local ↔ api.cledyu.local)에 맞춰 API hostname을 유도한다.
  // app.* 패턴이 아닌 환경은 같은 hostname을 유지해 preview/custom domain을 깨뜨리지 않는다.
  const hostname = location.hostname.startsWith('app.')
    ? `api.${location.hostname.slice(4)}`
    : location.hostname;
  return `${protocol}//${hostname}`;
}

/**
 * 실제 브라우저 주소를 사용하되, Next.js SSR처럼 window가 없는 단계에서는 안전한 로컬값을 쓴다.
 * 이 fallback은 서버에서 네트워크 연결을 만들기 위한 값이 아니라 hydration 중 ReferenceError 방지용이다.
 * @returns {string}
 */
export function browserWebSocketOrigin() {
  const location =
    typeof window === 'undefined' ? { protocol: 'http:', hostname: 'localhost' } : window.location;
  return resolveWebSocketOrigin(process.env.NEXT_PUBLIC_WS_URL, location);
}

/**
 * 터미널과 같은 API host를 IDE health check·iframe에서도 사용하도록 HTTP origin으로 변환한다.
 * @param {string} [webSocketOrigin]
 * @returns {string}
 */
export function apiHttpOrigin(webSocketOrigin = browserWebSocketOrigin()) {
  return webSocketOrigin.replace(/^wss:/, 'https:').replace(/^ws:/, 'http:');
}

/**
 * 반복 장애 때 서버를 과도하게 두드리지 않도록 1초부터 지수 증가시키고 10초로 제한한다.
 * @param {number} attempt 0부터 시작하는 연속 재연결 횟수
 * @returns {number} 다음 연결까지 기다릴 밀리초
 */
export function reconnectDelayMs(attempt) {
  return Math.min(1000 * 2 ** attempt, 10_000);
}

/**
 * 화면 이동·정상 종료는 의도된 연결 해제이므로 재연결하지 않고, 비정상 종료만 복구한다.
 * @param {boolean} disposed React cleanup으로 이미 컴포넌트가 제거됐는지 여부
 * @param {number | undefined} closeCode WebSocket close code(1000은 정상 종료)
 * @returns {boolean}
 */
export function shouldReconnect(disposed, closeCode) {
  return !disposed && typeof closeCode === 'number' && closeCode !== 1000;
}

/**
 * API의 SerialConsole 연결 timeout(10초)보다 충분히 오래 유지된 socket만 안정 연결로 본다.
 * upgrade 직후 KubeVirt 연결이 실패한 socket은 backoff 횟수를 초기화하면 안 된다.
 *
 * @param {number | null} openedAt WebSocket open 시각
 * @param {number} closedAt WebSocket close 시각
 * @returns {boolean}
 */
export function connectionWasStable(openedAt, closedAt) {
  return openedAt !== null && closedAt - openedAt >= STABLE_CONNECTION_MS;
}

/**
 * 실패한 refresh를 매 WebSocket retry마다 반복하지 않고 1분 간격으로 제한한다.
 *
 * @param {number | null} lastAttemptAt 마지막 refresh 시도 시각
 * @param {number} now 현재 시각
 * @returns {boolean}
 */
export function shouldAttemptAuthRefresh(lastAttemptAt, now) {
  return lastAttemptAt === null || now - lastAttemptAt >= AUTH_REFRESH_RETRY_MS;
}
