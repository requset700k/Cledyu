import assert from 'node:assert/strict';
import { describe, it } from 'node:test';
import { bootGraceViewState, shouldShowSessionBoot } from './lab-session-boot.mjs';

describe('bootGraceViewState', () => {
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
