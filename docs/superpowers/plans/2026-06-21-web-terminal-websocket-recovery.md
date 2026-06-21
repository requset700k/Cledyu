# Web Terminal WebSocket Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 운영 Web이 현재 브라우저 주소에서 올바른 API WebSocket origin을 계산하고, 일시적인 연결 종료 후 기존 터미널 화면을 유지하며 자동 재연결하게 한다.

**Architecture:** URL 및 재연결 정책을 `apps/web/lib/runtime-api-origin.ts`의 순수 함수로 분리해 Vitest로 검증한다. `LabTerminal`은 하나의 xterm 인스턴스를 유지하면서 WebSocket과 재시도 timer만 교체하고, `LabWorkspace`는 같은 origin 함수로 IDE HTTP 주소를 계산한다.

**Tech Stack:** Next.js 15, React 18, TypeScript, xterm.js, browser WebSocket, Vitest

---

## 파일 구조

- Create: `apps/web/lib/runtime-api-origin.ts`
  - WebSocket/API HTTP origin 결정과 재연결 backoff 정책을 제공한다.
- Create: `apps/web/lib/runtime-api-origin.test.ts`
  - 운영 domain, localhost, 명시적 override, 프로토콜 변환, backoff를 검증한다.
- Modify: `apps/web/components/lab/LabTerminal.tsx`
  - 공통 URL 함수를 사용하고 WebSocket 재연결 lifecycle을 관리한다.
- Modify: `apps/web/components/lab/LabWorkspace.tsx`
  - 중복된 API origin 계산을 공통 함수로 교체한다.
- Modify: `apps/web/package.json`
  - Vitest test script와 dev dependency를 추가한다.
- Modify: `apps/web/pnpm-lock.yaml`
  - Vitest dependency lock을 반영한다.

### Task 1: URL 및 backoff 정책을 테스트 우선으로 구현

**Files:**
- Create: `apps/web/lib/runtime-api-origin.test.ts`
- Create: `apps/web/lib/runtime-api-origin.ts`
- Modify: `apps/web/package.json`
- Modify: `apps/web/pnpm-lock.yaml`

- [ ] **Step 1: Vitest를 개발 의존성으로 추가하고 test script를 등록**

Run:

```bash
cd apps/web
pnpm add -D vitest
```

`package.json` scripts에 다음을 추가한다.

```json
"test": "vitest run"
```

- [ ] **Step 2: URL 및 backoff 실패 테스트 작성**

`apps/web/lib/runtime-api-origin.test.ts`:

```ts
import { describe, expect, it } from 'vitest';
import {
  apiHttpOrigin,
  reconnectDelayMs,
  resolveWebSocketOrigin,
  shouldReconnect,
} from './runtime-api-origin';

describe('resolveWebSocketOrigin', () => {
  it('prefers an explicit build-time override', () => {
    expect(
      resolveWebSocketOrigin('wss://terminal.example.com/', {
        protocol: 'https:',
        hostname: 'app.cledyu.local',
      }),
    ).toBe('wss://terminal.example.com');
  });

  it('maps the production app host to the api host', () => {
    expect(
      resolveWebSocketOrigin(undefined, {
        protocol: 'https:',
        hostname: 'app.cledyu.local',
      }),
    ).toBe('wss://api.cledyu.local');
  });

  it('keeps local development on localhost port 8080', () => {
    expect(
      resolveWebSocketOrigin(undefined, {
        protocol: 'http:',
        hostname: 'localhost',
      }),
    ).toBe('ws://localhost:8080');
  });
});

describe('apiHttpOrigin', () => {
  it('converts secure websocket origins to https', () => {
    expect(apiHttpOrigin('wss://api.cledyu.local')).toBe('https://api.cledyu.local');
  });
});

describe('reconnect policy', () => {
  it('uses exponential backoff capped at ten seconds', () => {
    expect([0, 1, 2, 3, 4, 5].map(reconnectDelayMs)).toEqual([
      1000, 2000, 4000, 8000, 10000, 10000,
    ]);
  });

  it('does not reconnect after disposal or a normal close', () => {
    expect(shouldReconnect(true, 1006)).toBe(false);
    expect(shouldReconnect(false, 1000)).toBe(false);
    expect(shouldReconnect(false, 1006)).toBe(true);
  });
});
```

- [ ] **Step 3: 테스트가 구현 부재로 실패하는지 확인**

Run:

```bash
cd apps/web
pnpm test
```

Expected: `runtime-api-origin` module 또는 export를 찾지 못해 FAIL.

- [ ] **Step 4: 순수 함수 최소 구현**

`apps/web/lib/runtime-api-origin.ts`:

```ts
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
  return resolveWebSocketOrigin(process.env.NEXT_PUBLIC_WS_URL, window.location);
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
```

- [ ] **Step 5: focused test 통과 확인**

Run:

```bash
cd apps/web
pnpm test
```

Expected: 모든 `runtime-api-origin` 테스트 PASS.

- [ ] **Step 6: 정책 구현 커밋**

```bash
git add apps/web/package.json apps/web/pnpm-lock.yaml \
  apps/web/lib/runtime-api-origin.ts apps/web/lib/runtime-api-origin.test.ts
git commit -m "fix(web): resolve runtime api websocket origin"
```

