# Redis 백엔드 Rate Limiter — 다중 레플리카에서 공유 카운터로 한도를 강제한다.
# in-memory RateLimiter(guardrails.py)와 동일한 인터페이스(check/forget_session)를 가진다.
#
# 원자성: 분당(sliding window, sorted set)과 세션 누적(counter) 검사를 단일 Lua 로 수행해
# 동시 요청의 TOCTOU 경합을 막는다. Redis 장애 시 fail-open(허용)으로 학습 흐름을 막지 않는다
# (rate limit 은 비용 보호 장치이지 보안 경계가 아니다 — 장애 시 가용성 우선).
import logging
import time

from .guardrails import RateLimitExceeded

logger = logging.getLogger("ai_tutor")

# KEYS[1]=user zset, KEYS[2]=session counter
# ARGV: now, window(s), per_minute, per_session, member, session_ttl(s)
_CHECK_LUA = """
local now = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local per_minute = tonumber(ARGV[3])
local per_session = tonumber(ARGV[4])
local member = ARGV[5]
local session_ttl = tonumber(ARGV[6])

redis.call('ZREMRANGEBYSCORE', KEYS[1], 0, now - window)
local minute_count = redis.call('ZCARD', KEYS[1])
if per_minute > 0 and minute_count >= per_minute then
    return 'user_minute'
end

local session_count = tonumber(redis.call('GET', KEYS[2]) or '0')
if per_session > 0 and session_count >= per_session then
    return 'session_total'
end

redis.call('ZADD', KEYS[1], now, member)
redis.call('EXPIRE', KEYS[1], window)
redis.call('INCR', KEYS[2])
redis.call('EXPIRE', KEYS[2], session_ttl)
return 'ok'
"""

# 세션 카운터 만료 — 세션 최대 수명(3h)보다 넉넉히 둬 진행 중 세션 한도가 풀리지 않게 한다.
_SESSION_TTL = 6 * 3600


class RedisRateLimiter:
    """수강생당 분당 N회 + 세션당 총 M회 제한 (Redis 공유 카운터)."""

    def __init__(self, redis_url: str, per_minute: int, per_session: int):
        import redis  # lazy — Redis 백엔드를 쓸 때만 의존

        self._redis = redis.Redis.from_url(redis_url, socket_timeout=2)
        self._check = self._redis.register_script(_CHECK_LUA)
        self.per_minute = per_minute
        self.per_session = per_session
        self._counter = 0  # member 고유성 보조(같은 ms 다중 요청 구분)

    def check(self, user_id: str, session_id: str, now: float | None = None) -> None:
        ts = time.time() if now is None else now
        self._counter += 1
        member = f"{ts:.6f}-{self._counter}"
        try:
            result = self._check(
                keys=[f"ai:rl:min:{user_id}", f"ai:rl:sess:{session_id}"],
                args=[ts, 60, self.per_minute, self.per_session, member, _SESSION_TTL],
            )
        except Exception:
            # Redis 장애 — fail-open. 가용성 우선(가격 보호는 일시적으로 약화).
            logger.warning("rate limiter redis 장애 — fail-open", exc_info=True)
            return
        decoded = result.decode() if isinstance(result, bytes) else result
        if decoded != "ok":
            raise RateLimitExceeded(decoded)

    def forget_session(self, session_id: str) -> None:
        try:
            self._redis.delete(f"ai:rl:sess:{session_id}")
        except Exception:
            logger.warning("rate limiter forget_session redis 장애", exc_info=True)
