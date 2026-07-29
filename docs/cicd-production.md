# 生产环境 CI/CD 部署

本文档对应 [`.github/workflows/publish.yml`](../.github/workflows/publish.yml)。流水线仅部署生产环境，默认在代码推送到 `main` 后运行，也支持在 GitHub Actions 页面手动执行或回滚。

## 部署流程

流水线依次执行：

1. 运行 Go 后端测试和 `go vet` 静态检查。
2. 安装前端依赖，执行 critical 级依赖漏洞审计、ESLint、TypeScript 类型检查和生产构建。
3. 构建 Docker 镜像并推送到 Docker Hub：
   - `sha-<完整 commit SHA>`：不可变部署标签，也是主要回滚标签。
   - `latest`：指向最近一次成功构建的生产镜像。
4. 使用 SSH 账号和密码连接生产服务器。
5. 进入配置的 Docker Compose 目录，更新 `.env` 中的镜像仓库和标签。
6. 执行 `docker compose pull` 和 `docker compose up -d`。
7. 最多等待约 60 秒检查容器健康状态；部署失败或容器不健康时，自动回滚到部署前的本地镜像。

同一时间只允许一条生产部署运行，新的部署会排队，不会中断正在执行的部署。

## GitHub 配置

建议在仓库的 `Settings → Environments` 中创建名为 `production` 的 Environment，并按需启用 Required reviewers。以下 Secrets 和 Variables 均配置在该 Environment 中。

### Secrets

| 名称 | 必填 | 说明 |
| --- | --- | --- |
| `DOCKERHUB_USERNAME` | 是 | 用于推送镜像的 Docker Hub 用户名。 |
| `DOCKERHUB_TOKEN` | 是 | Docker Hub Access Token，应具有目标镜像仓库的读写权限，不要填写账号密码。 |
| `PROD_SSH_HOST` | 是 | 生产服务器域名或 IP。 |
| `PROD_SSH_USERNAME` | 是 | SSH 登录用户名；该用户必须可以直接执行 `docker`。 |
| `PROD_SSH_PASSWORD` | 是 | SSH 登录密码。 |
| `PROD_SSH_FINGERPRINT` | 是 | 生产服务器 SSH Host Key 指纹，例如 `SHA256:xxxxxxxx`，用于防止中间人攻击。 |
| `PROD_REGISTRY_USERNAME` | 私有镜像必填 | 生产服务器拉取私有 Docker Hub 镜像时使用的用户名。公开镜像可不配置。 |
| `PROD_REGISTRY_TOKEN` | 私有镜像必填 | 仅用于生产服务器拉取镜像，建议创建只读 Token。公开镜像可不配置。 |

`PROD_SSH_FINGERPRINT` 可在服务器本机查看，以下命令以 Ed25519 Host Key 为例：

```bash
ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub -E sha256
```

输出中的 `SHA256:...` 即需要保存的值。若服务器没有 Ed25519 Host Key，请对实际启用的 Host Key 公钥执行同样的命令。

### Variables

| 名称 | 必填 | 示例 | 说明 |
| --- | --- | --- | --- |
| `DOCKER_IMAGE` | 是 | `worryzyy/upstream-hub` | Docker Hub 镜像仓库，不包含标签。名称需使用小写。 |
| `PROD_COMPOSE_DIR` | 是 | `/opt/upstream-hub` | 服务器上 Docker Compose 文件所在的绝对目录。 |
| `DOCKER_PLATFORMS` | 否 | `linux/amd64` | 构建架构，默认 `linux/amd64`；ARM 服务器可设为 `linux/arm64`，多架构可使用逗号分隔。 |
| `PROD_SSH_PORT` | 否 | `22` | SSH 端口，默认 `22`。 |
| `PROD_COMPOSE_FILE` | 否 | `docker-compose.yml` | Compose 文件名，默认 `docker-compose.yml`。 |
| `PROD_COMPOSE_SERVICE` | 否 | `app` | 要更新的 Compose 服务名，默认 `app`。 |
| `PROD_COMPOSE_ENV_FILE` | 否 | `.env` | Compose 使用的环境变量文件，默认 `.env`。 |

业务运行密钥（例如 `APP_SECRET`、`POSTGRES_PASSWORD`、`ADMIN_PASSWORD`）不应放入此流水线。它们保存在生产服务器的 `.env` 中，工作流只更新 `UPSTREAMHUB_IMAGE` 和 `UPSTREAMHUB_IMAGE_TAG`。

