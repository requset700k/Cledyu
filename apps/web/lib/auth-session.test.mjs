import assert from 'node:assert/strict';
import { describe, it } from 'node:test';
import { refreshSession } from './auth-session.mjs';

describe('refreshSession', () => {
  it('shares one refresh request across concurrent callers', async () => {
    let calls = 0;
    let finishRequest;
    const fetchImpl = () => {
      calls += 1;
      return new Promise((resolve) => {
        finishRequest = resolve;
      });
    };

    const first = refreshSession(fetchImpl);
    const second = refreshSession(fetchImpl);

    assert.equal(calls, 1);
    finishRequest({ ok: true });
    assert.deepEqual(await Promise.all([first, second]), [true, true]);
  });
});
