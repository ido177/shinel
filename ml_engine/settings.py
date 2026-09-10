"""Sidecar settings. The HuggingFace id lives in the same yaml the Go proxy loads."""

from __future__ import annotations

import os

DEFAULT_MODEL = "urchade/gliner_multi-v2.1"
_BUILD_SECRET = "/run/secrets/hf_token"


def apply_build_token(secret_path: str = _BUILD_SECRET) -> bool:
    """Load a Hub token from a BuildKit secret file. Returns whether one was set.

    The token is never printed. Call this before from_pretrained in the image bake.
    """
    try:
        raw = open(secret_path, encoding="utf-8").read()
    except OSError:
        raw = ""
    token = raw.strip().strip("'").strip('"').strip()
    if not token:
        return False
    os.environ["HF_TOKEN"] = token
    os.environ["HUGGING_FACE_HUB_TOKEN"] = token
    return True


def model_name(path: str | None = None) -> str:
    # Env wins so a bake/rebuild can pin a model without editing the file.
    if env := os.environ.get("SHINEL_ML_MODEL", "").strip():
        return env
    if path:
        return _model_from_yaml(path) or DEFAULT_MODEL
    for candidate in _config_paths():
        if name := _model_from_yaml(candidate):
            return name
    return DEFAULT_MODEL


def _config_paths() -> list[str]:
    paths: list[str] = []
    if env := os.environ.get("SHINEL_CONFIG", "").strip():
        paths.append(env)
    paths.append("/etc/shinel/config.yaml")
    here = os.path.dirname(os.path.abspath(__file__))
    paths.append(os.path.normpath(os.path.join(here, "..", "config.yaml")))
    return paths


def _model_from_yaml(path: str) -> str:
    try:
        lines = open(path, encoding="utf-8").read().splitlines()
    except OSError:
        return ""
    in_ml = False
    for line in lines:
        stripped = line.split("#", 1)[0].rstrip()
        if not stripped.strip():
            continue
        indent = len(stripped) - len(stripped.lstrip(" "))
        keyval = stripped.strip()
        if indent == 0:
            in_ml = keyval == "ml_engine:" or keyval.startswith("ml_engine:")
            continue
        if in_ml and keyval.startswith("model:"):
            return _scalar(keyval[len("model:") :])
    return ""


def _scalar(raw: str) -> str:
    s = raw.strip()
    if len(s) >= 2 and s[0] == s[-1] and s[0] in "\"'":
        return s[1:-1]
    return s
