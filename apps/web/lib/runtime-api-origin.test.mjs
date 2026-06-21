import assert from 'node:assert/strict';
import { describe, it } from 'node:test';
import {
  apiHttpOrigin,
  browserWebSocketOrigin,
  reconnectDelayMs,
  resolveWebSocketOrigin,
  shouldReconnect,
} from './runtime-api-origin.mjs';

describe('resolveWebSocketOrigin', () => {
  it('prefers an explicit build-time override', () => {
    assert.equal(
      resolveWebSocketOrigin('wss://terminal.example.com/', {
        protocol: 'https:',
        hostname: 'app.cledyu.local',
      }),
      'wss://terminal.example.com',
    );
  });

  it('maps the production app host to the api host', () => {
    assert.equal(
      resolveWebSocketOrigin(undefined, {
        protocol: 'https:',
        hostname: 'app.cledyu.local',
      }),
      'wss://api.cledyu.local',
    );
  });

  it('keeps local development on localhost port 8080', () => {
    assert.equal(
      resolveWebSocketOrigin(undefined, {
        protocol: 'http:',
        hostname: 'localhost',
      }),
      'ws://localhost:8080',
    );
  });

  it('is safe during server rendering before window exists', () => {
    assert.equal(browserWebSocketOrigin(), 'ws://localhost:8080');
  });
});

describe('apiHttpOrigin', () => {
  it('converts secure websocket origins to https', () => {
    assert.equal(apiHttpOrigin('wss://api.cledyu.local'), 'https://api.cledyu.local');
  });
});

describe('reconnect policy', () => {
  it('uses exponential backoff capped at ten seconds', () => {
    assert.deepEqual(
      [0, 1, 2, 3, 4, 5].map(reconnectDelayMs),
      [1000, 2000, 4000, 8000, 10000, 10000],
    );
  });

  it('does not reconnect after disposal or a normal close', () => {
    assert.equal(shouldReconnect(true, 1006), false);
    assert.equal(shouldReconnect(false, 1000), false);
    assert.equal(shouldReconnect(false, 1006), true);
  });
});
