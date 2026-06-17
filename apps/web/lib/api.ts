// 백엔드 API 클라이언트.
// 모든 HTTP 요청은 Next.js route handler를 통해 /api/* → BACKEND_URL/api/*로 프록시됨.
// WebSocket은 rewrite 대상이 아니므로 Terminal 컴포넌트에서 NEXT_PUBLIC_WS_URL로 직접 연결.

import type { HintResponse, Lab, Session, StepProgress, User } from './types';

interface Paginated<T> {
  items: T[];
  total: number;
}

interface ApiErrorPayload {
  error?: string;
  code?: string;
  session_id?: string;
  [key: string]: unknown;
}

// 백엔드가 내려주는 code/session_id 같은 구조화된 에러 필드를 보존한다.
// Lab 시작 409(session_exists)처럼 UI가 후속 처리를 해야 하는 응답에서 사용한다.
export class ApiRequestError extends Error {
  readonly status: number;
  readonly code?: string;
  readonly sessionId?: string;
  readonly payload: ApiErrorPayload;

  constructor(
    status: number,
    payload: ApiErrorPayload,
    fallbackMessage: string,
    preferPayloadMessage = true,
  ) {
    super(preferPayloadMessage ? (payload.error ?? fallbackMessage) : fallbackMessage);
    this.name = 'ApiRequestError';
    this.status = status;
    this.code = payload.code;
    this.sessionId = payload.session_id;
    this.payload = payload;
  }
}

// 개발 모드에서는 Keycloak 대신 dev-token으로 백엔드 stub JWT 미들웨어를 통과
const DEV_HEADERS: Record<string, string> =
  process.env.NODE_ENV === 'development' ? { Authorization: 'Bearer dev-token' } : {};

// silent refresh — 401 을 받으면 refresh_token 쿠키로 access_token 을 갱신한 뒤 1회 재시도.
// 동시 다발 401(폴링 + 사용자 액션)은 같은 refresh 호출을 공유한다(single-flight).
let refreshInFlight: Promise<boolean> | null = null;

function refreshSession(): Promise<boolean> {
  refreshInFlight ??= fetch('/api/v1/auth/refresh', { method: 'POST', credentials: 'include' })
    .then((r) => r.ok)
    .catch(() => false)
    .finally(() => {
      refreshInFlight = null;
    });
  return refreshInFlight;
}

async function readErrorPayload(res: Response): Promise<ApiErrorPayload> {
  return res.json().catch(() => ({ error: res.statusText }));
}

/** 모든 API 요청의 공통 래퍼. 에러 응답은 백엔드의 { error: string } 포맷으로 throw. */
async function request<T>(path: string, options?: RequestInit, retried = false): Promise<T> {
  const headers: Record<string, string> = {
    ...DEV_HEADERS,
    ...(options?.headers as Record<string, string>),
  };
  // body가 있는 요청(POST/PUT/PATCH)에만 Content-Type 설정. GET에 불필요한 헤더 제거.
  if (options?.body) {
    headers['Content-Type'] = 'application/json';
  }

  let res: Response;
  try {
    res = await fetch(path, {
      ...options,
      credentials: 'include', // Keycloak 쿠키(access_token) 자동 포함
      headers,
    });
  } catch {
    // 서버 다운 또는 네트워크 끊김
    throw new Error('NETWORK_ERROR');
  }

  // access_token 만료 → silent refresh 후 원 요청 1회 재시도.
  // refresh 실패(SSO 세션 종료/refresh_token 만료)면 로그인 페이지로 강제 이동.
  if (res.status === 401) {
    if (!retried && (await refreshSession())) {
      return request<T>(path, options, true);
    }
    if (typeof window !== 'undefined') {
      window.location.href = `/login?from=${encodeURIComponent(window.location.pathname)}`;
    }
    return new Promise<never>(() => {});
  }

  // 인증은 됐지만 역할 권한 없음 (student → instructor 엔드포인트 등)
  if (res.status === 403) {
    throw new ApiRequestError(res.status, await readErrorPayload(res), 'FORBIDDEN', false);
  }

  // 존재하지 않는 리소스 (Lab ID, Session ID 등)
  if (res.status === 404) {
    throw new ApiRequestError(res.status, await readErrorPayload(res), 'NOT_FOUND', false);
  }

  // 500/502/503/504: 서버 측 문제 — 클라이언트 에러(4xx)와 구분해서 UI 처리
  if (res.status >= 500) {
    throw new ApiRequestError(res.status, await readErrorPayload(res), 'SERVER_ERROR', false);
  }

  if (!res.ok) {
    throw new ApiRequestError(res.status, await readErrorPayload(res), 'Request failed');
  }

  // 204 No Content: DELETE 등 응답 바디 없는 경우. T=void 호출 전용.
  if (res.status === 204) return undefined as unknown as T;
  return res.json() as T;
}

export const api = {
  labs: {
    list: () => request<Paginated<Lab>>('/api/v1/labs'),
    get: (id: string) => request<Lab>(`/api/v1/labs/${id}`),
  },

  sessions: {
    create: (labId: string) =>
      request<Session>('/api/v1/sessions', {
        method: 'POST',
        body: JSON.stringify({ lab_id: labId }),
      }),
    get: (id: string) => request<Session>(`/api/v1/sessions/${id}`),
    delete: (id: string) => request<void>(`/api/v1/sessions/${id}`, { method: 'DELETE' }),
    steps: (id: string) => request<Paginated<StepProgress>>(`/api/v1/sessions/${id}/steps`),
    validate: (id: string, stepId: number) =>
      request<{ status: string; message: string }>(`/api/v1/sessions/${id}/validate`, {
        method: 'POST',
        body: JSON.stringify({ step_id: stepId }),
      }),
    // AI 학습 도우미 힌트. level 미지정 시 백엔드가 1→2→3 으로 자동 상승시킨다.
    hint: (id: string, stepId: number, level?: number) =>
      request<HintResponse>(`/api/v1/sessions/${id}/hint`, {
        method: 'POST',
        body: JSON.stringify({ step_id: stepId, ...(level ? { level } : {}) }),
      }),
  },

  auth: {
    me: () => request<User>('/api/v1/me'),
    // 로그아웃은 전체 페이지 이동으로 처리한다 — 백엔드가 세션 쿠키를 지우고
    // Keycloak end-session(SSO 로그아웃)을 거쳐 프론트로 되돌려보낸다. fetch로는
    // 크로스 오리진 리다이렉트(Keycloak)를 의미있게 따라갈 수 없다.
    logout: () => {
      if (typeof window !== 'undefined') {
        window.location.href = '/api/v1/auth/logout';
      }
    },
  },
};
