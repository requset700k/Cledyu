# RAG 검색(기획서 3.5): ChromaDB 멀티테넌트 collection.
#
# 조직 중립성 — collection 을 조직별로 분리(public, org-kt-cloud, ...)하고
# 힌트 생성 시 [요청 org, public] 을 함께 검색한다. 호스트 미설정/조회 실패 시
# 빈 결과를 반환해 힌트 생성 자체는 계속 진행한다(RAG 는 품질 보강 레이어).
import logging

from .config import Settings

logger = logging.getLogger(__name__)

PUBLIC_COLLECTION = "public"


class Retriever:
    def __init__(self, settings: Settings):
        self._settings = settings
        self._client = None

    def _connect(self):
        if self._client is None:
            # chromadb 는 무겁고(onnxruntime 임베딩 포함) 선택 의존이라 lazy import.
            import chromadb

            self._client = chromadb.HttpClient(
                host=self._settings.chroma_host, port=self._settings.chroma_port
            )
        return self._client

    def search(self, org_id: str, query: str) -> list[dict]:
        """org collection + public collection 에서 관련 청크를 검색한다.

        반환: [{text, title, url}] (관련도 순, 최대 rag_top_k 개)
        """
        if not self._settings.chroma_host or not query.strip():
            return []
        names = [PUBLIC_COLLECTION]
        if org_id and org_id != PUBLIC_COLLECTION:
            names.insert(0, org_id)

        chunks: list[dict] = []
        try:
            client = self._connect()
            per_collection = max(1, self._settings.rag_top_k // len(names))
            for name in names:
                try:
                    col = client.get_collection(name)
                except Exception:
                    # collection 미존재(아직 인덱싱 전) — 정상 경로라 warning 아님.
                    logger.debug("collection 미존재 — 건너뜀: %s", name)
                    continue
                res = col.query(query_texts=[query], n_results=per_collection)
                docs = (res.get("documents") or [[]])[0]
                metas = (res.get("metadatas") or [[]])[0]
                for doc, meta in zip(docs, metas, strict=False):
                    meta = meta or {}
                    chunks.append(
                        {
                            "text": doc,
                            "title": str(meta.get("title", "")),
                            "url": str(meta.get("url", "")),
                        }
                    )
        except Exception:
            # RAG 장애는 힌트 생성을 막지 않는다 — 빈 컨텍스트로 진행.
            logger.warning("chromadb 검색 실패 — RAG 없이 진행", exc_info=True)
            return []
        return chunks[: self._settings.rag_top_k]
