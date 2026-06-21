import assert from 'node:assert/strict';
import { describe, it } from 'node:test';
import {
  apiHttpOrigin,
  browserWebSocketOrigin,
  reconnectDelayMs,
  resolveWebSocketOrigin,
  shouldReconnect,
} from './runtime-api-origin.mjs';

// 브라우저·클러스터 없이 실행하는 정책 단위 테스트다. 운영 Web이 다시 localhost를 사용하거나
// 재연결이 무한 동시 실행되는 회귀를 빠르게 잡기 위해 Node.js 기본 test runner만 사용한다.
describe('resolveWebSocketOrigin', () => {
  it('prefers an explicit build-time override', () => {
    // preview/별도 배포 환경이 명시한 주소는 app.* 자동 변환보다 우선해야 한다.
    assert.equal(
      resolveWebSocketOrigin('wss://terminal.example.com/', {
        protocol: 'https:',
        hostname: 'app.cledyu.local',
      }),
      'wss://terminal.example.com',
    );
  });

  it('maps the production app host to the api host', () => {
    // 실제 장애 원인이었던 app.cledyu.local → localhost fallback 회귀를 직접 방지한다.
    assert.equal(
      resolveWebSocketOrigin(undefined, {
        protocol: 'https:',
        hostname: 'app.cledyu.local',
      }),
      'wss://api.cledyu.local',
    );
  });

  it('keeps local development on localhost port 8080', () => {
    // 로컬 Next.js(3000)와 Go API(8080)를 분리 실행하는 기존 개발 흐름을 보존한다.
    assert.equal(
      resolveWebSocketOrigin(undefined, {
        protocol: 'http:',
        hostname: 'localhost',
      }),
      'ws://localhost:8080',
    );
  });

  it('is safe during server rendering before window exists', () => {
    // Next.js SSR에서는 window가 없으므로 bundle 평가 중 ReferenceError가 나면 안 된다.
    assert.equal(browserWebSocketOrigin(), 'ws://localhost:8080');
  });
});

describe('apiHttpOrigin', () => {
  it('converts secure websocket origins to https', () => {
    // IDE health check와 iframe은 WebSocket이 아닌 동일 API host의 HTTPS endpoint를 사용한다.
    assert.equal(apiHttpOrigin('wss://api.cledyu.local'), 'https://api.cledyu.local');
  });
});

describe('reconnect policy', () => {
  it('uses exponential backoff capped at ten seconds', () => {
    // 연속 장애 시 API를 과도하게 호출하지 않되 10초 이상 사용자 복구를 늦추지 않는다.
    assert.deepEqual(
      [0, 1, 2, 3, 4, 5].map(reconnectDelayMs),
      [1000, 2000, 4000, 8000, 10000, 10000],
    );
  });

  it('only reconnects after an abnormal close event', () => {
    // 화면 이동·정상 종료와 close event 전에 발생한 URL 생성 오류는 재시도하지 않는다.
    assert.equal(shouldReconnect(true, 1006), false);
    assert.equal(shouldReconnect(false, 1000), false);
    assert.equal(shouldReconnect(false, undefined), false);
    assert.equal(shouldReconnect(false, 1006), true);
  });
});
