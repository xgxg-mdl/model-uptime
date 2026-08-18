# model-uptime

大模型 API 探针 + 终端风格状态页。复刻终端风格大模型探针状态页的核心体验，并在此基础上提供：

- **多协议探针**：`chat`（OpenAI Chat Completions）、`response`（OpenAI Responses）、`message`（Anthropic Messages）、`http`（通用 HTTP），适配器架构便于扩展
- **终端风格状态页**：首次加载命令输入动效、60s 自动探测、最近 60 个完整时间桶、uptime% / coverage / latency、悬停 tooltip 错误详情、5s 轮询
- **健康状态热力图**：按模型展示最近 1/7/30 个自然日的二维热力图（96/168/720 格），聚合正常、慢响应、失败和数据不足状态
- **Telegram 聚合通知与日报**：按订阅聚合模型变化，并在每日北京时间零点发送前一日运行摘要
- **配置页**：在线管理监控目标、页面显示配置和版本更新，修改即时热重载
- **一键更新**：检查 GitHub 稳定版本，并确认目标版本与 GHCR `latest` digest 一致后更新容器
- **Docker Compose 一键部署**：Go 单二进制 + SQLite，最终镜像约 15MB

## 服务器部署（预构建镜像）

服务器无需安装 Go，也无需克隆源码。创建部署目录并准备 Compose 文件：

```bash
mkdir model-uptime && cd model-uptime
umask 077
curl -fsSLO https://raw.githubusercontent.com/xgxg-mdl/model-uptime/main/docker-compose.yml
curl -fsSLO https://raw.githubusercontent.com/xgxg-mdl/model-uptime/main/.env.example
mv .env.example .env
```

编辑 `.env`；管理员密码可以首次访问管理页时设置。一键更新需要先生成内部令牌并写入 `.env`：

```bash
chmod 600 .env
sed -i '/^UPDATE_TOKEN=/d' .env
openssl rand -hex 32 | sed 's/^/UPDATE_TOKEN=/' >> .env
```

`UPDATE_TOKEN` 只在 Compose 内网中的主应用与更新 sidecar 之间使用，更新器端口不会暴露到宿主机。

> 更新 sidecar 需要访问 Docker Socket，该权限等同于管理宿主机上的容器。Compose 不向主应用挂载 Socket，并通过 label 将更新范围限制为 `model-uptime`。请保持 updater 镜像为项目固定的版本，不要自行改成浮动的 `latest`。

然后拉取并启动镜像：

```bash
docker compose pull
docker compose up -d
```

- 镜像：`ghcr.io/xgxg-mdl/model-uptime:latest`
- 状态页：`http://<服务器>:8080/`
- 热力图：`http://<服务器>:8080/heatmap/`
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

### 从旧版本启用一键更新

现有部署需要最后一次人工更新 Compose。数据卷不会受影响：

```bash
cd model-uptime
curl -fsSLo docker-compose.yml https://raw.githubusercontent.com/xgxg-mdl/model-uptime/main/docker-compose.yml
chmod 600 .env
sed -i '/^UPDATE_TOKEN=/d' .env
openssl rand -hex 32 | sed 's/^/UPDATE_TOKEN=/' >> .env
docker compose pull
docker compose up -d
```

确认 `.env` 中 `MODEL_UPTIME_TAG=latest`。固定版本标签用于锁定版本，因此管理页会显示版本信息但拒绝一键更新。迁移完成后，后续稳定版本可在 `/admin/` 的 **System Update** 面板完成检查和更新。

若新版本无法启动，可在 VPS 将 `.env` 中的 `MODEL_UPTIME_TAG` 改为上一个版本号，然后执行 `docker compose pull && docker compose up -d`。更新器不会主动删除旧镜像，但一键更新本身不承诺自动回滚。

## 本地运行

```bash
# 需要 Go 1.25.13
go run ./cmd/model-uptime                # 默认 :8080，数据目录 ./data
ADMIN_TOKEN=xxx go run ./cmd/model-uptime --port 9090 --data ./data
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

page:                    # 公开页面显示配置
  title: "model-uptime // status"
  subtitle: "model-uptime"
  probe_comment: "model-uptime · service health and performance" # 两页共用顶部注释
  public_url: "https://status.example.com/" # 留空则通知不显示链接
  history_len: 60        # 每个服务显示的完整时间桶数量
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
    warning_sec: 30       # 成功但耗时超过该值时显示 warning
    timeout_sec: 60
    enabled: true
```

`http` 协议额外支持 `method` / `headers`（JSON 对象）/ `body` / `expect_status`。

## Telegram 聚合订阅

Telegram 通知与探针的 `enabled` 开关独立：订阅可以选择配置中的任意服务，包括当前已禁用的服务。禁用期间该服务不会被探测，也不会产生空通知；重新启用后，它会在实际状态发生变化时继续参与订阅。

