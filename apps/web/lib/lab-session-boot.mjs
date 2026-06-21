/**
 * 세션 상태와 ready 이후 grace 시작 시각으로 준비 화면 노출 여부를 계산한다.
 * ready/active 첫 렌더에서 graceStartedAt이 아직 null이어도 booting으로 취급해
 * 실습 화면이 먼저 노출됐다가 준비 화면으로 돌아오는 전환을 막는다.
 *
 * @param {string | undefined} status
 * @param {number | null} graceStartedAt
 * @param {number} now
 * @param {number} graceMs
 * @returns {boolean}
 */
export function shouldShowSessionBoot(status, graceStartedAt, now, graceMs) {
  if (!status || status === 'provisioning') return true;
  if (status !== 'ready' && status !== 'active') return false;
  if (graceStartedAt === null) return true;
  return now - graceStartedAt < graceMs;
}
