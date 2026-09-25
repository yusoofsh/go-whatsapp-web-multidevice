# syntax=docker/dockerfile:1
FROM golang:1.26-alpine3.23 AS builder
RUN apk add --no-cache gcc musl-dev
WORKDIR /build
COPY src/go.mod src/go.sum ./
RUN go mod download
COPY src/ ./
RUN CGO_ENABLED=1 go build -trimpath -ldflags='-w -s' -o /app/whatsapp .

FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata ffmpeg libwebp-tools poppler-utils su-exec \
    && addgroup -g 20000 gowagroup \
    && adduser -D -u 20001 -G gowagroup gowauser
WORKDIR /app
COPY --from=builder /app/whatsapp /app/whatsapp
COPY docker/entrypoint.sh /entrypoint.sh
RUN chmod 755 /entrypoint.sh \
    && mkdir -p /app/storages /app/statics \
    && chown -R gowauser:gowagroup /app
ENV MCP_ENABLED=true MCP_STREAMING_ENABLED=true MCP_STREAM_PORT=3001
EXPOSE 3001
LABEL org.opencontainers.image.source="https://github.com/yusoofsh/go-whatsapp-web-multidevice" \
      org.opencontainers.image.description="Native Go GOWA with OAuth, MCP history, private attachments and durable event streaming" \
      org.opencontainers.image.licenses="MIT"
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:3001/healthz || exit 1
ENTRYPOINT ["/entrypoint.sh"]
CMD ["rest"]
