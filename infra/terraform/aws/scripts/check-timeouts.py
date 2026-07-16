#!/usr/bin/env python3
"""bastion 스크립트의 내부 대기 합 < SSM executionTimeout < 자식 SM 백스톱 인가.

🔴 왜 필요한가 (2026-07-16, codex P2):
  `timeoutSeconds` 는 자식 SM 에서 AWS-RunShellScript 의 executionTimeout 으로 전달돼 명령을 **강제
  종료**한다. 스크립트 내부 대기 합이 그보다 크면, 각 단계가 자기 제한 안에서 정상 진행 중인데도
  SSM 이 먼저 죽인다 — [9] 라면 **DNS 전환 직전에** 죽어 "전부 복구됐는데 서비스가 안 돌아온다".

  사람이 세면 틀린다. 실제로 세 번 틀렸다:
    · 09: 주석은 "내부 합 2400" 인데 실제 5700 — `kubectl wait --timeout` 만 세고 **존재 게이트 5개
          (3000초)를 빠뜨렸다.** 계획서 표(존재 게이트 추가 `79e9605` **이전**에 작성)를 베낀 탓이다.
    · 04: 주석이 "직렬 아님, 여유" 라고 **스스로 합리화** — 직렬이 맞아 1200 > 선언 900 이었다.
    · 08: 합 3600 = 선언 3600 으로 **여유 0** — 최악의 경우 경계에서 죽는다.

⚠️ **주석을 걷어내고 센다.** 스크립트 주석엔 "이렇게 쓰면 안 된다" 는 나쁜 예가 그대로 적혀 있어
   순진한 grep 은 오탐한다(계획서 T3 Step 9 경고 — 이 검증기 초안이 실제로 09 의 주석 속
   `--timeout=900s` 를 세서 5700 을 6600 으로 잘못 냈다).

사용법:  python3 infra/terraform/aws/scripts/check-timeouts.py   → exit 0 정상 / 1 결함
"""

import pathlib
import re
import sys

HERE = pathlib.Path(__file__).resolve().parent
TF = HERE.parent / "dr-orchestration.tf"
BASTION = HERE / "bastion"


def strip_comments(text: str) -> str:
    return "\n".join(ln for ln in text.split("\n") if not ln.lstrip().startswith("#"))


def internal_sum(path: pathlib.Path) -> tuple[int, list[str]]:
    """스크립트가 **최악의 경우** 소비할 수 있는 총 대기(초)."""
    code = strip_comments(path.read_text(encoding="utf-8"))
    total, parts = 0, []

    for w in re.findall(r"--timeout=(\d+)s", code):
        total += int(w)
        parts.append(f"wait{w}")

    # wait_exists 헬퍼: 본체 루프 x 호출 횟수
    fn = re.search(r"wait_exists\(\)\s*\{(.*?)\n\}", code, re.S)
    if fn:
        n = re.search(r"seq 1 (\d+)", fn.group(1))
        sl = re.search(r"sleep (\d+)", fn.group(1))
        if n and sl:
            per = int(n.group(1)) * int(sl.group(1))
            calls = len(re.findall(r"^\s*wait_exists \"", code, re.M))
            total += per * calls
            parts.append(f"wait_exists x{calls}@{per}")
        code = code.replace(fn.group(0), "")  # 본체를 아래 루프 집계에서 제외

    for m in re.finditer(r"for \w+ in \$\(seq 1 (\d+)\)(.*?)(?=\ndone)", code, re.S):
        sl = re.search(r"sleep (\d+)", m.group(2))
        if sl:
            total += int(m.group(1)) * int(sl.group(1))
            parts.append(f"loop{m.group(1)}x{sl.group(1)}")

    return total, parts


def main() -> int:
    tf = TF.read_text(encoding="utf-8")
    declared = {
        m.group(1): int(m.group(3))
        for m in re.finditer(
            r'scripts/bastion/([0-9.]+)-[^"]+"\)(.*?)timeoutSeconds = (\d+)', tf, re.S
        )
    }
    backstop = re.search(r"TimeoutSeconds = (\d+)\n\s*StartAt\s*=\s*\"WaitForSsmAgent\"", tf)
    if not backstop:
        m = re.search(r'Comment = "bastion 에서.*?TimeoutSeconds = (\d+)', tf, re.S)
        backstop = m
    backstop_v = int(backstop.group(1)) if backstop else 0

    bad = []
    print(f"{'스크립트':28} {'내부합':>7} {'선언':>6} {'여유':>6}")
    print("-" * 56)
    for f in sorted(BASTION.glob("*.sh")):
        key = f.name.split("-")[0]
        if key not in declared:
            continue
        total, parts = internal_sum(f)
        d = declared[key]
        ok = d > total
        print(f"{f.name:28} {total:>7} {d:>6} {d - total:>6} {'' if ok else '🔴'}")
        if not ok:
            bad.append(
                f"{f.name}: 내부합 {total} >= timeoutSeconds {d} — SSM 이 정상 진행을 죽인다"
            )
            print(f"{'':28} └ {' + '.join(parts)}")

    longest = max(
        (
            declared[k.split("-")[0]]
            for k in [f.name for f in BASTION.glob("*.sh")]
            if k.split("-")[0] in declared
        ),
        default=0,
    )
    print(f"\n자식 SM 백스톱 TimeoutSeconds = {backstop_v} / 가장 긴 스크립트 = {longest}")
    if backstop_v <= longest:
        bad.append(
            f"자식 SM 백스톱 {backstop_v} <= 가장 긴 timeoutSeconds {longest} — "
            "백스톱이 먼저 걸려 SSM TimedOut 대신 States.Timeout 이 나고 원인이 사라진다"
        )

    if bad:
        print("\n🔴 결함:")
        for b in bad:
            print(f"  - {b}")
        return 1
    print("\n✅ 전부 정합 (내부합 < timeoutSeconds < 자식 SM 백스톱)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
