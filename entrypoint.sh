#!/bin/sh
set -e
# 以 root 启动（Docker 默认）时：确保数据目录可写后降权到 nobody 运行。
# 这兼容 named volume（初始 root 所有）与宿主绑定挂载（权限未知）两种持久化方式。
if [ "$(id -u)" = "0" ]; then
  mkdir -p "$DATA_DIR"
  chown -R 65534:65534 "$DATA_DIR"
  exec su-exec 65534:65534 /model-uptime "$@"
fi
exec /model-uptime "$@"
