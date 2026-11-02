# FileHub

一个极简的自托管文件中转服务：打开网页拖拽文件即可上传，获得可直接分享的文件链接；支持大文件、断点续传、文件列表与多选打包下载。

## 功能特性

- 🖱️ 拖拽或点击上传，带实时进度、上传速度和用时显示
- 🔗 保留原始文件名，同名文件自动追加时间戳
- 🖼️ 文件列表按文件类型显示图标
- ☑️ 支持多选文件，一键打包为 ZIP 下载
- 🔒 可选口令保护的文件列表页，集中查看并下载所有文件
- 📤 上传完成或文件列表中一键复制链接，移动端可调用系统分享
- 📦 单文件最大 1 GiB
- ⏯️ 下载支持 `Range` / 断点续传
- 🐳 提供 Docker / Docker Compose 部署，多架构镜像（amd64 / arm64）
- 🪶 纯 Go 标准库实现，无第三方依赖

## 目录结构

```
.
├── main.go                  # 全部服务端逻辑与内置上传页面
├── go.mod                   # Go 模块定义（Go 1.22+）
├── README.md                # 使用文档
├── .env.example             # 环境变量示例
├── Dockerfile               # 多阶段构建
├── docker-compose.yml       # 一键部署
└── .github/workflows/docker.yml  # 构建并推送 GHCR 镜像、生成 SBOM
```

## 快速开始

### 方式一：Docker Compose（推荐）

```bash
git clone https://github.com/ctfrookie/filehub.git
cd filehub

# 设置文件列表口令（不设置则 /files 页面关闭）
cp .env.example .env    # Windows: copy .env.example .env
# 编辑 .env，把 FILEHUB_PASSWORD 改成自己的口令

# 方式 A：从源码构建并启动
docker compose up -d --build

# 方式 B：使用已发布的 GHCR 镜像
docker compose pull && docker compose up -d
```

访问 <http://localhost:8080>，上传的文件保存在宿主机的 `./data` 目录；访问 <http://localhost:8080/files> 并输入口令即可查看所有已上传文件。

### 方式二：直接拉取镜像

```bash
docker run -d \
  --name filehub \
  -p 8080:8080 \
  -v filehub-data:/app/data \
  -e FILEHUB_PASSWORD=change-me \
  --restart unless-stopped \
  ghcr.io/ctfrookie/filehub:latest
```

### 方式三：本地运行

需要 Go 1.22 或更高版本：

```bash
# macOS / Linux
FILEHUB_PASSWORD=change-me go run .

# Windows PowerShell
$env:FILEHUB_PASSWORD="change-me"; go run .

# 或编译后运行
go build -o filehub . && ./filehub
```

默认监听 `:8080`，文件写入当前目录下的 `./data`。

## 更新

使用 Docker Compose 部署时，一条命令即可拉取最新镜像并重建容器：

```bash
docker compose pull && docker compose up -d
```

如果是从源码构建：

```bash
git pull && docker compose up -d --build
```

`docker run` 部署更新时，需要先拉取最新镜像，再重建容器（数据卷不会丢失）：

```bash
docker pull ghcr.io/ctfrookie/filehub:latest
docker rm -f filehub
docker run -d \
  --name filehub \
  -p 8080:8080 \
  -v filehub-data:/app/data \
  -e FILEHUB_PASSWORD=change-me \
  --restart unless-stopped \
  ghcr.io/ctfrookie/filehub:latest
```

> ⚠️ `docker rm -f filehub` 只删除**容器**，不会删除本地镜像；而 `docker run` 发现本地已有 `latest` 标签时会直接复用，不会自动访问镜像仓库。因此必须先执行 `docker pull`。
>
> 也可以让 Docker 每次启动前强制检查更新：
>
> ```bash
> docker rm -f filehub
> docker run -d --pull always \
>   --name filehub \
>   -p 8080:8080 \
>   -v filehub-data:/app/data \
>   -e FILEHUB_PASSWORD=change-me \
>   --restart unless-stopped \
>   ghcr.io/ctfrookie/filehub:latest
> ```

如果仍显示旧内容，可以确认镜像是否真的更新：

```bash
docker image inspect ghcr.io/ctfrookie/filehub:latest --format '{{.Id}} {{.Created}}'
```

必要时强制删除旧镜像再拉取（不会影响数据卷）：

```bash
docker rmi ghcr.io/ctfrookie/filehub:latest
docker pull ghcr.io/ctfrookie/filehub:latest
```

## 使用说明

### 网页上传

1. 打开 `http://<服务器地址>:8080`
2. 将文件拖入虚线框，或点击「选择文件」
3. 上传过程中会显示百分比、已上传大小、总大小和平均速度；完成后显示总用时和平均速度
4. 上传完成后会显示形如 `/f/example.zip` 的链接，以及「复制链接」按钮；支持系统分享的设备还会显示「分享」按钮，可一键发送到微信、邮件等应用

> 文件会保留原始名称。如果同名文件已存在，会自动在扩展名前追加时间戳，例如 `example_20261102-103045.zip`，不会覆盖旧文件。

### 查看并下载已上传文件（文件列表）

服务启动时设置环境变量 `FILEHUB_PASSWORD` 后，即可使用口令保护的文件列表：

1. 打开 <http://localhost:8080/files>（首页也有「📁 文件列表」入口）
2. 浏览器会弹出登录框：
   - 用户名：任意填写（例如 `admin`）
   - 密码：`FILEHUB_PASSWORD` 的值
