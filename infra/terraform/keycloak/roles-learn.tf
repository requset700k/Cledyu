# cledyu-learn realm 의 역할/그룹.
#
# 핵심 보안 결정: 자가가입(self-registration)·소셜 로그인으로 만들어지는 신규
# 사용자는 realm default group(students)에만 자동 편입되어 student 역할만 받는다.
# instructor 권한은 운영자가 admin console 에서 수동으로 instructors 그룹에 넣어야
# 부여된다 — 누구나 가입만으로 강사 권한을 얻지 못하게.

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

resource "keycloak_group" "learn_students" {
  realm_id = keycloak_realm.cledyu_learn.id
  name     = "students"
}

resource "keycloak_group" "learn_instructors" {
  realm_id = keycloak_realm.cledyu_learn.id
  name     = "instructors"
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

# 신규 가입자 자동 편입 그룹 → student 역할만. (운영 권한 격리)
resource "keycloak_default_groups" "learn_default" {
  realm_id  = keycloak_realm.cledyu_learn.id
  group_ids = [keycloak_group.learn_students.id]
}
