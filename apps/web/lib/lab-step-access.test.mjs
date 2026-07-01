import assert from 'node:assert/strict';
import { describe, it } from 'node:test';
import { firstSelectableFutureIndex, isStepSelectable } from './lab-step-access.mjs';

// Lab DSL의 실제 step id는 1부터 시작하지만, 접근 제어는 화면 표시 순서 기준으로 판단한다.
// 테스트에서는 id와 index가 헷갈리지 않도록 4단계 고정 fixture를 사용한다.
const steps = [
  { id: 1, title: '첫 단계' },
  { id: 2, title: '둘째 단계' },
  { id: 3, title: '셋째 단계' },
  { id: 4, title: '넷째 단계' },
];

// API가 아직 진행 상태를 만들지 않은 단계는 pending으로 취급한다.
// 이 기본값이 있어야 새 세션에서 첫 단계만 열리는 흐름을 재현할 수 있다.
function statusOf(statuses) {
  return (id) => statuses[id] ?? 'pending';
}

describe('firstSelectableFutureIndex', () => {
  it('allows only the first step when nothing has passed yet', () => {
    // 아무 단계도 통과하지 않았다면 step1만 열 수 있어야 한다.
    assert.equal(firstSelectableFutureIndex(steps, statusOf({})), 0);
  });

  it('allows the next unpassed step after previous steps passed', () => {
    // step1, step2를 통과하면 다음 학습 대상인 step3까지 열 수 있다.
    assert.equal(firstSelectableFutureIndex(steps, statusOf({ 1: 'passed', 2: 'passed' })), 2);
  });

  it('keeps a failed or validating step as the current selectable boundary', () => {
    // 실패/검증 중 단계는 아직 통과한 단계가 아니므로 그 다음 단계는 열리지 않아야 한다.
    assert.equal(firstSelectableFutureIndex(steps, statusOf({ 1: 'passed', 2: 'failed' })), 1);
    assert.equal(firstSelectableFutureIndex(steps, statusOf({ 1: 'passed', 2: 'validating' })), 1);
  });

  it('keeps every step selectable after all steps passed', () => {
    // 모든 단계를 통과한 뒤에는 복습을 위해 마지막 단계까지 모두 선택 가능해야 한다.
    assert.equal(
      firstSelectableFutureIndex(
        steps,
        statusOf({ 1: 'passed', 2: 'passed', 3: 'passed', 4: 'passed' }),
      ),
      3,
    );
  });
});

describe('isStepSelectable', () => {
  it('blocks future steps until the previous step has passed', () => {
    const currentStatusOf = statusOf({ 1: 'passed', 2: 'active' });

    // 이전 단계와 현재 단계는 열 수 있지만, 아직 도달하지 않은 step3/step4는 막는다.
    assert.equal(isStepSelectable(steps, 1, currentStatusOf), true);
    assert.equal(isStepSelectable(steps, 2, currentStatusOf), true);
    assert.equal(isStepSelectable(steps, 3, currentStatusOf), false);
    assert.equal(isStepSelectable(steps, 4, currentStatusOf), false);
  });

  it('does not allow unknown step ids', () => {
    // URL/state 오염으로 존재하지 않는 step id가 들어와도 선택 가능 처리하지 않는다.
    assert.equal(isStepSelectable(steps, 99, statusOf({})), false);
  });
});
