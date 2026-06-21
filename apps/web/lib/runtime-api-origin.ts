export interface BrowserLocation {
  protocol: string;
  hostname: string;
}

const LOOPBACK_HOSTS = new Set(['localhost', '127.0.0.1', '::1']);

export function resolveWebSocketOrigin(
  explicitOrigin: string | undefined,
  location: BrowserLocation,
): string {
  if (explicitOrigin?.trim()) return explicitOrigin.replace(/\/+$/, '');
  if (LOOPBACK_HOSTS.has(location.hostname)) return 'ws://localhost:8080';

  const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const hostname = location.hostname.startsWith('app.')
    ? `api.${location.hostname.slice(4)}`
    : location.hostname;
  return `${protocol}//${hostname}`;
}

export function browserWebSocketOrigin(): string {
  const location =
    typeof window === 'undefined' ? { protocol: 'http:', hostname: 'localhost' } : window.location;
  return resolveWebSocketOrigin(process.env.NEXT_PUBLIC_WS_URL, location);
}

export function apiHttpOrigin(webSocketOrigin = browserWebSocketOrigin()): string {
  return webSocketOrigin.replace(/^wss:/, 'https:').replace(/^ws:/, 'http:');
}

export function reconnectDelayMs(attempt: number): number {
  return Math.min(1000 * 2 ** attempt, 10_000);
}

export function shouldReconnect(disposed: boolean, closeCode: number): boolean {
  return !disposed && closeCode !== 1000;
}
