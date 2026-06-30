/**
 * 준비 화면 진행률과 완료 여부를 하나의 시각 기준으로 계산한다.
 * 진행률만 100%가 되고 화면 전환은 별도 timer에 남는 상태가 생기지 않게 두 값을 함께 반환한다.
 *
 * @param {string | undefined} status
 * @param {number | null} startedAt
 * @param {number} now
 * @param {number} graceMs
 * @returns {{ progress: number, complete: boolean }}
 */
export function bootGraceViewState(status, startedAt, now, graceMs) {
  if (startedAt === null) {
    return { progress: status === 'provisioning' ? 15 : 0, complete: false };
  }

  const elapsed = Math.max(0, now - startedAt);
  const complete = graceMs <= 0 || elapsed >= graceMs;
  const ratio = graceMs <= 0 ? 1 : Math.min(1, elapsed / graceMs);
  return { progress: 30 + ratio * 70, complete };
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

/**
 * 준비 화면에 표시할 세부 단계를 만든다.
 * API가 내려주는 provisioning_stage는 VMI Running 이전 병목(디스크 복제/VM 시작)을,
 * graceStartedAt은 VMI Running 이후 터미널 자동 로그인 보호 시간을 의미한다.
 *
 * @param {string | undefined} status
 * @param {string | undefined} provisioningStage
 * @param {string | undefined} vmProvider
 * @param {number | null} graceStartedAt
 * @returns {{ label: string, done: boolean, inProgress: boolean }[]}
 */
export function bootStageViewStates(status, provisioningStage, vmProvider, graceStartedAt) {
  const sessionKnown = status !== undefined;
  const hasKubeVirtStage =
    vmProvider === 'kubevirt' &&
    (provisioningStage === 'disk_cloning' || provisioningStage === 'vm_starting');
  if (!hasKubeVirtStage) {
    const vmReady = status === 'ready' || status === 'active' || graceStartedAt !== null;
    return [
      { label: '세션 생성', done: sessionKnown, inProgress: false },
      {
        label: 'VM 프로비저닝',
        done: vmReady,
        inProgress: sessionKnown && !vmReady,
      },
      {
        label: '자동 로그인 활성화',
        done: false,
        inProgress: vmReady,
      },
    ];
  }

  const diskReady =
    status === 'ready' ||
    status === 'active' ||
    graceStartedAt !== null ||
    provisioningStage === 'vm_starting';
  const vmRunning = status === 'ready' || status === 'active' || graceStartedAt !== null;

  return [
    { label: '세션 생성', done: sessionKnown, inProgress: false },
    {
      label: '디스크 복제',
      done: diskReady,
      inProgress: sessionKnown && !diskReady,
    },
    {
      label: 'VM 시작',
      done: vmRunning,
      inProgress: diskReady && !vmRunning,
    },
    {
      label: '자동 로그인 활성화',
      done: false,
      inProgress: vmRunning,
    },
  ];
}
