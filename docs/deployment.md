# AI 中转服务自动部署

## 流水线

Gitea Actions 工作流 `.gitea/workflows/ai-relay-deploy.yml` 监听 `main` 分支更新。代码合并到 `main` 后，`cxerp-wsl-build` Runner 使用仓库 Dockerfile 构建 `cx-ai-relay:<提交号>` 镜像，通过 SSH `root@192.168.1.21:59999` 上传镜像归档，并在服务器加载镜像、替换 `cx-ai-relay` 容器。Runner 和服务器之间使用内网地址与严格 `known_hosts` 校验。

工作流当前只监听 `main` 推送；开发和日常提交应在 `develop` 完成，再通过合并进入 `main` 发布。镜像在 Runner 构建，服务器只接收镜像并启动容器。

## 首次部署准备

在深圳1服务器上准备 `/opt/cx-ai-relay/.env`，权限设为仅 root 可读（`chmod 600`）。至少配置 `DATABASE_DSN`、`ENCRYPTION_KEY`、`JWT_HS256_SECRET`、`JWT_ISSUER` 和 `JWT_AUDIENCE`；`HTTP_ADDR` 应监听容器主机网络上的 `:8088`。数据库地址必须能从服务器网络访问。流水线不会传输或提交本地 `.env`。

Runner 需要 Docker、Gitea Actions `cxerp-wsl-build` 标签，以及 `/root/.ssh/cxerp-deploy.key` 和 `/root/.ssh/known_hosts` 中 `[192.168.1.21]:59999` 的服务器指纹。部署脚本支持通过 `CXAI_DEPLOY_KEY` 指定 Runner 上的 SSH 私钥路径。

部署脚本要求服务器安装 Docker 和 curl。容器使用 host 网络监听 `8088`，通过 `/healthz` 验证启动；新容器未通过健康检查时会尝试恢复上一个容器。每次部署只处理 `cx-ai-relay` 容器和 `/opt/cx-ai-relay` 下上传的镜像归档，不修改数据库、其他容器或数据卷。

## 故障处理

- 工作流未触发：确认变更已合并到 `main`，并确认 Gitea Actions 已启用且 Runner 在线。
- SSH 阶段失败：检查 Runner 的部署私钥、`known_hosts` 和到 `192.168.1.21:59999` 的内网连通性。
- 部署前检查失败：在服务器补齐 `/opt/cx-ai-relay/.env` 中的必需项，并确认数据库连接可用。
- 健康检查失败：查看 `cx-ai-relay` 容器日志和 `DATABASE_DSN` 对应数据库可达性；脚本会保留并尝试重新启动上一容器。
