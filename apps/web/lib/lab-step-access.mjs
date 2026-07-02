/**
 * 학습 단계는 이전 단계 통과 여부를 기준으로 순차 접근만 허용한다.
 * passed 단계는 복습 가능하고, 첫 미통과 단계(active/validating/failed/pending)까지만 열 수 있다.
 *
 * @param {{ id: number }[]} steps
 * @param {(id: number) => string} statusOf
 * @returns {number}
 */
export function firstSelectableFutureIndex(steps, statusOf) {
  if (steps.length === 0) return -1;

  const firstUnpassed = steps.findIndex((step) => statusOf(step.id) !== 'passed');
  return firstUnpassed === -1 ? steps.length - 1 : firstUnpassed;
}

/**
 * 특정 step이 현재 학습자가 열 수 있는 범위 안에 있는지 확인한다.
 *
 * @param {{ id: number }[]} steps
 * @param {number} stepId
 * @param {(id: number) => string} statusOf
 * @returns {boolean}
 */
export function isStepSelectable(steps, stepId, statusOf) {
  const targetIndex = steps.findIndex((step) => step.id === stepId);
  if (targetIndex === -1) return false;

  return targetIndex <= firstSelectableFutureIndex(steps, statusOf);
}
