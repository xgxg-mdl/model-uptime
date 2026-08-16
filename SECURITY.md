# Security Policy

## Supported Versions

项目只为最新稳定版本提供安全修复。旧版本不会单独维护；修复发布后，应升级到最新稳定标签。

| Version | Supported |
| --- | --- |
| Latest stable release | Yes |
| Older releases | No |

## Reporting a Vulnerability

请通过仓库的 [private vulnerability report](https://github.com/xgxg-mdl/model-uptime/security/advisories/new) 提交安全问题，不要在公开 Issue、讨论区或 Pull Request 中披露漏洞细节。

报告应尽量包含：

- 受影响的版本或 commit；
- 可复现的最小步骤；
- 预期影响及攻击前提；
- 已验证的缓解方式（如有）；
- 可用于回归测试的样例，但不要包含真实凭据或生产数据。

维护者会在私有通道中确认报告、评估影响并协调修复与披露。修复可用前，请避免公开利用细节。

## Scope

安全问题包括但不限于认证绕过、敏感信息泄露、远程请求边界突破、配置或数据库权限问题、依赖漏洞，以及容器和发布链路风险。

普通功能缺陷、配置疑问和不包含安全影响的异常，请使用公开 Issue。
