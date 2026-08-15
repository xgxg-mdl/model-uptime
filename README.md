# model-uptime

大模型 API 探针 + 终端风格状态页。复刻终端风格大模型探针状态页的核心体验，并在此基础上提供：

- **多协议探针**：`chat`（OpenAI Chat Completions）、`response`（OpenAI Responses）、`message`（Anthropic Messages）、`http`（通用 HTTP），适配器架构便于扩展
- **终端风格状态页**：60s 自动探测、60 根历史状态条、uptime% / samples / latency、悬停 tooltip 错误详情、5s 轮询
- **Telegram 聚合通知**：按订阅合并同一调度轮的多模型变化，仅在异常/恢复切换时发送可自定义 HTML 消息
- **配置页**：在线管理监控目标与页面显示配置（标题、历史窗口、统计维度开关），修改即时热重载
- **Docker Compose 一键部署**：Go 单二进制 + SQLite，最终镜像约 15MB

## 服务器部署（预构建镜像）

服务器无需安装 Go，也无需克隆源码。创建部署目录并准备 Compose 文件：

```bash
mkdir model-uptime && cd model-uptime
curl -fsSLO https://raw.githubusercontent.com/xgxg-mdl/model-uptime/main/docker-compose.yml
curl -fsSLO https://raw.githubusercontent.com/xgxg-mdl/model-uptime/main/.env.example
mv .env.example .env
```

编辑 `.env`，至少设置一个强管理员密码；然后拉取并启动镜像：

```bash
docker compose pull
docker compose up -d
```

- 镜像：`ghcr.io/xgxg-mdl/model-uptime:latest`
- 状态页：`http://<服务器>:8080/`
- 配置页：`http://<服务器>:8080/admin/`

首次启动自动生成 `/data/config.yaml`（空服务列表）。在配置页添加监控目标，或直接编辑配置文件后重启。

### 发布镜像

镜像只在推送版本 tag 时构建。发布新版本：

```bash
git tag v0.1.0
git push origin v0.1.0
```

tag 推送后 GitHub Actions 自动构建并发布到 GHCR，生成以下标签：

- `ghcr.io/xgxg-mdl/model-uptime:latest`
- `ghcr.io/xgxg-mdl/model-uptime:0.1.0`
- `ghcr.io/xgxg-mdl/model-uptime:0.1`
- `ghcr.io/xgxg-mdl/model-uptime:0`

服务器固定版本：在 `.env` 中设置 `MODEL_UPTIME_TAG=0.1.0`，然后 `docker compose pull && docker compose up -d`。

常用运维命令：

```bash
docker compose logs -f                 # 查看日志
docker compose pull && docker compose up -d   # 更新镜像
docker compose restart                 # 重启
```

配置和探测历史保存在 Docker 命名卷 `model-uptime-data` 中。升级镜像不会删除数据；不要执行 `docker compose down -v`，除非确认要删除所有配置和历史。

> 镜像内以非 root（nobody, uid 65534）运行。使用命名卷时 entrypoint 会自动初始化目录权限；若改用绑定挂载（`./data:/data`），需保证宿主目录对 uid 65534 可写：`sudo chown -R 65534:65534 data`。

> 管理密码优先级：环境变量 `ADMIN_TOKEN` > 配置文件 `admin_token`。两者均为空（默认）时，**首次访问 `/admin/` 会在页面设置管理密码**，设置后写入 `/data/config.yaml` 持久化，之后用该密码登录。

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

telegram:
  bot_token: "123456789:replace-with-your-bot-token"
  subscriptions:
    - id: operations     # 订阅唯一 ID
      name: Operations
      enabled: true
      chat_id: "-1001234567890"
      language: zh-CN     # zh-CN（默认）| en-US
      service_ids:       # 可包含 enabled: false 的服务
        - openai-gpt54
      template: ""       # 留空使用默认聚合模板

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

## Telegram 聚合订阅

Telegram 通知与探针的 `enabled` 开关独立：订阅可以选择配置中的任意服务，包括当前已禁用的服务。禁用期间该服务不会被探测，也不会产生空通知；重新启用后，它会在实际状态发生变化时继续参与订阅。

通知规则：

- 首次有效探测只建立状态基线，不发送通知。
- 只有 `up → down` 和 `down → up` 会发送；持续异常或持续正常不重复发送。
- 同一调度轮中，每个订阅最多发送一条消息，异常模型和恢复模型分区展示。
- 管理页的手动测试连接使用 3 秒聚合窗口，连续测试多个模型时不会逐条刷屏。

每个订阅可通过 `language` 独立选择 `zh-CN` 或 `en-US`，未配置时默认使用中文。`template` 留空时使用对应语言的内置模板；已有自定义模板不会被语言设置覆盖。模板采用 Go `html/template`，通知以 Telegram HTML 模式发送，变量值自动转义，最长 4096 个字符。默认模板使用北京时间，按模型展示异常持续时间、确认时间，以及当天运行时间、异常时间、异常次数和基于已观测时间计算的可用率；同一轮多个模型变化仍合并在一条消息中。

| 模板变量 | 说明 |
|---|---|
| `.ChangedAt` | 该批状态变化时间（北京时间） |
| `.DownCount` / `.RecoveryCount` / `.TotalChanges` | 异常数、恢复数和总变化数 |
| `.DownModels` / `.RecoveredModels` | 异常模型列表和恢复模型列表 |
| `.Changes` | 本条订阅消息中的全部状态变化 |

`.DownModels`、`.RecoveredModels` 和 `.Changes` 的每个元素包含 `.ServiceID`、`.Model`、`.Provider`、`.Protocol`、`.OK`、`.LatencyMS`、`.Error`、`.UptimePct`、`.Samples`、`.PreviousStatus`、`.Status`、`.LastTS`、`.OutageDurationSec`、`.TodayUpSec`、`.TodayDownSec`、`.TodayDownCount` 和 `.TodayUptimePct`。时间统计以北京时间零点为边界；相邻探测之间的时长归属于前一次已知状态。模板函数 `formatBeijing`、`beijingDate`、`durationCN` 和 `durationEN` 可用于格式化时间。完整默认模板见 [config.example.yaml](config.example.yaml)。

## 配置页

`/admin/`，用管理密码登录（会话内有效）。首次部署未设置密码时，进入页面会提示**设置管理密码**，之后用该密码登录。功能：

- **监控服务**：增删改、测试连接（立即探测一次并显示结果）、启停
- **页面显示**：标题、副标题、探针注释、历史窗口、轮询间隔、四个统计维度开关
- **Telegram 订阅**：Bot Token 脱敏编辑、订阅增删改/启停、多模型选择、聚合模板编辑与测试发送
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
| `/api/admin/telegram` | GET / PUT | Bearer | 获取（Token 脱敏）/ 更新 Telegram 配置 |
| `/api/admin/telegram/test` | POST | Bearer | 按 `{"subscription_id": "operations"}` 同步发送聚合测试消息 |

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
  notifier/          Telegram 聚合模板、异步发送与失败重试
  api/               HTTP 路由、管理 API、token 认证、embed 前端
    web/             前端：状态页（复刻）+ 配置页 + JetBrains Mono 字体
```

## 测试

```bash
go test ./...
```

覆盖：各协议探测成功/失败/超时/路径拼接/鉴权头、配置加载与校验、SQLite 读写、调度轮次聚合、Telegram 模板与重试、管理 API 全流程（认证、CRUD、密钥保留、热重载）。
