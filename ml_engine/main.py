"""Named-entity sidecar for shinel.

Runs GLiNER behind a small HTTP API so the Go proxy can find entities that
regular expressions cannot, without pulling ONNX or cgo into the Go binary.
"""

from __future__ import annotations

import logging
import os
import queue
import threading
import time
from collections import defaultdict
from concurrent.futures import Future
from contextlib import asynccontextmanager
from typing import Any

from fastapi import FastAPI
from pydantic import BaseModel, Field

from settings import model_name

log = logging.getLogger("ml_engine")

_model = None
_engine: InferenceEngine | None = None


class InferenceEngine:
    """Serializes GLiNER onto one worker and batches arrivals that share labels.

    The model is not documented as thread-safe, so only this thread touches it.
    Concurrent HTTP handlers block on a Future instead of holding a global lock
    through the whole round-trip, which lets the worker group them.
    """

    def __init__(self, model: Any, max_batch: int = 8, max_wait: float = 0.02):
        self._model = model
        self._max_batch = max_batch
        self._max_wait = max_wait
        self._q: queue.Queue[_Job] = queue.Queue()
        t = threading.Thread(target=self._run, name="gliner-worker", daemon=True)
        t.start()

    def predict(self, text: str, labels: list[str], threshold: float) -> list[dict]:
        job = _Job(text, labels, threshold)
        self._q.put(job)
        return job.future.result()

    def _run(self) -> None:
        while True:
            first = self._q.get()
            batch = [first]
            deadline = time.monotonic() + self._max_wait
            while len(batch) < self._max_batch:
                timeout = deadline - time.monotonic()
                if timeout <= 0:
                    break
                try:
                    batch.append(self._q.get(timeout=timeout))
                except queue.Empty:
                    break
            self._flush(batch)

    def _flush(self, batch: list[_Job]) -> None:
        groups: dict[tuple, list[_Job]] = defaultdict(list)
        for job in batch:
            groups[(tuple(job.labels), job.threshold)].append(job)
        for (labels, threshold), jobs in groups.items():
            texts = [j.text for j in jobs]
            try:
                results = self._infer(texts, list(labels), threshold)
            except Exception as exc:  # noqa: BLE001 — surface any model failure to waiters
                for job in jobs:
                    job.future.set_exception(exc)
                continue
            if len(results) != len(jobs):
                err = RuntimeError(f"model returned {len(results)} results for {len(jobs)} texts")
                for job in jobs:
                    job.future.set_exception(err)
                continue
            for job, found in zip(jobs, results, strict=True):
                job.future.set_result(found)

    def _infer(self, texts: list[str], labels: list[str], threshold: float) -> list[list[dict]]:
        batch_fn = getattr(self._model, "batch_predict_entities", None)
        if batch_fn is not None and len(texts) > 1:
            return batch_fn(texts, labels, threshold=threshold)
        return [self._model.predict_entities(t, labels, threshold=threshold) for t in texts]


class _Job:
    __slots__ = ("text", "labels", "threshold", "future")

    def __init__(self, text: str, labels: list[str], threshold: float):
        self.text = text
        self.labels = labels
        self.threshold = threshold
        self.future: Future = Future()


@asynccontextmanager
async def lifespan(app: FastAPI):
    """Load the weights once, before the first request is served."""
    global _model, _engine
    from gliner import GLiNER

    name = model_name()
    log.info("loading %s", name)
    _model = GLiNER.from_pretrained(name)
    _engine = InferenceEngine(
        _model,
        max_batch=int(os.environ.get("SHINEL_ML_BATCH", "8")),
        max_wait=float(os.environ.get("SHINEL_ML_BATCH_WAIT", "0.02")),
    )
    log.info("model ready")
    yield
    _engine = None
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
    if not req.text or not req.labels or _engine is None:
        return []

    found = _engine.predict(req.text, req.labels, req.threshold)
    return [
        Entity(
            entity=e["text"],
            label=e["label"],
            start=e["start"],
            end=e["end"],
        )
        for e in found
    ]
