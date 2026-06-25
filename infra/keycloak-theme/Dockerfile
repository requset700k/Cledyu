# cledyu Keycloak 커스텀 이미지 — login 테마만 추가(외형 브랜딩).
# 베이스 버전은 라이브 Keycloak(quay.io/keycloak/keycloak:26.6.1)과 일치시킨다.
# 테마는 런타임에 themes/ 에서 스캔되므로 kc.sh build 불필요(COPY 만으로 동작).
# build context: infra/keycloak-theme/
FROM quay.io/keycloak/keycloak:26.6.1

COPY cledyu /opt/keycloak/themes/cledyu

# realm 의 login_theme=cledyu 설정 시 적용된다(terraform realm-learn.tf).
