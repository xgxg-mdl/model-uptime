# model-uptime

大模型 API 探针 + 终端风格状态页。复刻终端风格大模型探针状态页的核心体验，并在此基础上提供：

- **多协议探针**：`chat`（OpenAI Chat Completions）、`response`（OpenAI Responses）、`message`（Anthropic Messages）、`http`（通用 HTTP），适配器架构便于扩展
- **终端风格状态页**：60s 自动探测、60 根历史状态条、uptime% / samples / latency、悬停 tooltip 错误详情、5s 轮询
- **配置页**：在线管理监控目标与页面显示配置（标题、历史窗口、统计维度开关），修改即时热重载
- **Docker Compose 一键部署**：Go 单二进制 + SQLite，最终镜像约 15MB

## 服务器部署（Docker Compose）

在服务器上克隆仓库后：

```bash
cp .env.example .env        # 修改 ADMIN_TOKEN
docker compose up -d --build
```

- 状态页：`http://<服务器>:8080/`
- 配置页：`http://<服务器>:8080/admin/`

首次启动自动生成 `data/config.yaml`（空服务列表）。在配置页添加监控目标，或直接编辑配置文件后重启。

常用运维命令：

```bash
docker compose logs -f          # 查看日志
docker compose restart          # 重启
docker compose pull && docker compose up -d   # 更新镜像
```

> 镜像内以非 root（nobody, uid 65534）运行。使用命名卷时 entrypoint 会自动初始化目录权限；若改用绑定挂载（`./data:/data`），需保证宿主目录对 uid 65534 可写：`sudo chown -R 65534:65534 data`。

> 管理密码优先级：环境变量 `ADMIN_TOKEN` > 配置文件 `admin_token`。两者均为空（默认）时，**首次访问 `/admin/` 会在页面设置管理密码**，设置后写入 `data/config.yaml` 持久化，之后用该密码登录。也可直接在 `.env` 或配置文件里预设。

## 本地运行

```bash
# 需要 Go 1.25+
go run ./cmd/server                # 默认 :8080，数据目录 ./data
ADMIN_TOKEN=xxx go run ./cmd/server --port 9090 --data ./data
```

## 支持的协议

| 协议 | 请求 | 可用判定 |
|---|---|---|
| `chat` | `POST {base_url}/v1/chat/completions` | HTTP 2xx + JSON 含 `choices` |
| `response` | `POST {base_url}/v1/responses` | HTTP 2xx + JSON 含 `output` |
| `message` | `POST {base_url}/v1/messages` | HTTP 2xx + JSON 含 `content` |
| `http` | 按配置的 method/headers/body | 状态码等于 `expect_status` |

`base_url` 可含或不含 `/v1`，自动补全；`path` 字段可完全覆盖默认端点。失败原因（连接错误、HTTP 状态码、JSON 校验失败、API 层 `error` 字段）会脱敏后展示在状态页 tooltip。

## 配置文件

完整示例见 [config.example.yaml](config.example.yaml)。核心结构：

```yaml
admin_token: ""          # 建议用环境变量 ADMIN_TOKEN 代替

page:                    # 状态页显示配置
  title: "model-uptime // status"
  subtitle: "model-uptime"
  probe_comment: "model-uptime service monitor · probing every 60s"
  history_len: 60        # 状态条数量 / 历史窗口
  refresh_sec: 5         # 状态页轮询间隔
  show_uptime: true      # ↓ 统计维度开关
  show_samples: true
  show_latency: true
  show_avg_load: true

services:                # 监控目标
  - id: openai-gpt54     # 唯一 ID，创建后不可改
    name: gpt-5.4        # 状态页显示名
    provider: OpenAI     # 展示用标签
    protocol: chat       # chat | response | message | http
    model: gpt-5.4       # 发给 API 的模型 ID（http 协议无需）
    base_url: https://api.openai.com/v1
    api_key: sk-xxx      # 留空则探测时不带鉴权头
    interval_sec: 60
    timeout_sec: 15
    enabled: true
```

`http` 协议额外支持 `method` / `headers`（JSON 对象）/ `body` / `expect_status`。

## 配置页

`/admin/`，用管理密码登录（会话内有效）。首次部署未设置密码时，进入页面会提示**设置管理密码**，之后用该密码登录。功能：

- **监控服务**：增删改、测试连接（立即探测一次并显示结果）、启停
- **页面显示**：标题、副标题、探针注释、历史窗口、轮询间隔、四个统计维度开关
- **API Key 脱敏**：列表只显示掩码；编辑时留空即保留原密钥

所有修改原子写回配置文件并即时热重载，无需重启。

## HTTP API

| 端点 | 方法 | 认证 | 说明 |
|---|---|---|---|
| `/api/status` | GET | 公开 | 状态页数据（保持状态 API 的稳定数据结构） |
| `/api/admin/setup-status` | GET | 公开 | 管理密码是否已配置（前端选择登录/设置视图） |
| `/api/admin/setup` | POST | — | 首次设置管理密码（仅未配置时可用） |
| `/api/admin/login` | POST | — | `{token}` 校验 |
| `/api/admin/services` | GET / POST | Bearer | 列表 / 新增 |
| `/api/admin/services/{id}` | PUT / DELETE | Bearer | 更新 / 删除 |
| `/api/admin/services/{id}/test` | POST | Bearer | 立即探测一次 |
| `/api/admin/page` | GET / PUT | Bearer | 页面显示配置 |

## 存储与数据

- `data/config.yaml` — 配置源（配置页在线修改落盘于此）
- `data/probe.db` — SQLite 探测历史（用于重启恢复状态条），保留 30 天

Docker 部署使用命名卷持久化 `/data`。若改用绑定挂载，需确保宿主目录对容器非 root 用户（uid 65534）可写：

```bash
mkdir -p data && sudo chown -R 65534:65534 data
```

## 项目结构

```
cmd/server/          入口：装配配置 / 存储 / 调度器 / HTTP
internal/
  model/             领域模型（服务定义、探测结果、页面配置）
  config/            YAML 配置加载 / 校验 / 原子写回
  store/             SQLite 历史持久化（纯 Go，无 cgo）
  prober/            协议探针适配器（chat / response / message / http）
  scheduler/         1s 级调度、并发探测、历史窗口、聚合快照
  api/               HTTP 路由、管理 API、token 认证、embed 前端
    web/             前端：状态页（复刻）+ 配置页 + JetBrains Mono 字体
```

## 测试

```bash
go test ./...
```

覆盖：各协议探测成功/失败/超时/路径拼接/鉴权头、配置加载与校验、SQLite 读写、调度与聚合、管理 API 全流程（认证、CRUD、密钥保留、热重载）。
