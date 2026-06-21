import { describe, expect, it } from 'vitest';
import {
  apiHttpOrigin,
  browserWebSocketOrigin,
  reconnectDelayMs,
  resolveWebSocketOrigin,
  shouldReconnect,
} from './runtime-api-origin';

describe('resolveWebSocketOrigin', () => {
  it('prefers an explicit build-time override', () => {
    expect(
      resolveWebSocketOrigin('wss://terminal.example.com/', {
        protocol: 'https:',
        hostname: 'app.cledyu.local',
      }),
    ).toBe('wss://terminal.example.com');
  });

  it('maps the production app host to the api host', () => {
    expect(
      resolveWebSocketOrigin(undefined, {
        protocol: 'https:',
        hostname: 'app.cledyu.local',
      }),
    ).toBe('wss://api.cledyu.local');
  });

  it('keeps local development on localhost port 8080', () => {
    expect(
      resolveWebSocketOrigin(undefined, {
        protocol: 'http:',
        hostname: 'localhost',
      }),
    ).toBe('ws://localhost:8080');
  });

  it('is safe during server rendering before window exists', () => {
    expect(browserWebSocketOrigin()).toBe('ws://localhost:8080');
  });
});

describe('apiHttpOrigin', () => {
  it('converts secure websocket origins to https', () => {
    expect(apiHttpOrigin('wss://api.cledyu.local')).toBe('https://api.cledyu.local');
  });
});

describe('reconnect policy', () => {
  it('uses exponential backoff capped at ten seconds', () => {
    expect([0, 1, 2, 3, 4, 5].map(reconnectDelayMs)).toEqual([
      1000, 2000, 4000, 8000, 10000, 10000,
    ]);
  });

  it('does not reconnect after disposal or a normal close', () => {
    expect(shouldReconnect(true, 1006)).toBe(false);
    expect(shouldReconnect(false, 1000)).toBe(false);
    expect(shouldReconnect(false, 1006)).toBe(true);
  });
});