通知规则：

- 首次有效探测只建立状态基线，不发送通知。
- 只有 `up → down` 和 `down → up` 会发送；持续异常或持续正常不重复发送。
- 相邻状态变化使用 3 秒持久化聚合窗口；异常和恢复始终生成两类独立卡片，每张卡片只包含一种状态，超长列表按完整模型项稳定拆分。
- 探测结果与状态变化原子写入 SQLite，状态变化确认与通知入箱也在同一事务提交；进程重启后会继续处理未确认变化。
- Telegram `429`、网络错误和服务端故障按 `Retry-After` 与退避策略重试。连续四次确定性 `4xx` 会把该消息持久化隔离，避免阻塞同订阅的后续消息。
- 修改相关订阅的 Bot Token、Chat ID、模板、语言或服务筛选后，隔离项会恢复并按新配置重新渲染；停机编辑配置后重启同样生效。无关配置或相同 Telegram 配置不会打断临时错误的退避，也不会反复唤醒隔离项。

每个订阅可通过 `language` 独立选择 `zh-CN` 或 `en-US`，未配置时默认使用中文。`template` 留空时使用对应语言的内置模板；已有自定义模板不会被语言设置覆盖。模板采用 Go `html/template`，通知以 Telegram HTML 模式发送，变量值自动转义，最长 4096 个字符。默认实时模板采用“状态标题 + 引用区无序列表”结构，状态图标只出现在标题；异常原因保留在可展开详情中，恢复项展示故障时长与当前延迟。模型按服务的 `sort_order` 从小到大排列，相同值再按模型名排列。

每个启用的订阅都会在北京时间 N+1 日 `00:00` 收到 N 日日报，仅统计该订阅已选择的模型。日报包含总模型数、全程正常/日内异常后恢复/日终仍异常/无数据四类计数、按已观测时长加权的整体可用率、累计异常时长与故障次数，并逐项列出全部订阅模型。模型按日可用率从高到低排列，无数据置底；日报入箱记录按“日期 + 订阅”持久化去重。进程启动或版本升级不会补发日报，避免部署动作触发非零点消息。

`page.public_url` 可配置探针页的对外访问地址。配置后，状态变化通知、日报和测试通知会在消息下方显示本地化内联按钮，并关闭网页预览；URL 不再占用正文 footer。地址必须是无账号密码的完整 `http://` 或 `https://` URL。

| 模板变量 | 说明 |
|---|---|
| `.ChangedAt` | 该批状态变化时间（北京时间） |
| `.ChangedTime` | 该批状态变化的紧凑时间（北京时间，`MM-DD HH:mm`） |
| `.DownCount` / `.RecoveryCount` / `.TotalChanges` | 异常数、恢复数和总变化数 |
| `.DownModels` / `.RecoveredModels` | 异常模型列表和恢复模型列表 |
| `.Changes` | 本条订阅消息中的全部状态变化 |

`.DownModels`、`.RecoveredModels` 和 `.Changes` 的每个元素包含 `.ServiceID`、`.SortOrder`、`.Model`、`.Provider`、`.Protocol`、`.OK`、`.LatencyMS`、`.Error`、`.UptimePct`、`.Samples`、`.PreviousStatus`、`.Status`、`.LastTS`、`.OutageDurationSec`、`.TodayUpSec`、`.TodayDownSec`、`.TodayDownCount` 和 `.TodayUptimePct`。时间统计以北京时间零点为边界；相邻探测之间的时长归属于前一次已知状态。模板函数 `formatBeijing`、`beijingDate`、`durationCN` 和 `durationEN` 可用于格式化时间。完整默认模板见 [config.example.yaml](config.example.yaml)。

## 配置页

`/admin/`，用管理密码登录（会话内有效）。首次部署未设置密码时，进入页面会提示**设置管理密码**，之后用该密码登录。功能：

- **监控服务**：增删改、测试连接（立即探测一次并显示结果）、启停、设置通知排序；新服务默认追加到末尾
- **页面显示**：标题、副标题、探针注释、历史窗口、轮询间隔、四个统计维度开关
- **Telegram 订阅**：Bot Token 脱敏编辑、订阅增删改/启停、多模型选择、聚合模板编辑、实时与日报测试发送
- **系统更新**：显示当前与最新稳定版本，确认 GHCR 镜像可用后触发容器更新并跟踪重启
- **API Key 脱敏**：列表只显示掩码；编辑时留空即保留原密钥

所有修改原子写回配置文件并即时热重载，无需重启。

