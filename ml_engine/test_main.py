"""Contract test for /analyze.

The Go client in internal/analyzer/mlclient.go parses exactly this JSON, so the
payload here is the one its test feeds back. No weights are downloaded: TestClient
is used without a context manager, which skips the lifespan that loads GLiNER,
and a stub is installed in its place.
"""

import os
import threading
from concurrent.futures import ThreadPoolExecutor

import main
from fastapi.testclient import TestClient

client = TestClient(main.app)


class StubModel:
    """Stands in for GLiNER, returning whatever it was told to."""

    def __init__(self, entities):
        self.entities = entities
        self.calls = []

    def predict_entities(self, text, labels, threshold=0.5):
        self.calls.append((text, labels, threshold))
        return self.entities


def install(entities):
    stub = StubModel(entities)
    main._model = stub
    # max_wait=0 keeps these tests serial: the worker never waits for a batch.
    main._engine = main.InferenceEngine(stub, max_batch=1, max_wait=0)
    return stub


def test_renames_text_to_entity():
    # GLiNER's key is "text"; the API must expose it as "entity".
    install([{"text": "Иван", "label": "PERSON", "start": 0, "end": 4, "score": 0.99}])

    resp = client.post("/analyze", json={"text": "Иван ушёл", "labels": ["PERSON"]})

    assert resp.status_code == 200
    assert resp.json() == [{"entity": "Иван", "label": "PERSON", "start": 0, "end": 4}]


def test_offsets_are_characters_not_bytes():
    # "Иван" is 4 characters but 8 UTF-8 bytes. The API promises characters and
    # the Go client converts; handing back bytes would corrupt its slice.
    install([{"text": "Иван", "label": "PERSON", "start": 0, "end": 4, "score": 0.9}])

    resp = client.post("/analyze", json={"text": "Иван", "labels": ["PERSON"]})

    assert resp.json()[0]["end"] == len("Иван") == 4
    assert len("Иван".encode()) == 8


def test_score_is_not_exposed():
    install([{"text": "x", "label": "L", "start": 0, "end": 1, "score": 0.7}])

    resp = client.post("/analyze", json={"text": "x", "labels": ["L"]})

    assert "score" not in resp.json()[0]


def test_empty_labels_skip_the_model():
    stub = install([{"text": "x", "label": "L", "start": 0, "end": 1}])

    resp = client.post("/analyze", json={"text": "Иван", "labels": []})

    assert resp.json() == []
    assert stub.calls == []


def test_empty_text_skips_the_model():
    stub = install([{"text": "x", "label": "L", "start": 0, "end": 1}])

    resp = client.post("/analyze", json={"text": "", "labels": ["PERSON"]})

    assert resp.json() == []
    assert stub.calls == []


def test_unloaded_model_returns_no_entities():
    main._model = None
    main._engine = None

    resp = client.post("/analyze", json={"text": "Иван", "labels": ["PERSON"]})

    assert resp.status_code == 200
    assert resp.json() == []


def test_threshold_is_passed_through():
    stub = install([])

    client.post("/analyze", json={"text": "hi", "labels": ["P"], "threshold": 0.8})

    assert stub.calls[0][2] == 0.8


def test_threshold_defaults_and_is_bounded():
    stub = install([])

    client.post("/analyze", json={"text": "hi", "labels": ["P"]})
    assert stub.calls[0][2] == 0.5

    bad = client.post("/analyze", json={"text": "hi", "labels": ["P"], "threshold": 2})
    assert bad.status_code == 422


def test_rejects_missing_fields():
    install([])

    assert client.post("/analyze", json={"text": "hi"}).status_code == 422
    assert client.post("/analyze", json={"labels": ["PERSON"]}).status_code == 422


def test_health_reports_model_state():
    install([])
    assert client.get("/health").json() == {"status": "ok"}

    main._model = None
    main._engine = None
    assert client.get("/health").json() == {"status": "loading"}


def test_engine_batches_same_labels():
    recorded = {}

    class Rec:
        def batch_predict_entities(self, texts, labels, threshold=0.5):
            recorded["texts"] = list(texts)
            return [[] for _ in texts]

        def predict_entities(self, *args, **kwargs):
            raise AssertionError("single-text path should not run for a batch")

    eng = main.InferenceEngine(Rec(), max_batch=8, max_wait=0.3)
    barrier = threading.Barrier(2)

    def go(text):
        barrier.wait()
        return eng.predict(text, ["PERSON"], 0.5)

    with ThreadPoolExecutor(max_workers=2) as pool:
        f1 = pool.submit(go, "alpha")
        f2 = pool.submit(go, "beta")
        assert f1.result() == []
        assert f2.result() == []

    assert set(recorded.get("texts", ())) == {"alpha", "beta"}


def test_model_name_from_yaml(tmp_path, monkeypatch):
    import settings

    monkeypatch.delenv("SHINEL_ML_MODEL", raising=False)
    p = tmp_path / "config.yaml"
    p.write_text("ml_engine:\n  url: \"\"\n  model: org/from-yaml\n  timeout_ms: 1\n")
    assert settings.model_name(str(p)) == "org/from-yaml"


def test_model_name_nested_model_key_does_not_win(tmp_path, monkeypatch):
    import settings

    monkeypatch.delenv("SHINEL_ML_MODEL", raising=False)
    p = tmp_path / "config.yaml"
    p.write_text(
        "ml_engine:\n"
        "  tokenizer:\n"
        "    model: org/nested\n"
        "  model: org/real\n"
    )
    assert settings.model_name(str(p)) == "org/real"


def test_model_name_quoted_hash_and_spaced_key(tmp_path, monkeypatch):
    import settings

    monkeypatch.delenv("SHINEL_ML_MODEL", raising=False)
    p = tmp_path / "config.yaml"
    p.write_text('ml_engine:\n  model : "org/foo#bar"\n')
    assert settings.model_name(str(p)) == "org/foo#bar"


def test_model_name_env_overrides_yaml(tmp_path, monkeypatch):
    import settings

    p = tmp_path / "config.yaml"
    p.write_text("ml_engine:\n  model: org/from-yaml\n")
    monkeypatch.setenv("SHINEL_ML_MODEL", "org/from-env")
    assert settings.model_name(str(p)) == "org/from-env"


def test_model_name_missing_file_uses_default(monkeypatch):
    import settings

    monkeypatch.delenv("SHINEL_ML_MODEL", raising=False)
    assert settings.model_name("/no/such/config.yaml") == settings.DEFAULT_MODEL


def test_apply_build_token_strips_quotes(tmp_path, monkeypatch):
    import settings

    monkeypatch.delenv("HF_TOKEN", raising=False)
    monkeypatch.delenv("HUGGING_FACE_HUB_TOKEN", raising=False)
    p = tmp_path / "hf_token"
    p.write_text('  "hf_test"\n')
    assert settings.apply_build_token(str(p)) is True
    assert os.environ["HF_TOKEN"] == "hf_test"
    assert os.environ["HUGGING_FACE_HUB_TOKEN"] == "hf_test"


def test_apply_build_token_missing_file(monkeypatch):
    import settings

    monkeypatch.delenv("HF_TOKEN", raising=False)
    assert settings.apply_build_token("/no/such/secret") is False
