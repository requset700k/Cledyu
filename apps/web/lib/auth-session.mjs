// HTTP API 요청과 WebSocket 재연결이 같은 refresh 요청을 공유한다.
// Keycloak은 refresh token rotation을 사용하므로 동시 호출을 하나로 합쳐야 한다.
let refreshInFlight = null;

/**
 * refresh_token 쿠키로 access_token을 갱신한다.
 * 네트워크 오류와 만료된 refresh token은 false로 반환하고 호출부가 후속 동작을 결정한다.
 *
 * @param {typeof fetch} [fetchImpl]
 * @returns {Promise<boolean>}
 */
export function refreshSession(fetchImpl = globalThis.fetch) {
  refreshInFlight ??= fetchImpl('/api/v1/auth/refresh', {
    method: 'POST',
    credentials: 'include',
  })
    .then((response) => response.ok)
    .catch(() => false)
    .finally(() => {
      refreshInFlight = null;
    });
  return refreshInFlight;
}
