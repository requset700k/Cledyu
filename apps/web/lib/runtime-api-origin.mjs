const LOOPBACK_HOSTS = new Set(['localhost', '127.0.0.1', '::1']);

/**
 * @param {string | undefined} explicitOrigin
 * @param {{ protocol: string, hostname: string }} location
 * @returns {string}
 */
export function resolveWebSocketOrigin(explicitOrigin, location) {
  if (explicitOrigin?.trim()) return explicitOrigin.replace(/\/+$/, '');
  if (LOOPBACK_HOSTS.has(location.hostname)) return 'ws://localhost:8080';

  const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const hostname = location.hostname.startsWith('app.')
    ? `api.${location.hostname.slice(4)}`
    : location.hostname;
  return `${protocol}//${hostname}`;
}

/** @returns {string} */
export function browserWebSocketOrigin() {
  const location =
    typeof window === 'undefined' ? { protocol: 'http:', hostname: 'localhost' } : window.location;
  return resolveWebSocketOrigin(process.env.NEXT_PUBLIC_WS_URL, location);
}

/** @param {string} [webSocketOrigin] @returns {string} */
export function apiHttpOrigin(webSocketOrigin = browserWebSocketOrigin()) {
  return webSocketOrigin.replace(/^wss:/, 'https:').replace(/^ws:/, 'http:');
}

/** @param {number} attempt @returns {number} */
export function reconnectDelayMs(attempt) {
  return Math.min(1000 * 2 ** attempt, 10_000);
}

/** @param {boolean} disposed @param {number} closeCode @returns {boolean} */
export function shouldReconnect(disposed, closeCode) {
  return !disposed && closeCode !== 1000;
}