### Task 2: LabTerminal 자동 재연결 구현

**Files:**
- Modify: `apps/web/components/lab/LabTerminal.tsx`

- [ ] **Step 1: 공통 origin 및 재연결 정책 import**

```ts
import {
  browserWebSocketOrigin,
  reconnectDelayMs,
  shouldReconnect,
} from '@/lib/runtime-api-origin';
```

연결 상태 타입을 다음으로 변경한다.

```ts
type ConnectionState = 'connecting' | 'connected' | 'reconnecting' | 'error';
```

- [ ] **Step 2: xterm을 유지하는 연결 함수 구현**

`useEffect` 내부에서 다음 lifecycle을 구현한다.

```ts
let disposed = false;
let ws: WebSocket | null = null;
let retryTimer: ReturnType<typeof setTimeout> | null = null;
let retryAttempt = 0;

const scheduleReconnect = () => {
  if (disposed || retryTimer) return;
  setConnectionState('reconnecting');
  retryTimer = setTimeout(() => {
    retryTimer = null;
    retryAttempt += 1;
    connect();
  }, reconnectDelayMs(retryAttempt));
};

const connect = () => {
  if (disposed) return;
  const base = browserWebSocketOrigin();
  const token = process.env.NODE_ENV === 'development' ? 'dev-token' : '';
  const url = `${base}${terminalPath}${token ? `?token=${token}` : ''}`;

  try {
    ws = new WebSocket(url);
  } catch {
    setConnectionState('error');
    scheduleReconnect();
    return;
  }

  ws.binaryType = 'arraybuffer';
  ws.onopen = () => {
    retryAttempt = 0;
    setConnectionState('connected');
    term.focus();
  };
  ws.onmessage = (event) => {
    term.write(
      typeof event.data === 'string' ? event.data : new Uint8Array(event.data),
    );
  };
  ws.onerror = () => setConnectionState('error');
  ws.onclose = (event) => {
    ws = null;
    if (shouldReconnect(disposed, event.code)) scheduleReconnect();
  };
};
```

- [ ] **Step 3: 입력과 cleanup을 새 lifecycle에 맞춤**

입력은 현재 열린 socket으로만 전달한다.

```ts
term.onData((data) => {
  if (ws?.readyState === WebSocket.OPEN) ws.send(data);
});
```

cleanup은 timer와 socket을 모두 해제한다.

```ts
disposed = true;
if (retryTimer) clearTimeout(retryTimer);
ws?.close(1000, 'component disposed');
dispose?.();
```

- [ ] **Step 4: 상태 문구 갱신**

```tsx
{connectionState === 'connected'
  ? '연결됨'
  : connectionState === 'reconnecting'
    ? '재연결 중…'
    : connectionState === 'error'
      ? '연결 오류'
      : '연결 중…'}
```

- [ ] **Step 5: typecheck와 lint 실행**

Run:

```bash
cd apps/web
pnpm typecheck
pnpm lint
```

Expected: exit code 0.

- [ ] **Step 6: 터미널 재연결 커밋**

```bash
git add apps/web/components/lab/LabTerminal.tsx
git commit -m "fix(web): reconnect interrupted lab terminals"
```

### Task 3: IDE origin 중복 제거 및 전체 검증

**Files:**
- Modify: `apps/web/components/lab/LabWorkspace.tsx`

- [ ] **Step 1: 로컬 apiHttpOrigin 함수 제거**

다음 import를 추가한다.

```ts
import { apiHttpOrigin } from '@/lib/runtime-api-origin';
```

`LabWorkspace.tsx` 안의 기존 `apiHttpOrigin()` 함수는 삭제하고 `IdePane`의 호출은 그대로 유지한다.

- [ ] **Step 2: 전체 Web 검증**

Run:

```bash
cd apps/web
pnpm test
pnpm typecheck
pnpm lint
pnpm build
```

Expected: 모든 명령 exit code 0.

- [ ] **Step 3: 저장소 가드레일 검증**

Run:

```bash
pre-commit run --files \
  apps/web/package.json \
  apps/web/pnpm-lock.yaml \
  apps/web/lib/runtime-api-origin.ts \
  apps/web/lib/runtime-api-origin.test.ts \
  apps/web/components/lab/LabTerminal.tsx \
  apps/web/components/lab/LabWorkspace.tsx \
  docs/superpowers/specs/2026-06-21-web-terminal-websocket-recovery-design.md \
  docs/superpowers/plans/2026-06-21-web-terminal-websocket-recovery.md

git diff --check
```

Expected: 모든 hook PASS, `git diff --check` exit code 0.

- [ ] **Step 4: IDE origin 및 검증 커밋**

```bash
git add apps/web/components/lab/LabWorkspace.tsx \
  docs/superpowers/plans/2026-06-21-web-terminal-websocket-recovery.md
git commit -m "refactor(web): share runtime api origin"
```

- [ ] **Step 5: main 대비 diff 확인**

Run:

```bash
git status --short --branch
git diff --stat origin/main...HEAD
git log --oneline origin/main..HEAD
```

Expected: WebSocket URL·재연결 관련 파일과 설계/계획 문서만 표시되고 기존 사용자 untracked 파일은 커밋되지 않는다.
