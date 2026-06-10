# RAG 오프라인 인덱싱(기획서 3.5 — 주 1회 배치).
#
# 마크다운/텍스트 문서를 청크로 잘라 ChromaDB collection 에 적재한다.
# 조직 중립성: --collection 으로 대상 collection 을 지정한다(public, org-kt-cloud, ...).
#
# 사용 예:
#   python scripts/index_docs.py --host chromadb.ai-tutor.svc --collection public \
#       --source ./corpus/k8s --title-prefix "Kubernetes Docs" --base-url https://kubernetes.io/docs
import argparse
import hashlib
import logging
import sys
from pathlib import Path

logging.basicConfig(level=logging.INFO, format="%(levelname)s %(message)s")
logger = logging.getLogger("index_docs")

CHUNK_CHARS = 1500
CHUNK_OVERLAP = 200


def chunk_text(text: str) -> list[str]:
    """문자 기준 고정 길이 + overlap 청킹. 임베딩 모델(MiniLM) 입력 한도에 안전한 크기."""
    chunks = []
    start = 0
    while start < len(text):
        end = start + CHUNK_CHARS
        chunk = text[start:end].strip()
        if chunk:
            chunks.append(chunk)
        start = end - CHUNK_OVERLAP
    return chunks


def main() -> int:
    parser = argparse.ArgumentParser(description="ChromaDB 문서 인덱싱")
    parser.add_argument("--host", required=True, help="ChromaDB host")
    parser.add_argument("--port", type=int, default=8000)
    parser.add_argument("--collection", default="public", help="대상 collection(조직 네임스페이스)")
    parser.add_argument("--source", required=True, help="문서 디렉터리(.md/.txt 재귀 탐색)")
    parser.add_argument("--title-prefix", default="", help="출처 표기용 제목 prefix")
    parser.add_argument("--base-url", default="", help="관련 문서 링크 base URL")
    args = parser.parse_args()

    import chromadb

    client = chromadb.HttpClient(host=args.host, port=args.port)
    col = client.get_or_create_collection(args.collection)

    src = Path(args.source)
    files = sorted([*src.rglob("*.md"), *src.rglob("*.txt")])
    if not files:
        logger.error("문서가 없습니다: %s", src)
        return 1

    total = 0
    for f in files:
        text = f.read_text(encoding="utf-8", errors="ignore")
        rel = f.relative_to(src).as_posix()
        title = f"{args.title_prefix} — {rel}" if args.title_prefix else rel
        url = f"{args.base_url.rstrip('/')}/{rel}" if args.base_url else ""
        chunks = chunk_text(text)
        if not chunks:
            continue
        # 동일 파일 재인덱싱 시 id 가 같아 upsert 로 갱신된다(중복 적재 방지).
        ids = [hashlib.sha1(f"{rel}:{i}".encode()).hexdigest() for i in range(len(chunks))]  # noqa: S324
        col.upsert(
            ids=ids,
            documents=chunks,
            metadatas=[{"title": title, "url": url, "path": rel}] * len(chunks),
        )
        total += len(chunks)
        logger.info("indexed %s (%d chunks)", rel, len(chunks))

    logger.info("완료 — collection=%s files=%d chunks=%d", args.collection, len(files), total)
    return 0


if __name__ == "__main__":
    sys.exit(main())
