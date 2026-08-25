"""Contract test for /analyze.

The Go client in internal/analyzer/mlclient.go parses exactly this JSON, so the
payload here is the one its test feeds back. No weights are downloaded: TestClient
is used without a context manager, which skips the lifespan that loads GLiNER,
and a stub is installed in its place.
"""

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
    assert client.get("/health").json() == {"status": "loading"}
