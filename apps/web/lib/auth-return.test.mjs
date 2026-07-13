import assert from 'node:assert/strict';
import { describe, it } from 'node:test';
import { normalizeReturnPath, pathWithSearch } from './auth-return.mjs';

describe('auth return path helpers', () => {
  it('preserves query string for protected route redirects', () => {
    assert.equal(
      pathWithSearch({ pathname: '/billing', search: '?checkout_session_id=cs_mock&provider=mock' }),
      '/billing?checkout_session_id=cs_mock&provider=mock',
    );
  });

  it('keeps local checkout return paths with search params', () => {
    assert.equal(
      normalizeReturnPath('/billing?checkout_session_id=cs_mock&provider=mock'),
      '/billing?checkout_session_id=cs_mock&provider=mock',
    );
  });

  it('rejects external or protocol-relative return paths', () => {
    assert.equal(normalizeReturnPath('https://evil.example/billing'), '/labs');
    assert.equal(normalizeReturnPath('//evil.example/billing'), '/labs');
  });
});
