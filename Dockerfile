# ---- 构建阶段 ----
FROM golang:1.25-alpine AS build
WORKDIR /src

# 先拷贝依赖清单，利用层缓存
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# CGO_ENABLED=0：modernc.org/sqlite 为纯 Go，产出静态二进制
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/model-uptime ./cmd/server \
 && mkdir -p /data && chown 65534:65534 /data

# ---- 运行阶段：alpine + entrypoint 处理卷权限，主程序仍以 nobody 运行 ----
FROM alpine:3.20
# ca-certificates：探针必须能验证 https API 的证书
# su-exec：root 启动后降权到 nobody（65534）
RUN apk add --no-cache ca-certificates su-exec
COPY --from=build /out/model-uptime /model-uptime
COPY --from=build /data /data
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENV DATA_DIR=/data PORT=8080
EXPOSE 8080
ENTRYPOINT ["/entrypoint.sh"]
