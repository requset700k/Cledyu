# Web 터미널 WebSocket 주소 및 재연결 설계

## 배경

운영 Web 이미지는 `NEXT_PUBLIC_WS_URL` 없이 빌드되고 있다. 현재 클라이언트는 값이 없으면
`ws://localhost:8080`을 사용하므로, `app.cledyu.local`에서 접속한 사용자의 브라우저가
클러스터 API가 아니라 사용자 PC의 localhost에 연결한다. 연결 실패 이후 재시도도 없어
터미널은 `연결 종료` 상태에 머문다.

## 목표

- 운영 환경에서 별도 빌드 변수 없이 `wss://api.cledyu.local`을 선택한다.
- localhost 개발 환경에서는 기존 `ws://localhost:8080` 흐름을 유지한다.
- `NEXT_PUBLIC_WS_URL`이 명시되면 그 값을 최우선으로 사용한다.
- 일시적인 WebSocket 종료 후 기존 터미널 화면을 보존하며 자동 재연결한다.
- IDE 프록시 주소와 터미널 WebSocket 주소가 같은 API origin 규칙을 사용하게 한다.

## URL 결정

URL 계산을 브라우저 컴포넌트에서 분리한 순수 함수로 구현한다.

1. 명시적인 `NEXT_PUBLIC_WS_URL` 값이 있으면 사용한다.
2. 현재 hostname이 `localhost` 또는 loopback이면 `ws://localhost:8080`을 사용한다.
3. hostname이 `app.<domain>`이면 같은 domain의 `api.<domain>`을 사용한다.
4. HTTPS 페이지는 `wss`, HTTP 페이지는 `ws`를 사용한다.
5. 예상하지 못한 hostname은 현재 origin의 hostname을 사용하되 포트는 유지하지 않는다.

HTTP API origin은 결정된 WebSocket origin의 `ws/wss`를 `http/https`로 변환한다.

## 재연결 정책

- 최초 연결과 재연결은 동일한 연결 함수가 담당한다.
- 비정상 종료 또는 연결 오류 후 `1초, 2초, 4초, 8초, 10초` 간격으로 재시도한다.
- 연결 성공 시 재시도 횟수를 초기화한다.
- 컴포넌트가 언마운트되거나 terminal path가 바뀌어 기존 연결을 닫은 경우 재시도하지 않는다.
- 동시에 하나의 WebSocket과 하나의 재시도 timer만 존재하도록 정리한다.
- xterm 인스턴스는 재연결 사이에 유지해 기존 출력과 사용자 문맥을 보존한다.

표시 상태는 `연결 중…`, `연결됨`, `재연결 중…`, `연결 오류`로 구분한다. 재연결 가능한
종료를 영구적인 `연결 종료`로 표시하지 않는다.

## 테스트

- 명시적 환경값 우선순위
- `app.cledyu.local`의 `wss://api.cledyu.local` 변환
- localhost 개발 주소
- HTTP/HTTPS 프로토콜 변환
- 재연결 backoff 상한
- 정상적인 dispose에서는 재연결하지 않는 조건

검증 명령:

```bash
pnpm typecheck
pnpm lint
pnpm build
pre-commit run --files \
  apps/web/components/lab/LabTerminal.tsx \
  apps/web/components/lab/LabWorkspace.tsx \
  apps/web/lib/runtime-api-origin.ts \
  apps/web/lib/runtime-api-origin.test.ts \
  docs/superpowers/specs/2026-06-21-web-terminal-websocket-recovery-design.md
```

## 범위 제외

- API 프로비저닝 timeout과 cloud-init 처리
- Kubernetes/Traefik Ingress 변경
- 세션 생성·삭제 lifecycle
- Kafka `lab-events` 오류