状态页按每个服务自己的 `interval_sec` 展示当前 interval 之前最近 `history_len` 个完整等宽时间桶。桶边界与 interval 固定对齐，只在进入下一个 interval 时推进；每次已完成探测覆盖从请求开始到请求完成或正常调度周期结束的观测区间，因此页面刷新、不同服务的启动相位和模型请求耗时不会制造空桶。`coverage` 表示已有探测结果或正在探测的观测桶数量；配置字段仍为 `show_samples`，以兼容已有配置。跨过完整桶仍在等待模型响应时显示 `probing`，启动前显示 `not started`，当前进程记录到的明确禁用期间显示 `paused`，完整 interval 内没有任何观测覆盖才显示 `no data`；这些非结果状态都不计为探测失败。

热力图先要求时间格达到 50% 探测覆盖率，再按聚合结果判定状态：失败率达到 20% 为 `failing`；失败或慢响应合计达到 5%，或成功请求的 p95 延迟超过服务的 `warning_sec`，为 `warning`；其余为 `healthy`。低覆盖率显示 `insufficient`，没有观测显示 `unobserved`。

## HTTP API

| 端点 | 方法 | 认证 | 说明 |
|---|---|---|---|
| `/healthz` | GET | 公开 | 轻量进程健康检查（`204 No Content`） |
| `/api/status` | GET | 公开 | 状态页数据（保持状态 API 的稳定数据结构） |
| `/api/heatmap?range=1d\|7d\|30d` | GET | 公开 | 按北京时间自然日聚合的健康状态热力图；默认 `7d`，兼容 `day`/`week`/`month` 旧参数 |
| `/api/admin/setup-status` | GET | 公开 | 管理密码是否已配置（前端选择登录/设置视图） |
| `/api/admin/setup` | POST | — | 首次设置管理密码（仅未配置时可用） |
| `/api/admin/login` | POST | — | `{token}` 校验 |
| `/api/admin/services` | GET / POST | Bearer | 列表 / 新增 |
| `/api/admin/services` | PATCH | Bearer | 批量启用、禁用或删除服务 |
| `/api/admin/services/{id}` | PUT / DELETE | Bearer | 更新 / 删除 |
| `/api/admin/services/{id}/duplicate` | POST | Bearer | 复制服务并生成新 ID |
| `/api/admin/services/{id}/test` | POST | Bearer | 立即探测一次 |
| `/api/admin/page` | GET / PUT | Bearer | 页面显示配置 |
| `/api/admin/telegram` | GET / PUT | Bearer | 获取（Token 脱敏）/ 更新 Telegram 配置 |
| `/api/admin/telegram/test` | POST | Bearer | 按 `{"subscription_id": "operations", "kind": "event"\|"daily"}` 同步发送实时或日报测试消息 |
| `/api/admin/update` | GET / POST | Bearer | 获取版本状态 / 触发一键更新 |
| `/api/admin/update/check` | POST | Bearer | 强制刷新 GitHub Tag 与 GHCR 镜像状态 |

## 存储与数据

- `data/config.yaml` — 配置源（配置页在线修改落盘于此）
- `data/probe.db` — SQLite 探测历史、待处理状态变化、通知 outbox 与日报去重账本；探测历史保留 30 天

Docker 部署使用命名卷持久化 `/data`。若改用绑定挂载，需确保宿主目录对容器非 root 用户（uid 65534）可写：

```bash
mkdir -p data && sudo chown -R 65534:65534 data
```

## 项目结构

```text
cmd/model-uptime/       进程入口：参数、信号与应用启动
internal/
  app/                  依赖装配与生命周期管理
  admin/                配置事务与管理操作
  heatmap/              探测历史的公开健康热力图聚合
  httpserver/           HTTP 路由、认证与静态资源服务
    web/                状态页、配置页、样式、脚本与字体
  model/                领域模型（服务定义、探测结果、页面配置）
  monitor/              探测调度、并发控制、历史窗口与状态快照
    probe/              协议探针适配器（chat / response / message / http）
  notification/         Telegram 聚合模板、outbox 与可靠投递
  settings/             YAML 配置加载、校验、归一化与原子写回
  storage/sqlite/       SQLite 历史、状态变化 ledger 与通知 outbox
  update/               稳定版本检查、镜像确认与容器更新触发
tests/
  integration/          跨内部模块的 Go 行为测试
  web/                  跨前端模块的 Node.js 回归测试
  deployment/           Docker 与发布配置契约测试
```

## 测试

```bash
go test ./...
make web-test
make deployment-test
```

Go 包测试遵循标准工具链约定，以 `*_test.go` 与被测包共置，便于直接测试包内行为；不额外建立 `__test__` 目录。跨多个内部包的 Go 行为测试放在 `tests/integration/`，Web 回归测试和部署契约测试分别放在 `tests/web/` 与 `tests/deployment/`，测试夹具放在对应包或测试目录的 `testdata/` 中。

覆盖：各协议探测成功/失败/超时/路径拼接/鉴权头、配置加载与校验、SQLite 事务与迁移、持久化状态变化聚合、Telegram 模板与重试、管理 API 全流程（认证、CRUD、密钥保留、热重载）。
