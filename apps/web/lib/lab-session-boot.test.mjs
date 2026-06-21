import assert from 'node:assert/strict';
import { describe, it } from 'node:test';
import { shouldShowSessionBoot } from './lab-session-boot.mjs';

describe('shouldShowSessionBoot', () => {
  it('shows the boot screen while session data or VM provisioning is pending', () => {
    assert.equal(shouldShowSessionBoot(undefined, null, 1_000, 120_000), true);
    assert.equal(shouldShowSessionBoot('provisioning', null, 1_000, 120_000), true);
  });

  it('keeps the boot screen visible on the first ready render', () => {
    assert.equal(shouldShowSessionBoot('ready', null, 1_000, 120_000), true);
  });

  it('keeps the boot screen visible during the ready grace period', () => {
    assert.equal(shouldShowSessionBoot('ready', 1_000, 120_999, 120_000), true);
    assert.equal(shouldShowSessionBoot('active', 1_000, 120_999, 120_000), true);
  });

  it('reveals the lab once grace ends and does not mask terminal states', () => {
    assert.equal(shouldShowSessionBoot('ready', 1_000, 121_000, 120_000), false);
    assert.equal(shouldShowSessionBoot('completed', null, 1_000, 120_000), false);
    assert.equal(shouldShowSessionBoot('failed', null, 1_000, 120_000), false);
  });
});
