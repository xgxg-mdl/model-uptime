# Development Guide

## Prerequisites

项目的可复现开发基线为：

- Go `1.25.13`
- Node.js `24.13.1`
- GNU Make 或兼容实现
- Docker（仅构建或检查容器镜像时需要）

`go.mod` 固定了最低补丁版本。机器上的 Go 版本较旧时，默认的 `GOTOOLCHAIN=auto` 会下载匹配工具链；CI 使用 `GOTOOLCHAIN=local`，确保实际执行的是工作流安装的精确版本。

```bash
GOTOOLCHAIN=auto go version
node --version
go mod download
npm ci
```

不要提交本地数据目录、`.env`、数据库文件或任何真实凭据。

## Project Layout

```text
api/openapi.yaml            OpenAPI 3.1 contract for public and admin HTTP APIs
cmd/model-uptime/           Process entry point: flags, signals, and app startup
internal/app/               Dependency wiring and lifecycle management
internal/admin/             Configuration transactions and admin operations
internal/httpserver/        HTTP transport, authentication, and static serving
internal/httpserver/web/    Embedded HTML, CSS, JavaScript, and fonts
internal/model/             Shared domain types
internal/monitor/           Probe scheduling, concurrency, and status snapshots
internal/monitor/probe/     Protocol-specific health probes
internal/notification/      Telegram aggregation, outbox, and delivery
internal/settings/          YAML repository, validation, and atomic writes
internal/storage/sqlite/    SQLite history, transition ledger, and outbox persistence
internal/update/            Release discovery and deployment update integration
tests/integration/          Cross-module Go behavior tests
tests/web/                  Cross-module Node.js web regression tests
tests/deployment/           Deployment and release contract tests
```

依赖方向应保持从 `cmd/model-uptime` 经 `internal/app` 到各职责包；`internal` 包之间通过明确类型和消费方定义的小接口协作。配置文件是运行时配置的可信来源，SQLite 保存探测历史、状态变化 ledger 和可靠通知 outbox。前端 HTML、CSS 和 JavaScript 位于 `internal/httpserver/web` 并嵌入二进制，因此不需要独立前端构建步骤。npm 依赖只用于 ESLint、Prettier、OpenAPI 校验和测试，不进入最终镜像。

通知 outbox 以订阅 ID 保持 FIFO。临时投递错误保留原退避时间；连续永久错误进入持久化隔离，不阻塞同订阅后续项。每条永久失败只保存实际投递配置的 SHA-256 指纹，不保存额外凭据副本；相关订阅配置发生变化时才恢复并使用持久化领域 payload 重渲染，因而运行时热更新与停机修改配置后重启具有相同语义。

## Test Layout

Go 测试遵循标准工具链约定：`*_test.go` 与被测包共置，不建立 `__test__` 目录。这样既能用同包测试覆盖必要的包内行为，也能用 `package_name_test` 编写只依赖公开接口的黑盒测试。

仅当测试横跨多个内部包或验证仓库级契约时，才放在顶层 `tests/`：跨内部包的 Go 行为测试位于 `tests/integration/`，浏览器无关的前端模块回归测试位于 `tests/web/`，Docker 与发布配置测试位于 `tests/deployment/`。静态夹具使用 Go 工具链会忽略的 `testdata/` 目录，并尽量放在消费它的包或测试套件旁边。

## Common Commands

```bash
make fmt       # Format repository Go files
make check     # Check formatting, modules, and go vet
make test      # Run Go tests
make race      # Run Go tests with the race detector
make js-check  # Run JavaScript lint and formatting checks
make web-test  # Run Node.js web regression tests
make deployment-test # Run deployment and release contract tests
make vuln      # Run the pinned govulncheck version
make ci        # Run all CI quality gates
```

`make check` 使用 `go mod tidy -diff`，只报告模块文件差异而不修改工作区。`make vuln` 通过 Go module cache 执行固定版本的工具，不要求全局安装 `govulncheck`。

## Local Development

首次运行会在数据目录创建配置与 SQLite 数据库。使用独立的临时目录，避免污染仓库或覆盖实际数据。

```bash
tmp_dir="$(mktemp -d)"
go run ./cmd/model-uptime --port 8080 --data "$tmp_dir"
```

后端或配置变更优先运行目标包测试，再运行完整门禁：

```bash
go test ./internal/settings -run TestName -count=1
make ci
git diff --check
```

前端没有打包阶段。首次检出或 `package-lock.json` 更新后运行 `npm ci`；修改嵌入页面后运行 `make js-check` 和 `make web-test`，并确认相关 Go API 测试仍通过。API 路由或响应结构变化时同步更新 `api/openapi.yaml`，部署契约测试会核对 OpenAPI 与 Go 路由注册的一致性。

## Configuration and Data Safety

- 使用 `config.example.yaml` 或首次启动生成的模板建立本地配置。
- 测试中使用假凭据和本地临时服务器，不请求真实模型服务。
- 配置可能包含 API Key、Telegram Bot Token 和管理员令牌，日志与失败输出不得包含原值。
- 数据库、WAL 文件、`.env` 和本地数据目录均不得提交。

## CI and Release

Pull Request 与 `main` 分支 push 会运行相同质量门禁。稳定 SemVer tag（例如 `v1.2.3`）触发发布；发布任务只有在格式、模块、静态检查、race-enabled tests、前端回归测试和漏洞扫描全部通过后，才取得包写权限并推送多架构镜像。

维护者在创建 tag 前执行：

```bash
make ci
git diff --check
git status --short
```

发布工作流负责注入 `VERSION`、`COMMIT` 和 `BUILD_TIME`，并生成 OCI release metadata。不要从本地覆盖 `latest`，也不要为预发布或非 SemVer tag 手工绕过发布门禁。
