# Contributing

感谢参与 model-uptime。提交应保持现有 UI、交互和公开 API 的兼容性；行为变化必须有测试支撑，并在说明中明确用户可见影响。

## Development Setup

开发环境固定使用 Go `1.25.13` 和 Node.js `24.13.1`。Node.js 仅用于运行基于内置 test runner 的前端回归检查，不需要安装 npm 依赖。

```bash
GOTOOLCHAIN=auto go version
node --version
go mod download
make check
make test
make web-test
make deployment-test
```

完整环境与调试说明见 [`docs/development.md`](docs/development.md)。

## Change Guidelines

- 先阅读相邻实现与测试，保持现有包边界和命名习惯。
- 修复根因，并为新增行为或回归问题补充测试。
- Go 包测试以 `*_test.go` 与被测包共置；跨内部包的 Go 测试、Web 测试和部署契约测试分别放在 `tests/integration/`、`tests/web/`、`tests/deployment/`，静态夹具放在就近的 `testdata/`。
- Go 代码使用 `gofmt`；代码注释说明原因、限制或取舍，不复述语句本身。
- 不在代码、测试、日志或提交记录中放入 API Key、Token、私钥或生产数据。
- 不在无关改动中重排文件、升级依赖或改变 UI。
- 公开 API、配置字段或持久化格式发生变化时，必须说明兼容性和迁移方式。

## Validation

提交前执行完整门禁：

```bash
make ci
git diff --check
```

`make ci` 会检查格式、模块一致性、`go vet`、race-enabled tests、前端回归测试和已知漏洞。仅修改后端测试时也应执行完整门禁，因为前端资源嵌入 Go 二进制，发布产物是一个整体。

## Commits and Pull Requests

提交信息使用 Conventional Commits 类型前缀，描述使用中文，例如：

```text
fix(httpserver): 修复服务更新时的配置校验
```

Pull Request 标题与描述使用 English，并包含：问题背景、实现方式、兼容性影响、验证命令和结果。保持一次提交内容聚焦且可独立审查；不要夹带格式化或依赖更新等无关变化。

安全漏洞不要提交公开 Pull Request，请遵循 [`SECURITY.md`](SECURITY.md)。
