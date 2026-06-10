# Guardrails(기획서 3.5): Rate Limit + 정답 누출 필터.
#
# Rate Limit 은 in-memory(단일 레플리카 전제)다. 레플리카를 늘리면 ElastiCache/Redis
# 카운터로 옮겨야 한다(기획서의 'AI Rate Limit 카운터' 용도) — 후속 과제로 문서화됨.
import threading
import time
from collections import defaultdict, deque


class RateLimitExceeded(Exception):
    def __init__(self, scope: str):
        self.scope = scope  # "user_minute" | "session_total"
        super().__init__(f"rate limit exceeded: {scope}")


class RateLimiter:
    """수강생당 분당 N회 + 세션당 총 M회 제한."""

    def __init__(self, per_minute: int, per_session: int):
        self.per_minute = per_minute
        self.per_session = per_session
        self._lock = threading.Lock()
        self._user_hits: dict[str, deque[float]] = defaultdict(deque)
        self._session_counts: dict[str, int] = defaultdict(int)

    def check(self, user_id: str, session_id: str, now: float | None = None) -> None:
        """제한 위반 시 RateLimitExceeded 를 던지고, 통과 시 사용량을 기록한다."""
        ts = time.monotonic() if now is None else now
        with self._lock:
            hits = self._user_hits[user_id]
            while hits and ts - hits[0] >= 60.0:
                hits.popleft()
            if self.per_minute > 0 and len(hits) >= self.per_minute:
                raise RateLimitExceeded("user_minute")
            if self.per_session > 0 and self._session_counts[session_id] >= self.per_session:
                raise RateLimitExceeded("session_total")
            hits.append(ts)
            self._session_counts[session_id] += 1

    def forget_session(self, session_id: str) -> None:
        with self._lock:
            self._session_counts.pop(session_id, None)


def mask_answer_leak(hint: str, commands: list[str], hint_level: int) -> str:
    """정답 누출 필터.

    레벨 1~2 힌트에 모범 답안 명령이 '원문 그대로' 들어 있으면 명령 이름만 남기고
    마스킹한다. 레벨 3 은 placeholder 형태의 구체 안내를 허용하므로 검사하지 않는다
    (Lab DSL 의 정적 hint_levels[2] 도 같은 수준의 구체성을 가진다).
    """
    if hint_level >= 3:
        return hint
    masked = hint
    for cmd in commands:
        cmd = cmd.strip()
        # 한 단어짜리 명령(예: "ls")은 마스킹하면 힌트 자체가 무의미해져 제외.
        if not cmd or " " not in cmd:
            continue
        if cmd in masked:
            head = cmd.split()[0]
            masked = masked.replace(cmd, f"{head} … (직접 완성해 보세요)")
    return masked
