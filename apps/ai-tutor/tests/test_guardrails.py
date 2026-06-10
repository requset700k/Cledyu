import pytest
from app.guardrails import RateLimiter, RateLimitExceeded, mask_answer_leak


class TestRateLimiter:
    def test_user_minute_limit(self):
        rl = RateLimiter(per_minute=2, per_session=100)
        rl.check("u1", "s1", now=0.0)
        rl.check("u1", "s1", now=1.0)
        with pytest.raises(RateLimitExceeded) as e:
            rl.check("u1", "s1", now=2.0)
        assert e.value.scope == "user_minute"

    def test_window_slides(self):
        rl = RateLimiter(per_minute=2, per_session=100)
        rl.check("u1", "s1", now=0.0)
        rl.check("u1", "s1", now=1.0)
        # 60초가 지나면 첫 호출이 윈도우에서 빠져 다시 허용된다.
        rl.check("u1", "s1", now=61.0)

    def test_session_total_limit(self):
        rl = RateLimiter(per_minute=100, per_session=3)
        for i in range(3):
            rl.check(f"u{i}", "s1", now=float(i * 70))  # 분당 제한 회피용 시간 간격
        with pytest.raises(RateLimitExceeded) as e:
            rl.check("u9", "s1", now=400.0)
        assert e.value.scope == "session_total"

    def test_users_are_isolated(self):
        rl = RateLimiter(per_minute=1, per_session=100)
        rl.check("u1", "s1", now=0.0)
        rl.check("u2", "s2", now=0.0)  # 다른 유저는 영향 없음

    def test_forget_session(self):
        rl = RateLimiter(per_minute=100, per_session=1)
        rl.check("u1", "s1", now=0.0)
        rl.forget_session("s1")
        rl.check("u1", "s1", now=70.0)


class TestMaskAnswerLeak:
    def test_masks_full_command_at_level_1(self):
        hint = "mkdir -p ~/work 를 실행해 보세요."
        out = mask_answer_leak(hint, ["mkdir -p ~/work"], hint_level=1)
        assert "mkdir -p ~/work" not in out
        assert "mkdir" in out  # 명령 이름은 남는다

    def test_level_3_allows_concrete_hint(self):
        hint = "mkdir -p ~/work 를 실행해 보세요."
        assert mask_answer_leak(hint, ["mkdir -p ~/work"], hint_level=3) == hint

    def test_single_word_command_not_masked(self):
        hint = "ls 로 확인해 보세요."
        assert mask_answer_leak(hint, ["ls"], hint_level=1) == hint

    def test_no_commands(self):
        assert mask_answer_leak("힌트", [], 1) == "힌트"
