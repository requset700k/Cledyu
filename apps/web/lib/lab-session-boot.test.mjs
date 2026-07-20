import assert from 'node:assert/strict';
import { describe, it } from 'node:test';
import {
  bootGraceViewState,
  bootStageViewStates,
  shouldShowSessionBoot,
} from './lab-session-boot.mjs';

describe('bootGraceViewState', () => {
  it('advances provisioning progress with the backend stage', () => {
    assert.equal(
      bootGraceViewState('provisioning', null, 1_000, 120_000, 'disk_cloning').progress,
      35,
    );
    assert.equal(
      bootGraceViewState('provisioning', null, 1_000, 120_000, 'vm_starting').progress,
      65,
    );
  });

  it('does not move backwards when ready grace begins', () => {
    assert.equal(bootGraceViewState('ready', 1_000, 1_000, 120_000).progress, 75);
  });

  it('marks grace complete at the same boundary that fills progress to 100 percent', () => {
    const beforeBoundary = bootGraceViewState('ready', 1_000, 120_999, 120_000);
    assert.equal(beforeBoundary.complete, false);
    assert.ok(beforeBoundary.progress < 100);

    assert.deepEqual(bootGraceViewState('ready', 1_000, 121_000, 120_000), {
      progress: 100,
      complete: true,
    });
  });
});

describe('shouldShowSessionBoot', () => {
  it('shows the boot screen while session data or VM provisioning is pending', () => {
    assert.equal(shouldShowSessionBoot(undefined, null, 1_000, 120_000, false), true);
    assert.equal(shouldShowSessionBoot('provisioning', null, 1_000, 120_000, true), true);
  });

  it('keeps a newly ready session masked before its grace timer is initialized', () => {
    assert.equal(shouldShowSessionBoot('ready', null, 1_000, 120_000, false), true);
  });

  it('keeps a newly ready session masked during grace and reveals it afterwards', () => {
    assert.equal(shouldShowSessionBoot('ready', 1_000, 120_999, 120_000, false), true);
    assert.equal(shouldShowSessionBoot('ready', 1_000, 121_000, 120_000, false), false);
  });

  it('reveals a ready session immediately when resume already verified readiness', () => {
    assert.equal(shouldShowSessionBoot('ready', null, 1_000, 120_000, true), false);
    assert.equal(shouldShowSessionBoot('active', null, 1_000, 120_000, true), false);
  });

  it('does not mask terminal session states', () => {
    assert.equal(shouldShowSessionBoot('completed', null, 1_000, 120_000, false), false);
    assert.equal(shouldShowSessionBoot('failed', null, 1_000, 120_000, false), false);
  });
});

describe('bootStageViewStates', () => {
  it('shows disk cloning before the VM can start', () => {
    assert.deepEqual(bootStageViewStates('provisioning', 'disk_cloning', 'kubevirt', null), [
      { label: '세션 생성', done: true, inProgress: false },
      { label: '디스크 복제', done: false, inProgress: true },
      { label: 'VM 시작', done: false, inProgress: false },
      { label: '자동 로그인 활성화', done: false, inProgress: false },
    ]);
  });

  it('shows VM startup after the root disk is ready', () => {
    assert.deepEqual(bootStageViewStates('provisioning', 'vm_starting', 'kubevirt', null), [
      { label: '세션 생성', done: true, inProgress: false },
      { label: '디스크 복제', done: true, inProgress: false },
      { label: 'VM 시작', done: false, inProgress: true },
      { label: '자동 로그인 활성화', done: false, inProgress: false },
    ]);
  });

  it('shows autologin grace after the VM is running', () => {
    assert.deepEqual(bootStageViewStates('ready', undefined, 'kubevirt', 1_000), [
      { label: '세션 생성', done: true, inProgress: false },
      { label: 'VM 프로비저닝', done: true, inProgress: false },
      { label: '자동 로그인 활성화', done: false, inProgress: true },
    ]);
  });

  it('keeps provider-neutral provisioning when the backend does not expose KubeVirt stages', () => {
    assert.deepEqual(bootStageViewStates('provisioning', undefined, 'ec2', null), [
      { label: '세션 생성', done: true, inProgress: false },
      { label: 'VM 프로비저닝', done: false, inProgress: true },
      { label: '자동 로그인 활성화', done: false, inProgress: false },
    ]);
  });

  it('keeps provider-neutral provisioning when stage information is not available yet', () => {
    assert.deepEqual(bootStageViewStates('provisioning', undefined, 'kubevirt', null), [
      { label: '세션 생성', done: true, inProgress: false },
      { label: 'VM 프로비저닝', done: false, inProgress: true },
      { label: '자동 로그인 활성화', done: false, inProgress: false },
    ]);
  });
});