## 生产服务器准备

服务器需要安装 Docker Engine 和 Docker Compose v2，并确保部署用户执行以下命令无需 `sudo`：

```bash
docker version
docker compose version
```

首次部署前创建 Compose 目录，并将仓库中的 `docker-compose.yml` 放入该目录：

```text
/opt/upstream-hub/
├── docker-compose.yml
└── .env
```

从示例文件创建 `.env`，至少确认以下配置：

```env
UPSTREAMHUB_IMAGE=worryzyy/upstream-hub
UPSTREAMHUB_IMAGE_TAG=latest

POSTGRES_HOST=172.17.0.1
POSTGRES_PORT=5432
POSTGRES_USER=upstreamhub
POSTGRES_PASSWORD=请替换为真实数据库密码
POSTGRES_DB=upstreamhub

APP_SECRET=请替换为至少32字节的随机字符串
AUTH_ENABLED=true
ADMIN_USERNAME=admin
ADMIN_PASSWORD=请替换为强密码
AUTH_TOKEN_SECRET=请替换为独立随机字符串
```

注意事项：

- 当前 Compose 配置连接外部 PostgreSQL，不会创建数据库容器。请确保数据库已存在，且容器可以访问 `POSTGRES_HOST:POSTGRES_PORT`。
- `172.17.0.1` 通常是 Linux 默认 Docker Bridge 的宿主机地址；若数据库位于其他服务器，请填写实际地址。
- `APP_SECRET` 修改后，数据库中已加密的通知配置将无法解密，必须长期保存。
- 公网部署应保持 `AUTH_ENABLED=true` 并设置强密码。
- `.env` 包含生产密钥，建议权限设置为 `600`，并且不要提交到 Git。

完成后可先在服务器验证配置：

```bash
cd /opt/upstream-hub
docker compose --env-file .env -f docker-compose.yml config --quiet
```

如果使用私有 Docker Hub 镜像，可以配置 `PROD_REGISTRY_USERNAME` 和 `PROD_REGISTRY_TOKEN` 让流水线在每次部署前登录；也可以在服务器上提前执行 `docker login`。

## 自动部署

合并或推送代码到 `main` 后，`Deploy Production` 工作流会自动运行。若 `production` Environment 配置了审核人，镜像构建和 SSH 部署会在审批后开始。

部署成功后，服务器 `.env` 会类似：

```env
UPSTREAMHUB_IMAGE=worryzyy/upstream-hub
UPSTREAMHUB_IMAGE_TAG=sha-0123456789abcdef0123456789abcdef01234567
```

因此后续在服务器手动执行 `docker compose up -d` 时，仍会使用本次已经验证过的不可变镜像，而不会意外跟随变化的 `latest`。

## 手动部署与回滚

在 GitHub 仓库进入 `Actions → Deploy Production → Run workflow`：

- `image_tag` 留空：构建所选分支当前提交，推送镜像并部署。
- `image_tag` 填已有标签：跳过构建，直接部署该镜像。回滚时填写先前成功部署的 `sha-<完整 commit SHA>`。

例如回滚到提交 `0123456789abcdef0123456789abcdef01234567`：

```text
sha-0123456789abcdef0123456789abcdef01234567
```

每次部署前，流水线还会把当前容器镜像临时标记为 `rollback-<GitHub run id>`。如果新容器启动失败或健康检查不通过，流水线会自动使用该镜像恢复服务，并把回滚标签写回服务器 `.env`。部署成功后会清理临时回滚标签。

## 常见问题

### SSH 连接失败

检查 `PROD_SSH_HOST`、`PROD_SSH_PORT`、用户名和密码，并确认 GitHub 托管 Runner 可以访问服务器 SSH 端口。如果指纹不匹配，应先确认服务器是否更换过 Host Key，不要直接删除指纹校验。

### `permission denied` 或无法访问 Docker Socket

将部署用户加入服务器的 `docker` 用户组，然后重新登录使组权限生效：

```bash
sudo usermod -aG docker <部署用户名>
```

### 镜像拉取失败

确认 `DOCKER_IMAGE` 与 Docker Hub 仓库一致。私有仓库还需检查 `PROD_REGISTRY_USERNAME` 和只读 `PROD_REGISTRY_TOKEN`。

### 容器不健康并自动回滚

流水线会输出新容器最后 100 行日志。通常需要检查 PostgreSQL 地址、数据库密码、`APP_SECRET` 和端口占用情况。修复服务器 `.env` 后重新运行工作流即可。
