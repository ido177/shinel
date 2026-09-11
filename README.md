# 🧥 Shinel: AI Privacy Proxy

![License](https://img.shields.io/badge/license-MIT-blue.svg)
![Go Version](https://img.shields.io/badge/go-1.25-cyan)
![Python Version](https://img.shields.io/badge/python-3.12-yellow)
![Docker](https://img.shields.io/badge/docker-ready-blue)

**Shinel** is a transparent, self-hosted AI Privacy Proxy designed for secure enterprise integration with ChatGPT and other LLMs. 

It intercepts traffic between your application (or AI agents) and the LLM API, locally redacts PII and custom dictionary terms, and sends synthetic tokens to the cloud. Upon receiving the LLM's response, Shinel seamlessly restores the real data. 

**Zero code changes required** — just update your config file.

## ✨ Features

- 🔌 **Drop-in Replacement:** Point the SDK at `http://localhost:8080/openai/v1`, `/anthropic/v1`, or `/gemini` (native paths after the prefix).
- ⚡ **Streaming Support:** Seamlessly handles Server-Sent Events (SSE). Tokens are unmasked on the fly without breaking the streaming experience.
- 🛡️ **Multi-Layer Detection Engine:**
  - **Regex & Checksums:** Emails, IPv4 addresses, and credit cards (Luhn). JSON bodies are walked as a tree — only string values are masked, so numeric fields stay valid JSON.
  - **Custom Dictionaries:** Case-insensitive whole-word matching (Aho-Corasick) for internal project names or employee lists.
  - **Zero-Shot ML (GLiNER):** Optional Python sidecar for names, organizations, and custom labels. If the sidecar is down, regex and dictionary masking still run; names only the model would have caught can then leave the process.
- 🔒 **100% Local & Self-Hosted:** Your sensitive data never leaves your infrastructure until it's masked.
- 🖥️ **Local dashboard:** Live process logs, the loaded config, and recent mask mappings at `http://127.0.0.1:8081` (loopback is open; Docker asks for the dashboard password).
- 📋 **Request logs:** Each proxied call logs method, path, upstream status, duration, and how many values were masked. `log_level` in yaml is `debug` / `info` / `warn` / `error` (default `info`). `debug` also logs each token; the real value is included only when `admin.redact` is `false`.

## 🏗️ How It Works

```text
+----------------+          +--------------------------------------+           +----------------+
|                |          |              Shinel                  |           |                |
|  AI Agent /    |  Prompt  |  +----------------+  +------------+  | Masked    |  OpenAI /      |
|  User App      | -------> |  | Go Proxy (API) |->| ML Sidecar |  | Prompt    |  Anthropic     |
| (Ivan, 5000$)  |          |  +----------------+  +------------+  | --------> |                |
|                |          |          | Vault (Memory/Redis)      |           |                |
|                | <------- |  +--------------------------------+  | <-------- |                |
|                |  Real    |  | De-masking (Streaming buffer)  |  | AI Reply  |                |
+----------------+  Reply   |  +--------------------------------+  |           +----------------+
                            +--------------------------------------+
```

## 🐳 Docker

The ML image bakes GLiNER weights at build time (`ml_engine.model` in `config.yaml`). Hugging Face throttles anonymous downloads, so that layer often stalls on `Fetching 5 files` with a warning about unauthenticated requests.

Set a Hub token **before** the first build. It is passed as a BuildKit secret for that step only and is **not** stored in the image.

```bash
# gitignored; Compose and Make both read this file
echo 'HF_TOKEN=hf_...' > .env
```

Or `export HF_TOKEN=hf_...` in the shell. Get a token at [huggingface.co/settings/tokens](https://huggingface.co/settings/tokens).

The bake log must contain `huggingface: authenticated`. If you see `huggingface: anonymous` or `unauthenticated requests to the HF Hub`, the secret did not reach the build — stop it and fix `.env` (no spaces around `=`).

```bash
make build   # shinel-proxy + shinel-ml-engine
make up      # proxy on :8080, dashboard on 127.0.0.1:8081, sidecar on the internal network
make down
```

`make itest` rebuilds the same ML image, so it needs a non-empty token. `make up` exports `HF_TOKEN` even when empty so Compose does not abort; an empty secret is the same anonymous bake as `Dockerfile.python` (`required=false`). After a failed or anonymous bake, rebuild so Docker does not reuse that layer (`make build` is enough when `Dockerfile.python` or `requirements.txt` changed).

## 🖥️ Dashboard

The UI is a second HTTP server (default `127.0.0.1:8081`), not a path on the LLM proxy.

**Laptop (`go run` / local binary)** — `config.yaml` already binds loopback, no password:

```bash
go run ./cmd/shinel -config config.yaml
```

Open [http://127.0.0.1:8081](http://127.0.0.1:8081). Proxy stays on `:8080`.

**Docker** — compose publishes `127.0.0.1:8081` and binds `0.0.0.0` inside the container, so a password is required (sibling containers on `shinel-net` must not scrape unmasked stats).

```bash
# optional: pin the password in gitignored .env
echo 'SHINEL_ADMIN_TOKEN=pick-a-long-secret' >> .env
make up
```

If `SHINEL_ADMIN_TOKEN` is unset, the proxy logs a generated one: `admin dashboard password=…`. Open [http://127.0.0.1:8081](http://127.0.0.1:8081) and sign in with that password. `admin.port: 0` in yaml turns the UI off. `admin.redact: false` shows credentials as-is in the Config tab and process logs (Stats mappings are always the real values).

## 🧪 Try it

Point the client at a provider prefix. The native path after the prefix is forwarded as-is (no OpenAI↔Anthropic translation). A path without a known prefix returns 404.

```bash
# OpenAI
curl http://127.0.0.1:8080/openai/v1/models \
  -H "Authorization: Bearer $OPENAI_API_KEY"

curl http://127.0.0.1:8080/openai/v1/chat/completions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [
      {
        "role": "user",
        "content": "Repeat these unchanged: ada@example.com, 8.8.8.8, Ivan Petrov"
      }
    ]
  }'

curl -N http://127.0.0.1:8080/openai/v1/chat/completions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o-mini",
    "stream": true,
    "messages": [
      {
        "role": "user",
        "content": "Repeat these unchanged: ada@example.com, 8.8.8.8, Ivan Petrov"
      }
    ]
  }'

# Anthropic
curl http://127.0.0.1:8080/anthropic/v1/messages \
  -H "x-api-key: $ANTHROPIC_API_KEY" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4-5",
    "max_tokens": 128,
    "messages": [
      {
        "role": "user",
        "content": "Repeat these unchanged: ada@example.com, 8.8.8.8, Ivan Petrov"
      }
    ]
  }'

# Gemini
curl "http://127.0.0.1:8080/gemini/v1beta/models/gemini-2.0-flash:generateContent" \
  -H "Content-Type: application/json" \
  -H "x-goog-api-key: $GEMINI_API_KEY" \
  -d '{
    "contents": [
      {
        "parts": [
          {
            "text": "Repeat these unchanged: ada@example.com, 8.8.8.8, Ivan Petrov"
          }
        ]
      }
    ]
  }'
```

Without a valid key the provider still returns 401, but Shinel has already masked the body — check the Stats tab. With a key, the model should echo the real values (unmasked on the way back), not `[EMAIL_1]` / `[IP_1]` / `[PERSON_1]`.

## 🤝 Contributions are always welcome!
