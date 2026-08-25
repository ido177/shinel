"""Named-entity sidecar for shinel.

Runs GLiNER behind a small HTTP API so the Go proxy can find entities that
regular expressions cannot, without pulling ONNX or cgo into the Go binary.
"""

import logging
import threading
from contextlib import asynccontextmanager

from fastapi import FastAPI
from pydantic import BaseModel, Field

MODEL_NAME = "urchade/gliner_multi-v2.1"

log = logging.getLogger("ml_engine")

_model = None
# ponytail: one inference at a time. GLiNER is not documented as thread safe and
# a synchronous endpoint is served from a threadpool, so the lock keeps
# concurrent requests from sharing model state. Upgrade path: batch queued
# requests, or run several uvicorn workers behind the sidecar.
_lock = threading.Lock()


@asynccontextmanager
async def lifespan(app: FastAPI):
    """Load the weights once, before the first request is served."""
    global _model
    from gliner import GLiNER

    log.info("loading %s", MODEL_NAME)
    _model = GLiNER.from_pretrained(MODEL_NAME)
    log.info("model ready")
    yield
    _model = None


app = FastAPI(title="shinel ml_engine", lifespan=lifespan)


class AnalyzeRequest(BaseModel):
    text: str
    labels: list[str]
    threshold: float = Field(default=0.5, ge=0.0, le=1.0)


class Entity(BaseModel):
    entity: str
    label: str
    # Character offsets into text, not bytes. The Go client converts them.
    start: int
    end: int


@app.get("/health")
def health() -> dict[str, str]:
    return {"status": "ok" if _model is not None else "loading"}


# Deliberately sync: inference is blocking and CPU bound, so FastAPI runs this
# in a worker thread instead of stalling the event loop.
@app.post("/analyze", response_model=list[Entity])
def analyze(req: AnalyzeRequest) -> list[Entity]:
    if not req.text or not req.labels or _model is None:
        return []

    with _lock:
        found = _model.predict_entities(req.text, req.labels, threshold=req.threshold)

    return [
        Entity(
            entity=e["text"],
            label=e["label"],
            start=e["start"],
            end=e["end"],
        )
        for e in found
    ]
