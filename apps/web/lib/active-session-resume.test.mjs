import assert from 'node:assert/strict';
import { describe, it } from 'node:test';
import {
  buildActiveSessionResumeHref,
  readActiveSessionResumeId,
  resolveActiveSessionResume,
} from './active-session-resume.mjs';

describe('buildActiveSessionResumeHref', () => {
  it('encodes the target lab and session id', () => {
    assert.equal(
      buildActiveSessionResumeHref('lab/k8s', 'session?123'),
      '/labs/lab%2Fk8s?resume=session%3F123',
    );
  });
});

describe('readActiveSessionResumeId', () => {
  it('returns no resume target when the query is missing', () => {
    assert.equal(readActiveSessionResumeId(new URLSearchParams()), null);
  });

  it('returns no resume target when the query is blank', () => {
    assert.equal(readActiveSessionResumeId(new URLSearchParams('resume=')), null);
  });

  it('returns the requested session id', () => {
    assert.equal(readActiveSessionResumeId(new URLSearchParams('resume=session-1')), 'session-1');
  });
});

describe('resolveActiveSessionResume', () => {
  it('accepts a session that belongs to the current lab', () => {
    assert.deepEqual(
      resolveActiveSessionResume('lab-k8s', {
        id: 'session-1',
        lab_id: 'lab-k8s',
        status: 'ready',
      }),
      { status: 'resume', sessionId: 'session-1', skipBootGrace: true },
    );
  });

  it('keeps boot grace for an existing session that is still provisioning', () => {
    assert.deepEqual(
      resolveActiveSessionResume('lab-k8s', {
        id: 'session-1',
        lab_id: 'lab-k8s',
        status: 'provisioning',
      }),
      { status: 'resume', sessionId: 'session-1', skipBootGrace: false },
    );
  });

  it('rejects a session that belongs to another lab', () => {
    assert.deepEqual(
      resolveActiveSessionResume('lab-linux', {
        id: 'session-1',
        lab_id: 'lab-k8s',
        status: 'ready',
      }),
      { status: 'lab_mismatch' },
    );
  });
});
