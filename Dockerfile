FROM golang:1.24-bookworm AS builder

ARG ONNXRUNTIME_VERSION=1.23.0
ARG TARGETARCH=amd64

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

# onnxruntime powers background removal. It is loaded at runtime, so the
# service still starts if this step is removed.
RUN set -eux; \
    case "${TARGETARCH}" in \
      amd64) ort_arch=x64 ;; \
      arm64) ort_arch=aarch64 ;; \
      *) echo "unsupported architecture ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /tmp/onnxruntime.tgz \
      "https://github.com/microsoft/onnxruntime/releases/download/v${ONNXRUNTIME_VERSION}/onnxruntime-linux-${ort_arch}-${ONNXRUNTIME_VERSION}.tgz"; \
    mkdir -p /opt/onnxruntime; \
    tar -xzf /tmp/onnxruntime.tgz -C /opt/onnxruntime --strip-components=1; \
    rm /tmp/onnxruntime.tgz

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -o image-processor .

FROM debian:bookworm-slim

# The AVIF, WebP, and JPEG XL encoders use these shared libraries when present
# and fall back to bundled WASM builds otherwise, which is several times slower.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates tzdata libjxl-dev libavif-dev libwebp-dev \
    && rm -rf /var/lib/apt/lists/*

RUN useradd --create-home --shell /usr/sbin/nologin appuser \
    && mkdir -p /app/cache /app/models /app/uploads

WORKDIR /app

COPY --from=builder /app/image-processor .
COPY --from=builder /opt/onnxruntime/lib/libonnxruntime.so* /opt/onnxruntime/lib/

RUN chown -R appuser:appuser /app

USER appuser

VOLUME ["/app/cache", "/app/models", "/app/uploads"]

EXPOSE 8080

ENV GIN_MODE=release \
    ONNXRUNTIME_SHARED_LIBRARY=/opt/onnxruntime/lib/libonnxruntime.so \
    IM_CACHE_DIR=/app/cache \
    IM_MODEL_DIR=/app/models \
    IM_UPLOAD_DIR=/app/uploads

# Source hosts are denied by default; set IM_ALLOWED_HOSTS for your origins.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
    CMD ["./image-processor", "-healthcheck"]

CMD ["./image-processor"]
