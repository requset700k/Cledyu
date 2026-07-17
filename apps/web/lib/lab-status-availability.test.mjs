import assert from 'node:assert/strict';
import test from 'node:test';

import { getLabStatusAvailability } from './lab-status-availability.mjs';

test('캐시된 상태가 있으면 백그라운드 갱신 중에도 링크를 유지한다', () => {
  const availability = getLabStatusAvailability({
    hasData: true,
    isFetching: true,
  });

  assert.deepEqual(availability, {
    isInitialLoading: false,
    statusReady: true,
  });
});

test('상태 데이터가 없는 최초 요청 중에만 링크를 비활성화한다', () => {
  const availability = getLabStatusAvailability({
    hasData: false,
    isFetching: true,
  });

  assert.deepEqual(availability, {
    isInitialLoading: true,
    statusReady: false,
  });
});
