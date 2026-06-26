// 백엔드 API와 주고받는 도메인 타입 정의.
// Go 백엔드 API 스펙 기반으로 작성. 백엔드 구현 시 OpenAPI 스펙과 대조 검증 필요.

/** Lab 난이도 — 카탈로그 필터 및 카드 색상에 사용 */
export type Difficulty = 'beginner' | 'intermediate' | 'advanced';

/**
 * Lab 세션 상태 흐름:
 * provisioning → ready → active → completed
 *                              ↘ failed (VM 부팅 실패 시)
 */
export type SessionStatus = 'provisioning' | 'ready' | 'active' | 'completed' | 'failed';

/** VM 프로바이더 — 온프렘 KubeVirt 또는 AWS EC2 오버플로우 */
export type VMProvider = 'kubevirt' | 'ec2';

/**
 * 단계별 진행 상태 — StepList 컴포넌트의 아이콘/색상 결정에 사용.
 * validating: 검증 요청을 보내고 검증엔진 결과를 기다리는 중(비동기).
 */
export type StepStatus = 'pending' | 'active' | 'validating' | 'passed' | 'failed';

/** instructor 역할은 /instructor 대시보드 접근 + 강사 API 사용 가능 */
export type UserRole = 'student' | 'instructor' | 'admin';

/** 실습 상세/세션 페이지에서 표시하는 단계 콘텐츠. 백엔드 Lab DSL(content.Step)과 대응. */
export interface StepContent {
  id: number;
  title: string;
  description: string;
  commands?: string[];
  hint?: string;
  hint_levels?: string[]; // 레벨 1(개념)→2(방향)→3(구체) 정적 힌트 — AI 미가용 시 폴백
}

/** AI 힌트와 함께 표시하는 관련 문서 링크(RAG 출처) */
export interface HintSource {
  title: string;
  url?: string;
}

/**
 * AI 학습 도우미 힌트 — POST /api/v1/sessions/:id/hint 응답.
 * source=ai 면 Gemini 생성(model 포함), source=static 이면 Lab DSL hint_levels 폴백.
 */
export interface HintResponse {
  hint: string;
  hint_level: number;
  source: 'ai' | 'static';
  model?: string;
  sources?: HintSource[];
}

/** Lab 카탈로그에 표시되는 실습 항목 */
export interface Lab {
  id: string;
  title: string;
  description: string;
  difficulty: Difficulty;
  duration_min: number;
  tags: string[];
  vm_type: string; // Lab 실행에 필요한 VM 사양 (kt-lab-small | medium)
  step_count: number;
  steps?: StepContent[]; // GET /api/v1/labs/:id 에서만 채워짐 (목록 응답엔 없음)
  environment?: string; // 세션 VM 환경 (ubuntu | k3s). ubuntu만 실시간 터미널 제공(Phase-1).
  ide?: boolean; // true면 세션에 브라우저 VS Code(IDE 탭) 제공 (예: Terraform 랩)
}

/** 수강생 1명이 특정 Lab을 수행하는 동안 유지되는 세션 */
export interface Session {
  id: string;
  lab_id: string;
  user_id: string;
  status: SessionStatus;
  vm_provider?: VMProvider;
  terminal_url?: string; // status가 ready가 되면 채워짐 (/api/v1/sessions/:id/ws)
  ide_url?: string; // IDE 랩만 — code-server 프록시 경로 (/api/v1/sessions/:id/ide/)
  current_step: number;
  started_at: string;
  expires_at: string; // 세션 최대 유지 시간 3시간
}

/** 검증엔진이 돌려준 체크 항목 하나의 결과 (실패 사유 표시용) */
export interface CheckResult {
  type: string;
  passed: boolean;
  detail?: string;
}

/** 세션 내 개별 단계의 진행 상황 */
export interface StepProgress {
  step_id: number;
  status: StepStatus;
  attempts: number; // 검증 시도 횟수
  checks?: CheckResult[]; // 검증엔진 결과 수신 시 채워짐 (체크별 pass/detail)
}

export interface User {
  id: string;
  email: string;
  name: string;
  role: UserRole;
  org?: string; // 소속 조직(RAG 멀티테넌트). 없으면 빈 문자열/public.
  points: number;
}

/** API 에러 응답 공통 포맷 */
export interface ApiError {
  error: string;
  code?: string;
  session_id?: string;
}

/** 리더보드 한 행(명예의 전당/급상승) — GET /api/v1/leaderboard */
export interface LeaderboardItem {
  rank: number;
  name: string;
  score: number;
  labs_completed: number;
}

/** 본인 순위 — rank=0 은 미공개(옵트아웃)/완료 없음 */
export interface MyRank {
  rank: number;
  score: number;
  labs_completed: number;
}

/** GET /api/v1/leaderboard 응답 */
export interface LeaderboardResponse {
  hall_of_fame: LeaderboardItem[];
  recent_risers: LeaderboardItem[];
  me: MyRank;
}

/** GET /api/v1/me/progress 응답 */
export interface MyProgress {
  score: number;
  rank: number;
  labs_completed: number;
  by_difficulty: Record<Difficulty, number>;
  recent_completions: { lab_id: string; session_id: string; completed_at: string }[];
}
