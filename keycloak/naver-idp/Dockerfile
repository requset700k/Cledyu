# 재현 가능한 JAR 빌드용(로컬에 Maven/JDK 가 없을 때). 결과 JAR 을 dist/ 로 추출한다:
#   docker build -t naver-idp-build .
#   docker create --name naver-idp-extract naver-idp-build
#   docker cp naver-idp-extract:/build/target/naver-idp.jar dist/naver-idp.jar
#   docker rm naver-idp-extract
#
# 이 SPI 는 커스텀 keycloak 이미지를 만들지 않는다(operator 가 비-optimized 로 기동→
# /opt/keycloak/providers 의 JAR 을 매 시작 시 augmentation 으로 등록). JAR 은 ConfigMap
# (binaryData)으로 마운트한다. 자세한 내용은 README.md.
FROM maven:3.9-eclipse-temurin-21 AS build
WORKDIR /build
COPY pom.xml .
RUN mvn -q -B dependency:go-offline
COPY src ./src
RUN mvn -q -B clean package
