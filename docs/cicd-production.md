# 生产环境 CI/CD 部署

本文档对应 [`.github/workflows/publish.yml`](../.github/workflows/publish.yml)，配置方式参考 `Flux-silicon-pool/.github`。生产部署仅支持手动触发，不会响应 `push` 或 `pull_request`。

## 部署流程

1. 选择要发布的 Git Tag 或分支，并填写镜像版本 `version`。
2. 执行 Go `vet`、测试，以及前端依赖安装、审计、Lint、类型检查和构建。
3. 使用 GitHub 自动提供的 `GITHUB_TOKEN` 构建并推送 GHCR 镜像：

   ```text
   ghcr.io/<仓库所有者>/upstream-hub:<version>
   ```

4. 使用 SSH 用户名和密码连接服务器，进入 `/opt/upstream-hub`。
5. 更新服务器 `.env` 中的 `UPSTREAMHUB_IMAGE` 和 `UPSTREAMHUB_IMAGE_TAG`，然后执行 Docker Compose 部署。
6. 等待容器健康检查；失败时恢复 `.env.cicd.previous` 并重新启动上一版本。

## GitHub 配置

建议在仓库 `Settings → Environments` 中创建 `production` Environment，并按需启用 Required reviewers。

不需要配置 GitHub Variables，也不需要配置 Docker Hub 用户名、Token 或镜像仓库名称。镜像名称会根据当前 GitHub 仓库所有者自动生成。

### 必需 Secrets

| Secret | 说明 |
| --- | --- |
| `DEPLOY_HOSTS` | 生产服务器域名或 IP；多个服务器使用英文逗号分隔。 |
| `DEPLOY_PASSWORD` | SSH 登录密码。 |

如果服务器 SSH 用户不是 `root`，再配置：

| Secret | 默认值 | 说明 |
| --- | --- | --- |
| `DEPLOY_USER` | `root` | SSH 登录用户。 |

### 可选 Secrets

| Secret | 默认值 | 说明 |
| --- | --- | --- |
| `DEPLOY_PORT` | `22` | SSH 端口。 |
| `DEPLOY_PATH` | `/opt/upstream-hub` | 服务器上的 Docker Compose 目录。 |
| `SSH_FINGERPRINT` | 空 | SSH Host Key 的 SHA256 指纹，生产环境建议配置。 |
| `GHCR_USERNAME` | 仓库所有者 | 用于服务器登录 GHCR 的用户名。 |
| `GHCR_PAT` | 空 | 私有 GHCR 镜像必需，Token 至少需要 `read:packages`；公开镜像不需要。 |

最简配置只有两个 Secrets：`DEPLOY_HOSTS` 和 `DEPLOY_PASSWORD`。此时要求 SSH 用户为 `root`、端口为 `22`、部署目录为 `/opt/upstream-hub`，并且 GHCR 镜像可公开拉取。

## GHCR 镜像权限

GitHub Actions 推送镜像使用自动生成的 `GITHUB_TOKEN`，无需手动配置 Registry 凭据。

首次发布后可在 GitHub 仓库或个人主页的 `Packages` 中找到 `upstream-hub`：

- 将 Package 设置为 Public：服务器无需 Registry Token。
- 保持 Private：创建具有 `read:packages` 权限的 PAT，保存为 `GHCR_PAT`；如 PAT 所属账号不是仓库所有者，同时配置 `GHCR_USERNAME`。

## 生产服务器准备

服务器需要安装 Docker Engine 和 Docker Compose v2。SSH 用户必须能够直接执行 `docker`，并且有权读写部署目录。

目录结构固定为：

```text
/opt/upstream-hub/
├── docker-compose.yml
└── .env
```

如果通过 `DEPLOY_PATH` 修改了目录，请在该目录放置相同文件。Compose 服务名固定为 `app`。

生产 `.env` 示例：

```env
UPSTREAMHUB_IMAGE=ghcr.io/<仓库所有者>/upstream-hub
UPSTREAMHUB_IMAGE_TAG=v1.0.0

POSTGRES_HOST=172.17.0.1
POSTGRES_PORT=5432
POSTGRES_USER=upstreamhub
POSTGRES_PASSWORD=请替换为真实数据库密码
POSTGRES_DB=upstreamhub

UPSTREAMHUB_HTTP_PORT=8080
UPSTREAMHUB_SERVER_MODE=release
UPSTREAMHUB_LOG_LEVEL=info

APP_SECRET=请替换为至少32字节的随机字符串
AUTH_ENABLED=true
ADMIN_USERNAME=admin
ADMIN_PASSWORD=请替换为强密码
AUTH_TOKEN_SECRET=请替换为独立随机字符串
```

业务密钥只保存在服务器 `.env`，不需要配置到 GitHub Secrets。

首次部署前验证：

```bash
cd /opt/upstream-hub
docker compose config --quiet
```

## 发布生产环境

在 GitHub 仓库进入 `Actions → Deploy Production → Run workflow`：

1. 选择要部署的分支或 Git Tag。
2. 在 `version` 中填写版本，例如 `v1.0.0`。
3. 点击运行，并在配置了审核规则时完成 `production` Environment 审批。

也可以使用 GitHub CLI：

```bash
gh workflow run publish.yml --ref v1.0.0 -f version=v1.0.0
```

流水线只推送填写的精确版本，不维护 `latest`，避免生产服务器意外跟随可变标签。

## 回滚

部署失败时，流水线会自动恢复 `.env.cicd.previous` 并重新启动上一镜像。

手动回滚：

```bash
cd /opt/upstream-hub
cp .env.cicd.previous .env
docker compose pull app
docker compose up -d --no-build app
docker compose ps app
```

也可以在 GitHub Actions 中选择历史 Git Tag，填写对应历史版本，再次运行生产工作流。

## 常见问题

- SSH 失败：检查 `DEPLOY_HOSTS`、`DEPLOY_PASSWORD`，以及可选的 `DEPLOY_USER`、`DEPLOY_PORT`。
- 镜像拉取失败：公开镜像检查 Package 可见性；私有镜像检查 `GHCR_PAT` 的 `read:packages` 权限。
- Compose 文件找不到：确认 `DEPLOY_PATH` 指向包含 `docker-compose.yml` 和 `.env` 的目录。
- 健康检查失败：查看 `docker compose logs --tail=100 app`，并检查 PostgreSQL、`APP_SECRET` 和端口配置。