3. 验证通过后会按上传时间倒序列出所有文件，文件名前会显示对应类型的图标，并显示大小和上传时间
4. 点击文件名即可下载；每行右侧的「复制」「分享」按钮可以快速分享对应文件
5. 勾选多个文件（或点击表头全选框），然后点击「⬇️ 打包下载选中」，即可将选中的文件打包成一个 ZIP 下载

> 文件名保留原始名称；仅在同名冲突时自动追加时间戳，不会覆盖已有文件。

未设置 `FILEHUB_PASSWORD` 时，`/files` 会返回 404，列表功能保持关闭，避免文件被公开浏览。通过 `/f/...` 分享出去的链接仍然可以直接下载，不受口令影响。

### 通过 API 上传

```bash
curl -F "file=@./example.zip" -H "Accept: application/json" \
  http://localhost:8080/upload
```

返回：

```json
{"url":"/f/example.zip","name":"example.zip"}
```

> 请求头带 `Accept: application/json` 时返回 JSON；否则返回一个简单的 HTML 成功页。

### 下载

文件会以附件形式下载（`Content-Disposition: attachment`），同时保留原始文件名：

```bash
curl -O -J http://localhost:8080/f/example.zip
```

支持断点续传：

```bash
curl -C - -O http://localhost:8080/f/example.zip
```

## 接口一览

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/` | 上传页面 |
| `POST` | `/upload` | 上传文件，`multipart/form-data`，字段名 `file` |
| `GET` | `/f/{name}` | 下载文件，支持 `Range` |
| `GET` | `/files` | 文件列表页，需 HTTP Basic Auth（密码为 `FILEHUB_PASSWORD`） |
| `POST` | `/zip` | 将多个文件打包为 ZIP，表单字段 `files`（可重复） |

上传响应（`Accept: application/json`）：

```json
{
  "url": "/f/<文件名>",
  "name": "原始文件名"
}
```

打包下载选中文件：

```bash
curl -X POST \
  --data-urlencode "files=example.zip" \
  --data-urlencode "files=另一个文件.txt" \
  -o files.zip \
  http://localhost:8080/zip
```

## 配置说明

| 项目 | 值 | 说明 |
| --- | --- | --- |
| 监听端口 | `8080` | 当前硬编码在 `main.go` 中 |
| 存储目录 | `./data` | 容器内为 `/app/data`，建议挂载持久化 |
| `FILEHUB_PASSWORD` | 空 | 文件列表访问口令；为空时 `/files` 关闭并返回 404 |
| 单文件上限 | `1 GiB` | `main.go` 中 `maxSize` |
| 表单内存缓冲 | `32 MiB` | 超出部分写入临时文件 |

修改端口时请同时调整 `Dockerfile` 的 `EXPOSE`、`docker-compose.yml` 的端口映射（以及云厂商安全组/防火墙）。

## 数据与备份

所有上传文件都存放在数据目录中，文件名优先使用原始名称；同名时自动追加时间戳（例如 `photo_20261102-103045.jpg`），不会覆盖已有文件。路径分隔符和控制字符会被清理。备份时直接打包该目录即可：

```bash
# Docker Compose 部署
tar czf filehub-backup.tar.gz ./data

# 命名卷部署
docker run --rm -v filehub-data:/data -v "$PWD":/backup alpine \
  tar czf /backup/filehub-backup.tar.gz -C /data .
```

## 构建与发布

GitHub Actions 工作流 [`.github/workflows/docker.yml`](.github/workflows/docker.yml) 会在以下场景运行：

- 推送到 `main`
- 推送 `v*` 标签
- 针对 `main` 的 Pull Request（仅构建，不推送）
- 手动触发 `workflow_dispatch`

镜像发布到 `ghcr.io/ctfrookie/filehub`，标签规则：

- 分支名（如 `main`）
- 语义化版本（`v1.2.3` → `1.2.3`、`1.2`）
- `sha-<短提交号>`
- `latest`（默认分支）

同时构建 `linux/amd64` 与 `linux/arm64`，并生成 SBOM。

## 注意事项

- ⚠️ 上传接口、`/zip` 打包接口和 `/f/` 下载链接**没有鉴权**，任何能访问端口的人都可以上传，拿到链接即可下载。公网部署请务必配合反向代理、访问控制或内网使用。
- 🔐 `/files` 文件列表使用 HTTP Basic Auth 保护；密码随请求明文传输，公网使用请务必配置 HTTPS。
- 🛡️ 下载使用强制附件（`Content-Disposition: attachment`）并加上 `X-Content-Type-Options: nosniff`、`Content-Security-Policy: sandbox`，降低上传的 HTML/JS 被浏览器直接渲染导致的 XSS 风险。
- 🛡️ 路径穿越、空文件名、目录已被校验；表单大小和 ZIP 列表数量有限制。
- ⚠️ 没有删除或过期清理功能，请自行管理磁盘空间。
- ⚠️ 没有上传频率、总存储配额限制，也不做病毒扫描。公网部署建议在反向代理/WAF 层限流、限制请求体与总容量，并谨慎接收未知文件。
- 🐳 容器当前以 root 用户运行；如需进一步加固，可自行创建非 root 用户并确保数据卷可写。
- ⚠️ 文件现在保留原始名称，下载链接是可预测的（例如 `/f/报告.pdf`），拿到文件名即可尝试下载。请勿将敏感文件直接暴露在公网。
- 🐳 Dockerfile 改用 `golang:1-alpine` 和 `alpine:3` 滚动标签以获取安全补丁，建议定期 `docker pull` 更新镜像。
- 公网使用建议通过 Nginx/Caddy 配置 HTTPS，并同步放宽上传体积限制，例如 Nginx：

  ```nginx
  client_max_body_size 1024m;
  proxy_request_buffering off;
  ```

## License

未声明许可证，默认保留所有权利。
