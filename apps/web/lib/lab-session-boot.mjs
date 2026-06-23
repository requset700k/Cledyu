/**
 * effect가 정리된 뒤 다시 setup되어도 같은 grace 시작 시각을 유지하며 남은 시간을 계산한다.
 * React 개발 모드의 setup → cleanup → setup 순서에서도 두 번째 setup이 타이머를 복구해야 한다.
 *
 * @param {number | null} startedAt
 * @param {number} now
 * @param {number} graceMs
 * @returns {{ startedAt: number, remainingMs: number }}
 */
export function bootGraceSchedule(startedAt, now, graceMs) {
  const resolvedStartedAt = startedAt ?? now;
  return {
    startedAt: resolvedStartedAt,
    remainingMs: Math.max(0, graceMs - (now - resolvedStartedAt)),
  };
}

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
