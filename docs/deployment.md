# AI 中转服务自动部署

## 流水线

Gitea Actions 工作流 `.gitea/workflows/ai-relay-deploy.yml` 监听 `main` 分支更新。代码合并到 `main` 后，`cxerp-wsl-build` Runner 使用仓库 Dockerfile 构建 `cx-ai-relay:<提交号>` 镜像，通过 SSH `root@192.168.1.21:59999` 上传镜像归档，并在服务器加载镜像、替换 `cx-ai-relay` 容器。Runner 和服务器之间使用内网地址与严格 `known_hosts` 校验。

工作流当前只监听 `main` 推送；开发和日常提交应在 `develop` 完成，再通过合并进入 `main` 发布。镜像在 Runner 构建，服务器只接收镜像并启动容器。

## 首次部署准备

在深圳1服务器上准备 `/opt/cx-ai-relay/.env`，权限设为仅 root 可读（`chmod 600`）。至少配置 `DATABASE_DSN`、`ENCRYPTION_KEY`、`JWT_HS256_SECRET`、`JWT_ISSUER` 和 `JWT_AUDIENCE`；`HTTP_ADDR` 应监听容器主机网络上的 `:8088`。

Gitea Runner 从 Windows 凭据目录对应的 WSL 路径 `/mnt/e/dev/dotnet/CxWebServer/env/credentials/cx-ai-relay-production.env` 读取唯一的 `DATABASE_DSN=...` 项。该文件保存创想会员生产库 `cx_ec` 的连接信息，不进入 Git 仓库或 Docker 构建上下文。发布时，Runner 通过内网 SSH 单独传送 DSN；服务器先用它生成候选 `.env` 并启动候选容器，健康检查通过后才原子替换 `/opt/cx-ai-relay/.env`。其他 JWT 与加密密钥继续由服务器原 `.env` 管理。部署失败时保留原 `.env` 并恢复旧容器。

Runner 必须能读取该凭据文件，文件只应授权给当前 Windows 用户、SYSTEM 和本机 Administrators；WSL 挂载路径必须可用。文件格式为单行 `DATABASE_DSN`，目标必须是 `172.18.16.101:59997/cx_ec`，并启用 `charset=utf8mb4` 和 `parseTime=true`。不要将连接值放入 Gitea Actions 日志或仓库 Secret。

Runner 需要 Docker、Gitea Actions `cxerp-wsl-build` 标签，以及 `/root/.ssh/cxerp-deploy.key` 和 `/root/.ssh/known_hosts` 中 `[192.168.1.21]:59999` 的服务器指纹。部署脚本支持通过 `CXAI_DEPLOY_KEY` 指定 Runner 上的 SSH 私钥路径。

部署脚本要求服务器安装 Docker 和 curl。容器使用 host 网络监听 `8088`，通过 `/healthz` 验证启动；新容器未通过健康检查时会尝试恢复上一个容器。每次部署只处理 `cx-ai-relay` 容器和 `/opt/cx-ai-relay` 下上传的镜像归档，不修改数据库、其他容器或数据卷。

## 故障处理

- 工作流未触发：确认变更已合并到 `main`，并确认 Gitea Actions 已启用且 Runner 在线。
- SSH 阶段失败：检查 Runner 的部署私钥、`known_hosts` 和到 `192.168.1.21:59999` 的内网连通性。
- 部署前检查失败：确认 Runner 能读取生产 DSN 凭据文件，并确认服务器 `/opt/cx-ai-relay/.env` 中的其他必需项齐全、生产数据库可从服务器网络访问。
- 健康检查失败：查看 `cx-ai-relay` 容器日志和 `DATABASE_DSN` 对应数据库可达性；脚本会保留并尝试重新启动上一容器。
