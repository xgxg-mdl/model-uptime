# ---- 构建阶段 ----
FROM golang:1.25.13-alpine3.23 AS build
WORKDIR /src

# 先拷贝依赖清单，利用层缓存
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
# CGO_ENABLED=0：modernc.org/sqlite 为纯 Go，产出静态二进制
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildTime=${BUILD_TIME}" \
    -o /out/model-uptime ./cmd/model-uptime \
 && mkdir -p /data && chown 65534:65534 /data

# ---- 运行阶段：alpine + entrypoint 处理卷权限，主程序仍以 nobody 运行 ----
FROM alpine:3.23.3
LABEL org.opencontainers.image.title="model-uptime" \
      org.opencontainers.image.description="Model API uptime monitoring and status page" \
      org.opencontainers.image.source="https://github.com/xgxg-mdl/model-uptime"
# ca-certificates：探针必须能验证 https API 的证书
# su-exec：root 启动后降权到 nobody（65534）
RUN apk add --no-cache ca-certificates su-exec
COPY --from=build /out/model-uptime /model-uptime
COPY --from=build /data /data
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENV DATA_DIR=/data PORT=8080
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=4 \
  CMD wget -q -T 3 -O /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/entrypoint.sh"]
