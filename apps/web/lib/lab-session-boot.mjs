/**
 * 세션 상태와 ready 이후 grace 상태로 준비 화면 노출 여부를 계산한다.
 * resume 검증 시 이미 ready/active였던 세션만 grace를 생략한다.
 *
 * @param {string | undefined} status
 * @param {number | null} graceStartedAt
 * @param {number} now
 * @param {number} graceMs
 * @param {boolean} skipReadyGrace
 * @returns {boolean}
 */
export function shouldShowSessionBoot(status, graceStartedAt, now, graceMs, skipReadyGrace) {
  if (!status || status === 'provisioning') return true;
  if (status !== 'ready' && status !== 'active') return false;
  if (skipReadyGrace) return false;
  if (graceStartedAt === null) return true;
  return now - graceStartedAt < graceMs;
}
