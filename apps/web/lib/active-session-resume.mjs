/**
 * 진행 중인 세션의 Lab으로 이동하면서 resume 대상만 전달한다.
 * 세션 소유권과 Lab 일치 여부는 대상 페이지가 API를 통해 다시 검증한다.
 *
 * @param {string} labId
 * @param {string} sessionId
 * @returns {string}
 */
export function buildActiveSessionResumeHref(labId, sessionId) {
  return `/labs/${encodeURIComponent(labId)}?resume=${encodeURIComponent(sessionId)}`;
}

/**
 * Lab 상세 URL에서 이어갈 세션 ID를 읽는다.
 * 누락되거나 공백뿐인 값은 resume 요청으로 취급하지 않는다.
 *
 * @param {{ get(name: string): string | null }} searchParams
 * @returns {string | null}
 */
export function readActiveSessionResumeId(searchParams) {
  return searchParams.get('resume')?.trim() || null;
}

/**
 * 조회한 세션이 현재 Lab에서 이어갈 수 있는지 판정한다.
 *
 * @param {string} currentLabId
 * @param {{ id: string, lab_id: string }} session
 * @returns {{ status: 'resume', sessionId: string } | { status: 'lab_mismatch' }}
 */
export function resolveActiveSessionResume(currentLabId, session) {
  if (session.lab_id !== currentLabId) {
    return { status: 'lab_mismatch' };
  }
  return { status: 'resume', sessionId: session.id };
}
