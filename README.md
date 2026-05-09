# chore

剪贴板内容（文字或图片）同步到服务端并持久化，支持 Web 预览、详情查看与本地管理。

## 组成

| 程序 | 说明 |
|------|------|
| **chore** | 命令行客户端：读取剪贴板（文字或 PNG 图片）并通过 HTTP 上传到 chore_svr |
| **chore_svr** | HTTP 服务 + 本地工具：接收内容写入 SQLite，提供 Web 列表/详情；也可直连数据库做查询/更新/删除 |

## 快速开始

```bash
# 1. 编译（注入编译信息）
make all

# 2. 启动服务端（默认监听 :2026，数据存 ./chore.db）
./chore_svr

# 3. 上传剪贴板内容（文字或图片自动识别）
./chore

# 4. 浏览器查看列表
open http://localhost:2026/list/chore
```

也可手动指定 IP/端口：

```bash
make all IP=192.168.1.1 PORT=9000
```

## chore 客户端

可执行文件名即"客户端名"，决定服务端使用哪个数据库（`<name>.db`）。
将 `chore` 可执行文件复制并重命名，即可让多个客户端写入独立的数据库。

```
chore [选项]

  -s <url>          服务端地址，默认 http://localhost:2026
  -v                上传成功后打印详情 URL 与列表 URL
  -w                不上传，直接用浏览器打开列表页
  -i <id>           按 id 获取内容并打印到 stdout
                    若该记录为图片（含 png 标签），在终端渲染 24-bit 彩色字符画预览
  -i <id> -c        按 id 获取内容并复制到剪贴板
                    若该记录为图片，复制图片原始字节（而非路径文字）
  -n <N>            获取最新可见的第 N 条记录（1=最新），打印 id 和完整内容
  -n <N> -c         获取第 N 条记录并复制到剪贴板（图片则复制图片字节）
  -title <text>     上传时指定标题
  -tags <a,b,c>     上传时指定标签（逗号分隔，最多 10 个）
  -version          打印编译时间、commit id 与 git tag（如有）后退出
```

**剪贴板内容检测顺序：**

1. 若剪贴板含**文字**：直接上传文字
2. 若剪贴板含 **PNG 图片**：上传图片（自动添加 `png` 标签，标题默认 `png`）
3. 其他内容：报错并显示剪贴板类型信息

**平台支持（图片上传与图片复制）：**

| 平台 | 实现方式 | 依赖 |
|------|---------|------|
| macOS | AppleScript（`osascript`） | 系统内置 |
| Linux | `xclip -t image/png` | 需安装 xclip |
| Windows | PowerShell（`System.Windows.Forms`） | 系统内置（.NET Framework） |

**终端字符画预览（`-i <id>`，图片记录）：**

使用 `▄`（下半块）配合 24-bit ANSI 真彩色，每个字符覆盖 2 行像素，等比缩放到终端宽度。
终端宽度检测顺序：`$COLUMNS` 环境变量 → `tput cols` → 默认 80 列。

**短标志组合示例：**

```bash
chore                          # 上传剪贴板（文字或图片自动识别）
chore -v                       # 上传并打印 URL
chore -w                       # 不上传，用浏览器打开列表页
chore -vc                      # 上传、打印 URL
chore -i 5                     # 获取 #5 并打印（图片则终端预览）
chore -i 5 -c                  # 获取 #5 并复制到剪贴板（图片则复制图片字节）
chore -icv 5                   # 获取 #5、复制、打印 URL
chore -n 1                     # 获取最新可见记录（打印 id 和内容）
chore -n 2 -c                  # 获取第 2 新记录并复制
chore -title "今日笔记" -tags md,safe   # 上传并指定标题与标签
chore -s http://host:9000      # 指定自定义服务端
chore -version                 # 打印版本信息
```

## chore_svr 服务端 / 本地工具

```
chore_svr [选项]

服务端模式（默认，无 -i / -q / -limit）：
  -addr  <addr>    监听地址，默认 :2026
  -dbDir <dir>     数据目录，默认 ./
                   SQLite 文件：<dir>/<name>.db
                   图片目录：  <dir>/<name>.img/

本地工具模式（直连数据库，不受 hide 标签过滤）：
  -name  <name>    指定数据库名，默认按可执行文件名推断
  -i  <id>         按 id 查询单条（支持 "2-10" 或 "1,4,7" 格式）
  -i  <id> -c      查询并复制到剪贴板
  -i  <id> -delete 按 id 删除（支持范围/列表）
  -i  <id> -title "x" [-tags "a,b"]   更新 title 或 tags（单条）
  -q  <keyword>    本地搜索并打印（最多 20 条，可用 -limit 指定）
  -limit <N>       无 -q 时：列出最新 N 条
  -version         打印编译时间、commit id 与 git tag（如有）后退出
```

**名称推断顺序：** `-name` > `chore.json` 中的 `name` 字段 > 可执行文件名（去掉 `_svr` 后缀）

## HTTP API

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/api/paste` | 上传文字（Body：纯文本 或 JSON `{"content":"...","title":"...","tags":["a","b"]}`）|
| `POST` | `/api/image` | 上传 PNG 图片（Body：原始字节流；Query：`title=`、`tags=`）|
| `PATCH` | `/api/paste/:id` | 更新标题/标签（Body：JSON `{"title":"...","tags":["a","b"]}`，可省略任意字段）|
| `GET` | `/list[/:name]` | 分页历史列表；`?q=keyword` 搜索；`?page=1&per_page=20` 分页 |
| `GET` | `/detail/:name/:id` | 单条记录详情页 |
| `GET` | `/img/:name/:file` | 提供已上传的图片文件 |
| `GET` | `/search[/:name]` | 兼容旧链接，301 重定向到 `/list` |

**上传示例：**

```bash
# 纯文本
curl -X POST http://localhost:2026/api/paste \
  -H "X-Client-Name: chore" \
  -d "hello world"

