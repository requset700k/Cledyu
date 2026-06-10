# Lab DSL 작성 가이드

랩 콘텐츠는 `apps/api/internal/content/labs/*.yaml` 한 파일로 정의합니다(임베드 빌드).
콘텐츠 제작자는 Go 코드 없이 Git PR 로 랩을 추가합니다(기획서 3.4).

## 최상위 필드

| 필드 | 필수 | 설명 |
|---|---|---|
| `id` | O | 고유 ID. VM 사양 매핑(`labVMType`)과 일치시킬 것 |
| `title` / `description` | O | 카탈로그 표기 |
| `difficulty` | O | beginner / intermediate / advanced |
| `duration_min` | O | 예상 소요(분) |
| `vm_type` | O | lab-small(2vCPU/4GB) / lab-medium(4vCPU/8GB) |
| `environment` | O | `ubuntu`(실시간 터미널 제공) / `k3s`(콘텐츠 전용, 터미널 미구현) |
| `ide` |  | `true` 면 세션에 브라우저 VS Code(IDE 탭) 제공 — init 에서 code-server 설치 필요 |
| `init` |  | 세션 VM 부팅 시 cloud-init 초기화(아래) |
| `steps` | O | 실습 단계 목록 |

## init — 랩별 VM 초기화 (기획서의 initial_state)

```yaml
init:
  packages:        # cloud-init packages: (apt 설치)
    - ansible-core
  runcmd:          # cloud-init runcmd: 끝에 추가 — 셸 한 줄씩, root 로 실행
    - curl -fsSL -o /tmp/terraform.zip https://releases.hashicorp.com/terraform/1.9.8/terraform_1.9.8_linux_amd64.zip
```

- 공통 runcmd(autologin getty 재시작) **뒤에** 실행되므로 터미널은 설치 완료 전에 열린다.
- 콜론(`: `)이 들어가는 명령은 YAML 파싱을 위해 전체를 큰따옴표로 감싼다.
- 무거운 다운로드는 부팅 체감을 늘린다 — 자주 쓰는 도구는 베이스 이미지에 굽는 것이 후속 과제.

## ide — 브라우저 VS Code

`ide: true` 랩은 init 에서 code-server 를 설치·기동해야 한다(13337, auth none —
`lab-terraform-basics.yaml` 의 init 블록을 그대로 복사 권장). 접근 경로:

```
web iframe → api /api/v1/sessions/:id/ide/* (JWT + 세션 소유자 검증)
           → VM pod IP:13337 (code-server)
```

code-server 는 인증 없이 떠 있지만 VM pod IP 는 클러스터 밖에서 접근 불가하고,
프록시가 유일한 진입점이다. lab 네임스페이스에 CiliumNetworkPolicy 를 도입할 때
api → 13337 ingress 를 허용해야 한다.

## steps

```yaml
steps:
  - id: 1                  # 1부터 오름차순
    title: ...
    description: ...       # 멀티라인은 | 또는 >
    commands: [...]        # 화면에 보여줄 안내 명령(검증과 무관)
    hint_levels:           # 정확히 3개 — 레벨 1(개념) → 2(방향) → 3(구체)
      - ...
      - ...
      - ...
    checks: [...]          # 검증엔진 체크
```

`hint_levels` 는 AI 도우미(ai-tutor) 미가용 시의 최종 폴백이다. 3개가 아니면
content 테스트(`TestLoad_HintLevels`)가 실패한다. 레벨 3도 복사-붙여넣기 가능한
완성 명령은 피한다(소크라테스식 원칙).

## checks — 검증엔진 체크

| type | 필드 | 예 |
|---|---|---|
| `command` | command, expect | `terraform -chdir=/home/lab/workspace state list` → `local_file.hello` |
| `file_exists` | path | `/home/lab/work/notes.txt` |
| `file_content` | path, expect | 부분 문자열 매칭 |
| `process_running` | name | pgrep |
| `http_response` | url, expect_code | curl |

검증은 `lab` 사용자로 virtctl ssh 실행된다(`~` = `/home/lab`). **명령 화이트리스트**
(`영숫자 공백 / . _ - = : @ % " '`)를 벗어나는 문자(`; | & > < $` 등)는 거부되므로
파이프/리다이렉션 없는 단일 명령으로 작성한다. 경로는 절대경로만 허용.

## 체크리스트 (새 랩 PR)

- [ ] `id` 를 `apps/api/internal/kubevirt/session.go` 의 `labVMType` 에 추가(미등록 시 lab-small 폴백)
- [ ] 스텝마다 `hint_levels` 3개
- [ ] checks 가 화이트리스트/절대경로 규칙을 지킴
- [ ] `go test ./internal/content/` 통과(파싱·힌트 레벨 검증)
