# 生产环境 CI/CD 部署

本文档对应 [`.github/workflows/publish.yml`](../.github/workflows/publish.yml)。推送 `vX.Y.Z` 附注 tag 后，流水线自动构建镜像、创建 GitHub Release，并触发生产部署。

## 部署流程

1. 校验 tag 名、中文附注说明，以及 [`CHANGELOG.md`](../CHANGELOG.md) 中对应版本的中文变更项。
2. 执行 Go `vet`、测试，以及前端依赖安装、审计、Lint、类型检查和构建。
3. 使用 GitHub 自动提供的 `GITHUB_TOKEN` 构建并推送 GHCR 镜像：

   ```text
   ghcr.io/<仓库所有者>/upstream-hub:<tag>
   ```

4. 镜像构建成功后，根据更新日志的对应版本章节创建中文 GitHub Release，并附上镜像摘要和源码提交。
5. 使用 SSH 用户名和密码连接服务器，进入 `/opt/upstream-hub`，按镜像摘要拉取本次构建的镜像。
6. 更新服务器 `.env` 中的 `UPSTREAMHUB_IMAGE` 和 `UPSTREAMHUB_IMAGE_TAG`，然后执行 Docker Compose 部署。
7. 等待容器健康检查；失败时恢复 `.env.cicd.previous`，并使用部署前容器的本地镜像 ID 重新启动上一版本。

镜像构建成功后先创建 Release，再进入生产部署。若 `production` Environment 设置了审核人，部署会等待审批，但不会阻止 Release 创建；部署失败也不会自动撤销已发布的 Release。
连续推送多个 tag 时，部署会检查服务器 `.env` 中的当前版本，跳过低于当前生产版本的旧 tag。

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
| `GHCR_PAT` | 空 | 私有 GHCR 镜像必需，应使用 classic PAT 并授予 `read:packages`；公开镜像不要配置。 |

最简配置只有两个 Secrets：`DEPLOY_HOSTS` 和 `DEPLOY_PASSWORD`。此时要求 SSH 用户为 `root`、端口为 `22`、部署目录为 `/opt/upstream-hub`，并且 GHCR 镜像可公开拉取。

## GHCR 镜像权限

GitHub Actions 推送镜像使用自动生成的 `GITHUB_TOKEN`，无需手动配置 Registry 凭据。

首次发布后可在 GitHub 仓库或个人主页的 `Packages` 中找到 `upstream-hub`：

- 将 Package 设置为 Public：服务器无需 Registry Token。
- 保持 Private：创建 classic PAT，授予 `read:packages` 权限并保存为 `GHCR_PAT`；如 PAT 所属账号不是仓库所有者，同时配置 `GHCR_USERNAME`。组织启用了 SSO 时还需为该 Token 授权 SSO。

流水线会先验证目标镜像能够拉取，再修改 `.env` 或重建容器。GHCR 登录或镜像拉取失败时，当前运行中的服务不会被触碰。若误配了 `GHCR_PAT` 但 Package 是 Public，流水线会清除失败的临时登录状态并尝试匿名拉取。

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
UPSTREAMHUB_IMAGE_TAG=v1.1.6

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

1. 在 `CHANGELOG.md` 中将“未发布”的变更整理为 `## [vX.Y.Z] - YYYY-MM-DD` 章节，并至少写一条中文变更项。
2. 提交代码和更新日志。tag 必须指向包含该版本章节的提交。
3. 创建附注 tag，在 tag 标题中写中文发布说明，然后推送，例如：

   ```bash
   git push origin main
   git tag -a v1.1.6 -m "发布 v1.1.6：自动生成中文 Release"
   git push origin v1.1.6
   ```

4. 在 GitHub 的 `Actions → Publish Release` 查看校验、构建、Release 和生产部署结果；如配置了 `production` 审核规则，完成相应审批。

仅 `vX.Y.Z` 格式的 tag 会通过校验。轻量 tag、缺少中文附注标题、缺少对应更新日志章节或中文变更项都会使发布失败。流水线只推送该版本的精确镜像标签，不维护 `latest`。已发布 tag 不应移动或复用；下一次发布使用新版本号。

## 回滚

部署失败时，流水线会自动恢复 `.env.cicd.previous`，并将部署前容器的镜像 ID 临时标记为本地回滚镜像后重新启动。回滚不再依赖旧镜像标签仍能从 Registry 拉取。

手动回滚：

```bash
cd /opt/upstream-hub
cp .env.cicd.previous .env
docker compose up -d --no-build --pull never app
docker compose ps app
```

需要部署历史版本时，在服务器上确认旧版本镜像仍可用，并按目标版本修改 `.env` 中的 `UPSTREAMHUB_IMAGE_TAG` 后执行 `docker compose up -d --no-build --pull never app`。不要移动或重新推送已发布的 tag。

## 常见问题

- SSH 失败：检查 `DEPLOY_HOSTS`、`DEPLOY_PASSWORD`，以及可选的 `DEPLOY_USER`、`DEPLOY_PORT`。
- GHCR 返回 `denied`：公开 Package 应删除 `GHCR_PAT`；私有 Package 应检查 `GHCR_USERNAME`，并确认使用已授权 `read:packages` 的 classic PAT。
- 镜像拉取失败：确认 Package 中确实存在当前 tag 对应的镜像摘要，并检查 Package 可见性。
- Release 未创建：检查 tag 是否为含中文标题的附注 tag，以及 `CHANGELOG.md` 是否有对应版本的中文变更项。
- Compose 文件找不到：确认 `DEPLOY_PATH` 指向包含 `docker-compose.yml` 和 `.env` 的目录。
- 健康检查失败：查看 `docker compose logs --tail=100 app`，并检查 PostgreSQL、`APP_SECRET` 和端口配置。
