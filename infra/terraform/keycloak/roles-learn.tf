# cledyu-learn realm 의 역할/그룹.
#
# 핵심 보안 결정: 자가가입(self-registration)·소셜 로그인으로 만들어지는 신규
# 사용자는 realm default group(students)에만 자동 편입되어 student 역할만 받는다.
# instructor/admin 권한은 운영자가 수동으로 그룹에 넣어야 부여된다
# — 누구나 가입만으로 강사·관리자 권한을 얻지 못하게.
#
# 역할 위계(api RBAC): admin > instructor > student. 이 역할들은 학습 앱(cledyu-learn
# realm) 내부 권한이며, 운영 인프라(ArgoCD/Kafka-UI 등)를 관리하는 cledyu realm 의
# admin 과는 무관하다(realm 분리 — issuer 가 달라 토큰이 상호 통하지 않음).
# 학습 플랫폼 관리자(유저 관리·역할 승격·세션 강제 종료)는 여기 admin 역할을 쓴다.

resource "keycloak_role" "learn_student" {
  realm_id    = keycloak_realm.cledyu_learn.id
  name        = "student"
  description = "Learner role for lab access and AI tutor usage."
}

resource "keycloak_role" "learn_instructor" {
  realm_id    = keycloak_realm.cledyu_learn.id
  name        = "instructor"
  description = "Instructor role for learner creation and instructor mode."
}

# 학습 플랫폼 관리자 — 관리자 콘솔(/api/v1/admin/*) 접근. 운영 realm(cledyu)의 admin 과 별개.
resource "keycloak_role" "learn_admin" {
  realm_id    = keycloak_realm.cledyu_learn.id
  name        = "admin"
  description = "Learning platform admin: user management, role promotion, session control."
}

resource "keycloak_group" "learn_students" {
  realm_id = keycloak_realm.cledyu_learn.id
  name     = "students"
}

resource "keycloak_group" "learn_instructors" {
  realm_id = keycloak_realm.cledyu_learn.id
  name     = "instructors"
}

# 학습 플랫폼 관리자 그룹 — default group 이 아니다. 운영자가 수동(부트스트랩)으로만 편입.
resource "keycloak_group" "learn_admins" {
  realm_id = keycloak_realm.cledyu_learn.id
  name     = "admins"
}

resource "keycloak_group_roles" "learn_students" {
  realm_id = keycloak_realm.cledyu_learn.id
  group_id = keycloak_group.learn_students.id
  role_ids = [keycloak_role.learn_student.id]
}

resource "keycloak_group_roles" "learn_instructors" {
  realm_id = keycloak_realm.cledyu_learn.id
  group_id = keycloak_group.learn_instructors.id
  role_ids = [keycloak_role.learn_instructor.id]
}

# admins 그룹 → admin 역할. api RBAC 위계상 admin 은 instructor/student 라우트도 통과하므로
# admin 역할 하나만 부여한다(Identity.Role() 이 최고 역할을 택함).
resource "keycloak_group_roles" "learn_admins" {
  realm_id = keycloak_realm.cledyu_learn.id
  group_id = keycloak_group.learn_admins.id
  role_ids = [keycloak_role.learn_admin.id]
}

# 신규 가입자 자동 편입 그룹 → student 역할만. (운영 권한 격리)
resource "keycloak_default_groups" "learn_default" {
  realm_id  = keycloak_realm.cledyu_learn.id
  group_ids = [keycloak_group.learn_students.id]
}
