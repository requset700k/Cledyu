import assert from 'node:assert/strict';
import { describe, it } from 'node:test';
import { labStatusLabel } from './dashboard.mjs';

describe('labStatusLabel', () => {
  it('maps known statuses to Korean labels', () => {
    assert.equal(labStatusLabel('completed'), '수료');
    assert.equal(labStatusLabel('in_progress'), '진행중');
    assert.equal(labStatusLabel('not_started'), '미시작');
  });

  it('falls back to the raw status for unknown values', () => {
    assert.equal(labStatusLabel('weird'), 'weird');
  });
});
