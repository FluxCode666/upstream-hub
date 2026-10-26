# 本机 cf-solver 打码 provider（capsolver-local）

把 `cf-solver` 便携包包成 upstream-hub 的一个 Turnstile provider，不消耗任何云打码额度。

## 它是什么 / 不是什么

| 项 | 说明 |
|---|---|
| 类型值 | `capsolver-local`（前端「平台」下拉里的「本机 cf-solver（Linux x64）」） |
| 后端实现 | `backend/internal/captcha/cfsolver.go`，实现 `captcha.Provider.SolveTurnstile(ctx, siteKey, pageURL) (token, error)` |
| 调用方式 | `exec run.sh -mode solve -origin-solve <origin> -sitekey <siteKey>`，从 stdout 的 JSON 里取 `turnstile_token` |
| 产出 | Turnstile token，塞进上游登录请求（`internal/connector/newapi/newapi.go:69` / `sub2api/sub2api.go:64`） |
| 不产出 | `cf_clearance` / 站点 cookie。那是 cf-solver 的 URL 模式产物，属另一条鉴权通道，本 provider 不使用 |

## 配置项

| 字段 | 含义 |
|---|---|
| API Key | 启停标记。非空即允许执行；留空直接报「配置视为未启用」。解密的明文只用于这个判断，不落日志。 |
| Endpoint | cf-solver 包根目录（含 `run.sh`、`runtime/`、`transport/`）。留空则依次回退 `UPSTREAMHUB_CFSOLVER_ROOT`、`/app/cf-solver`。 |
| Extra | JSON，可选：`{"package_root":"…","timeout_sec":60,"concurrency":1,"node_bin":"…","extra_args":["…"]}` |

`timeout_sec` 默认 60，夹在 [20, 300]；`concurrency` 默认 1，按包目录做进程级信号量（每个包目录一个槽位池）。

## 环境要求（硬性）

1. **Linux x86_64**。包内 `runtime/bin/node`、`transport/go-transport` 都是 x86-64 ELF；macOS/Windows 原生跑不了，arm64 需要 `--platform linux/amd64`（QEMU 下更慢，指纹也可能漂）。
2. **glibc 2.28+**。包 README 明示不支持 Alpine/musl，因此镜像基座改成 `debian:12-slim`（见 `Dockerfile` 第 3 阶段）。
3. 系统需有 `libstdc++6`、`libgcc-s1`、CA 证书；中文站点的文字度量建议装 `fonts-noto-cjk`（镜像里已装）。
4. 出口网络要能直连上游站点和 `challenges.cloudflare.com`；需要代理时给容器设 `HTTPS_PROXY`，包内不自带代理。

## 构建与启动

第三方包不直接提交 Git（其中 `runtime/bin/node` 超过 GitHub 单文件限制）。Dockerfile 从
[`cf-solver-v1.4.0-rc.2` Release](https://github.com/FluxCode666/upstream-hub/releases/tag/cf-solver-v1.4.0-rc.2)
下载 `cf-solver-1.4.0-rc.2-linux-x64.tar.gz`，并用固定 SHA-256 校验：

```
5439e0c47da9d0bbb16905192311ee841041ff52ac966e96789bccdce52cd1ea
```

因此克隆仓库后不需要手动准备包，直接构建即可（x86_64 宿主机可省略 `--platform`）：

```sh
docker build --platform linux/amd64 -t upstream-hub:cfsolver .
```

多架构构建使用 `TARGETARCH` 控制依赖下载：amd64 镜像带完整 cf-solver；arm64 镜像仍可运行面板及
云打码 provider，但不带本机 x64 包，不能在本机执行 `capsolver-local`。在 Apple Silicon 上使用本机打码时，
按上面的命令显式构建、运行 `linux/amd64`。

容器启动时把只读模板 `/opt/cf-solver-template` 复制到可写的 `/app/cf-solver`（属主 uid 10001），
之后不再覆盖，方便改 `universal_host/fingerprint.config.json`。要回到镜像里的原始包，删掉容器内的
`/app/cf-solver` 再重启。

## 冒烟验证（已实测）

```sh
docker run --rm --platform linux/amd64 --entrypoint sh upstream-hub:cfsolver \
  -c 'cp -a /opt/cf-solver-template /tmp/p && chmod -R u+rwX /tmp/p && /tmp/p/run.sh --check'

# 容器起来后，以运行用户（uid 10001）再跑一次
docker exec -u upstream <容器> /app/cf-solver/run.sh --check
```

实测输出：

```json
{"status":"ok","version":"1.4.0-rc.2","node":"v22.22.0","files":230,"checks":["sha256","canvas","resvg","host","transport"]}
```

`--check` 会校验 SHA256SUMS、native 模块、宿主环境和 transport 版本。注意 `check.mjs` 要求通过
`run.sh` 启动，直调 node 会失败。

## 面板配置

打码配置页新增一条：类型选「本机 cf-solver（Linux x64）」→ API Key 随便填一串非空字符 → Endpoint 填 `/app/cf-solver`（留空也行）→ 启用。
然后在渠道里勾上「启用 Turnstile」并选中这条打码配置。

## 排障对照

| 现象 | 原因 / 处理 |
|---|---|
| `找不到 /app/cf-solver/run.sh` | Endpoint / `UPSTREAMHUB_CFSOLVER_ROOT` 指错，或包里 `run.sh` 丢了 |
| `缺少可执行权限` | 拷贝过程丢了 0755，`chmod +x run.sh` |
| `包内 node 缺失` / `transport/go-transport 缺失` | 包没解压完整，或 `.dockerignore` 误排除了 `runtime`/`transport` |
| `exit=2` | cf-solver 认为输入或身份配置错误：多半是 origin/sitekey 不对，或 `fingerprint.config.json` 里 UA 与 TLS profile 不匹配 |
| `exit=1` | 流程未通过（超时、站点拒绝）。官方实测中位 8.6–9.8 秒，偶发超时重试属正常，可把 `timeout_sec` 调到 90 |
| 一直成功但登录仍 403 | token 与登录请求不同源（cf-solver 自带独立 TLS/UA/出口 IP），需要让 Go 侧与包侧共用代理，或改用 cookie 注入通道 |
| `求解超过 60s` | 宿主 CPU 吃紧或 QEMU 太慢，降低并发、提高超时 |

## 安全注意

- `-artifacts` / `-work` 产出的诊断目录含 cookie 与 token，本 provider 默认**不传**这两个参数，也不落盘。
- 不要把运行后的 `/app/cf-solver` 目录打进镜像或外发；`SHA256SUMS` 只覆盖原始包文件。
- API Key 字段虽只做启停，仍走 AES-GCM 加密落库，不要复用到别处。