# JSON（携带标题与标签）
curl -X POST http://localhost:2026/api/paste \
  -H "Content-Type: application/json" \
  -H "X-Client-Name: chore" \
  -d '{"content":"hello","title":"测试","tags":["md","safe"]}'

# 上传 PNG 图片
curl -X POST http://localhost:2026/api/image \
  -H "Content-Type: application/octet-stream" \
  -H "X-Client-Name: chore" \
  --data-binary @screenshot.png
```

## 内置标签

标签写在 `-tags` 参数中（逗号分隔），服务端按精确匹配识别以下内置语义。
多个标签可以叠加，渲染类标签互斥时按优先级取第一个生效。

### 访问控制类

| 标签 | 作用 |
|------|------|
| `safe` | Web 列表与详情页**不展示正文**（占位"已隐藏"）；数据库仍存全文，`chore_svr -i <id>` 可查看 |
| `hide` | HTTP 列表/搜索/详情**完全过滤该行**（相当于对 Web 不存在）；`chore_svr` 本地工具直连数据库仍可见 |

### 正文渲染类（详情页）

渲染优先级从高到低：`png` > `md` > `json` > `yaml` > `sh` > `url` > 纯文本（默认）

| 标签 | 渲染方式 | 说明 |
|------|---------|------|
| `png` | 以 `<img>` 标签展示图片 | 上传图片时自动添加；content 字段存储图片 Web 路径 |
| `md` | Markdown 渲染（marked.js，CDN 按需加载）；加载失败降级纯文本 | marked.js（MIT） |
| `json` | 服务端解析 → 可折叠树 + 语法配色；非法 JSON 退回转义原文 | 无前端依赖 |
| `yaml` | 服务端解析 → 可折叠树（同 json 风格）；解析失败退回转义原文 | gopkg.in/yaml.v3（Apache-2.0） |
| `sh` | bash 语法高亮（Prism，CDN 按需加载）；**仅展示，不执行** | Prism.js（MIT） |
| `url` | 正文中 `http(s)://` 链接转为可点击 `<a>` | 无前端依赖 |

**标签叠加示例：**

```bash
# Markdown + 安全隐藏（Web 不展示正文）
chore -tags md,safe

# JSON + 隐藏行（Web 列表完全不显示）
chore -tags json,hide

# Shell 脚本高亮
chore -tags sh
```

## 数据存储

每个客户端名在 `dbDir` 下对应一组文件：

| 文件/目录 | 说明 |
|-----------|------|
| `<name>.db` | SQLite 数据库（文字记录） |
| `<name>.img/` | PNG 图片目录（按时间戳命名） |

`paste` 表结构：

| 列 | 类型 | 说明 |
|----|------|------|
| `id` | INTEGER PK | 自增主键 |
| `title` | TEXT | 标题（空时取内容前 8 字符；图片默认 `png`） |
| `tags` | TEXT | 规范化后的逗号分隔标签（去重、排序） |
| `content` | TEXT | 正文（文字内容 或 图片 Web 路径 `/img/<name>/<file>`） |
| `created_at` | DATETIME | 写入时间（UTC） |
| `client_ip` | TEXT | 来源 IP |
| `user_agent` | TEXT | 来源 User-Agent |

## Go 客户端模块

`client` 包封装了对 `chore_svr` 的全部 HTTP 操作，其他程序可直接 import 使用：

```go
import "chore/client"

c := client.New("http://localhost:2026", "myapp")

// 上传文字
result, err := c.UploadText(ctx, "hello", "标题", []string{"md"})

// 上传图片（PNG 字节）
result, err = c.UploadImage(ctx, pngBytes, "", nil)

// 按 id 查询
paste, err := c.Get(ctx, result.ID)
if paste.HasTag("png") {
    // 拉取图片原始字节
    imgBytes, err := c.FetchImage(ctx, paste.Content)
}

// 分页列表 / 搜索
list, err := c.List(ctx, 1, 20)
list, err  = c.Search(ctx, "keyword", 1, 20)

// 更新标题/标签
newTitle := "new title"
err = c.Update(ctx, paste.ID, &newTitle, nil)
```

所有方法接受 `context.Context`，可统一控制超时：

```go
c := client.New(url, name).WithHTTPClient(&http.Client{Timeout: 5 * time.Second})
```

## 项目结构

```
chore/
├── client/         # 可复用 Go 客户端模块（封装 chore_svr HTTP API）
├── cmd/
│   ├── chore/      # 客户端入口（文字/图片上传、获取与预览、版本信息）
│   └── chore_svr/  # 服务端 / 本地工具入口
└── internal/
    ├── store/
    │   ├── store.go    # SQLite CRUD（Paste、Store、TagHide 等）
    │   └── manager.go  # 多数据库管理（Manager、SanitizeClientName）
    └── server/
        ├── handler.go         # HTTP 路由处理器、页面渲染、图片上传/提供
        ├── detail_registry.go # 详情页格式注册表（png/md/json/yaml/sh/url/plain）
        └── format_tree.go     # JSON/YAML 可折叠树渲染、附加 CSS
```
