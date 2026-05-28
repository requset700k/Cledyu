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

/** 단계별 진행 상태 — StepList 컴포넌트의 아이콘/색상 결정에 사용 */
export type StepStatus = 'pending' | 'active' | 'passed' | 'failed';

/** instructor 역할은 /instructor 대시보드 접근 + 강사 API 사용 가능 */
export type UserRole = 'student' | 'instructor' | 'admin';

/** 실습 상세/세션 페이지에서 표시하는 단계 콘텐츠. 백엔드 Lab DSL(content.Step)과 대응. */
export interface StepContent {
  id: number;
  title: string;
  description: string;
  commands?: string[];
  hint?: string;
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
}

/** 수강생 1명이 특정 Lab을 수행하는 동안 유지되는 세션 */
export interface Session {
  id: string;
  lab_id: string;
  user_id: string;
  status: SessionStatus;
  vm_provider?: VMProvider;
  terminal_url?: string; // status가 ready가 되면 채워짐 (/api/v1/sessions/:id/ws)
  current_step: number;
  started_at: string;
  expires_at: string; // 세션 최대 유지 시간 3시간
}

/** 세션 내 개별 단계의 진행 상황 */
export interface StepProgress {
  step_id: number;
  status: StepStatus;
  attempts: number; // 검증 시도 횟수
}

export interface User {
  id: string;
  email: string;
  name: string;
  role: UserRole;
  points: number;
}

/** API 에러 응답 공통 포맷 */
export interface ApiError {
  error: string;
  code?: string;
}
