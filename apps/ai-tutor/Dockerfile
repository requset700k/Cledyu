# AI 학습 도우미 BFF — python slim 기반, non-root(65532) 실행.
# readOnlyRootFilesystem 배포를 전제로 캐시류는 /tmp(emptyDir) 아래로 보낸다.
FROM python:3.12-slim AS base

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    # chromadb 기본 ONNX 임베딩 모델 다운로드 위치(emptyDir 마운트 경로와 일치).
    XDG_CACHE_HOME=/tmp/cache \
    HF_HOME=/tmp/cache/hf

WORKDIR /srv/ai-tutor

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY app ./app

# distroless 계열과 동일한 uid 로 통일(api/validation-engine 배포와 같은 securityContext).
USER 65532:65532

EXPOSE 8080
CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8080"]
