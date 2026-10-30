# Hakopod

[English](../../README.md) · 简体中文 · [日本語](README.ja.md) · [Español](README.es.md) · [Português](README.pt-BR.md) · [Deutsch](README.de.md)

你的应用，你做主。Hakopod 是一个开源平台，用来在你自己的服务器上部署和运行应用：从 Git 推送、导入 `docker-compose.yml` 或选择模板，先查看将要发生的变更，再部署到你的域名并自动配置 TLS。

```sh
curl -fsSL https://hakopod.com/scripts/installer.sh | sudo sh
```

一台全新的 Ubuntu 或 Debian 服务器、4 GiB 内存就能开始。Hakopod 目前处于 alpha 阶段；在用于生产之前，请先阅读[已验证与未验证的内容](../milestones.md)（英文）。官网与指南：[hakopod.com](https://hakopod.com)。

## 可以不再付费的服务

下表每一行都是[模板目录](../templates.md)中现在就能从控制台部署的模板。目录里记录了每个模板的验证状态和限制，使用前请先查看。

| 替代 | 运行 |
| --- | --- |
| Vercel、Heroku、Render | 你自己的应用：来自 Git、Dockerfile 或 Compose |
| Sentry | GlitchTip |
| Google Analytics | Umami |
| Zapier | n8n |
| Pingdom、Better Stack | Uptime Kuma |
| Airtable | NocoDB |
| 团队共享密码管理器 | Vaultwarden |
| ChatGPT 团队席位 | Open WebUI，配合你自己的模型密钥 |
| 托管 Postgres 和 Redis | PostgreSQL、Valkey、Redis |

## 为什么选择 Hakopod

- **底层是 Kubernetes，上层不用写 YAML。** Hakopod 从第一台服务器起就运行在 K3s 上，扩展到第二台机器只需加入一个 worker，无需迁移。你只描述应用和它的服务，清单由 Hakopod 生成。
- **上线之前先看变更。** 每次部署都先生成一份计划，列出将要改变的内容。回滚会创建一个新的、可审计的版本。
- **直接用你的 Compose 文件。** 粘贴 `docker-compose.yml`，检查转换后的配置和部署计划，然后部署。
- **给 AI 代理一把钥匙，而不是整个集群。** CLI 和 MCP 端点使用有范围、短时效的机器密钥。CI 和代理永远不需要 Kubernetes 凭据。
- **默认私有。** 服务共享一个私有网络；只有你标记为公开的服务才会获得访问地址。

## 对比

Coolify、Dokploy 和 Openship 都是好工具；如果你现在就想要最成熟的选择，Coolify 的社区最大。以下差异基于各项目 2026 年 10 月的官方文档：

| | Hakopod | Coolify | Dokploy | Openship |
| --- | --- | --- | --- | --- |
| 应用运行在 | K3s（Kubernetes） | Docker | Docker，多服务器用 Swarm | Docker，集群用 K3s |
| 开发语言 | Go | PHP（Laravel） | TypeScript | TypeScript |
| 每次部署前显示将要变更的内容 | 支持，每次部署都有计划 | — | — | 部署向导中的审核步骤 |
| 预览环境 | 手动创建，基于已构建的镜像，1–72 小时 | 每个拉取请求 | 每个拉取请求（GitHub） | 基于 Git 分支 |
| IP 允许和拒绝列表、限流、国家规则 | 在控制台中配置 | — | — | 通过 CLI 配置 |
| 将控制台和 API 限制在你的网络或 VPN 内 | 允许列表、防火墙、内置 WireGuard 服务器、Tailscale、OpenVPN、IPsec（Pro） | API 的 IP 允许列表 | — | — |
| 通行密钥 / TOTP | 支持 / 支持 | 不支持 / 支持 | 支持 / 支持 | 支持 / 支持 |
| 闲置应用缩容到零 | 支持 | — | — | 支持 |
| 面向 AI 代理的 MCP | 支持 | 支持 | 支持 | 支持 |
| 官方 Terraform Provider | 支持 | — | 仅社区版 | — |
| 托管数据库 | PostgreSQL, MySQL, MongoDB, Redis, ClickHouse, Vitess, Oracle Free | PostgreSQL, MySQL, MariaDB, MongoDB, Redis, ClickHouse | PostgreSQL, MySQL, MariaDB, MongoDB, Redis, libSQL | PostgreSQL, MySQL, MongoDB, Redis |
| 可直接部署的模板 | 57 | 300+ | 500+ | 42 |
| 审计日志 | 支持；导出历史为 Pro | 支持 | Enterprise | 支持 |
| 许可证 | Apache-2.0 | Apache-2.0 | Apache-2.0，另含一个专有目录 | Apache-2.0 |

"—" 表示我们在该项目自己的文档中没有找到相关说明，并不代表它做不到。如有过时的内容，请告诉我们。

K3s 需要更多内存，所以 Hakopod 的要求高于基于 Docker 的工具。换来的是：跨服务器调度和滚动更新由 Kubernetes 负责。

## 我们如何构建它

我们在 AI 编程代理的帮助下开发 Hakopod，相关说明见 [AGENTS.md](../../AGENTS.md)。每个版本发布前都会在全新的 amd64 和 arm64 GitHub runner 上运行安装程序。遇到问题请[提交 issue](https://github.com/hakopod/hakopod/issues/new)，并写明版本、操作系统和你的操作步骤（可以用中文）。

完整文档（英文）请见 [README](../../README.md)。
