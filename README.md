# 🧥 Shinel: AI Privacy Proxy

![License](https://img.shields.io/badge/license-MIT-blue.svg)
![Go Version](https://img.shields.io/badge/go-1.25-cyan)
![Python Version](https://img.shields.io/badge/python-3.12-yellow)
![Docker](https://img.shields.io/badge/docker-ready-blue)

**Shinel** is a transparent, self-hosted AI Privacy Proxy designed for secure enterprise integration with ChatGPT and other LLMs. 

It intercepts traffic between your application (or AI agents) and the LLM API, locally redacts PII and custom dictionary terms, and sends synthetic tokens to the cloud. Upon receiving the LLM's response, Shinel seamlessly restores the real data. 

**Zero code changes required** — just update your config file.

## ✨ Features

- 🔌 **Drop-in Replacement:** Fully compatible with OpenAI API format. Just change your endpoint to `http://localhost:8080/v1`.
- ⚡ **Streaming Support:** Seamlessly handles Server-Sent Events (SSE). Tokens are unmasked on the fly without breaking the streaming experience.
- 🛡️ **Multi-Layer Detection Engine:**
  - **Regex & Checksums:** Emails, IPv4 addresses, and credit cards (Luhn). JSON bodies are walked as a tree — only string values are masked, so numeric fields stay valid JSON.
  - **Custom Dictionaries:** Case-insensitive whole-word matching (Aho-Corasick) for internal project names or employee lists.
  - **Zero-Shot ML (GLiNER):** Optional Python sidecar for names, organizations, and custom labels. If the sidecar is down, regex and dictionary masking still run; names only the model would have caught can then leave the process.
- 🔒 **100% Local & Self-Hosted:** Your sensitive data never leaves your infrastructure until it's masked.

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

## 🤝 Contributions are always welcome!