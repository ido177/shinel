FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0

RUN go build -trimpath -ldflags="-s -w" -o /out/shinel ./cmd/shinel

# distroless/static includes the CA bundle the proxy needs for
# https://api.openai.com; scratch would fail TLS verification.
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/shinel /shinel
ENTRYPOINT ["/shinel", "-config", "/etc/shinel/config.yaml"]
