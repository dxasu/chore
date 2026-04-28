# chore

剪贴板内容同步到服务端并持久化，支持 Web 预览、详情查看与本地管理。

## 组成

| 程序 | 说明 |
|------|------|
| **chore** | 命令行客户端：读取剪贴板并通过 HTTP 上传到 chore_svr |
| **chore_svr** | HTTP 服务 + 本地工具：接收内容写入 SQLite，提供 Web 列表/详情；也可直连数据库做查询/更新/删除 |

## 快速开始

```bash
# 1. 编译
go build ./cmd/chore_svr
go build ./cmd/chore

# 2. 启动服务端（默认监听 :2026，数据存 ./chore.db）
./chore_svr

# 3. 上传剪贴板内容
./chore

# 4. 浏览器查看列表
open http://localhost:2026/list/chore
```

## chore 客户端

可执行文件名即"客户端名"，决定服务端使用哪个数据库（`<name>.db`）。
将 `chore` 可执行文件复制并重命名，即可让多个客户端写入独立的数据库。

```
chore [选项]

  -s <url>          服务端地址，默认 http://localhost:2026
  -v                上传成功后打印详情 URL 与列表 URL
  -o                不上传，直接用浏览器打开列表页
  -i <id>           按 id 获取内容并打印到 stdout
  -i <id> -c        按 id 获取内容并复制到剪贴板
  -title <text>     上传时指定标题
  -tags <a,b,c>     上传时指定标签（逗号分隔，最多 10 个）
```

**短标志组合示例：**

```bash
chore                          # 上传剪贴板
chore -v                       # 上传并打印 URL
chore -vc                      # 上传、打印 URL（-v -c 组合）
chore -i 5                     # 获取 #5 并打印
chore -i 5 -c                  # 获取 #5 并复制到剪贴板
chore -icv 5                   # 获取 #5、复制、打印 URL（-i -c -v 组合）
chore -title "今日笔记" -tags md,safe   # 上传并指定标题与标签
chore -s http://host:9000      # 指定自定义服务端
```

## chore_svr 服务端 / 本地工具

```
chore_svr [选项]

服务端模式（默认，无 -i / -q / -limit）：
  -addr  <addr>    监听地址，默认 :2026
  -dbDir <dir>     SQLite 数据库目录，默认 ./（文件名为 <name>.db）

本地工具模式（直连数据库，不受 hide 标签过滤）：
  -name  <name>    指定数据库名，默认按可执行文件名推断
  -i  <id>         按 id 查询单条（可用 "2-10" 或 "1,4,7" 格式）
  -i  <id> -c      查询并复制到剪贴板
  -i  <id> -delete 按 id 删除（支持范围/列表）
  -i  <id> -title "x" [-tags "a,b"]   更新 title 或 tags（单条）
  -q  <keyword>    本地搜索并打印（最多 20 条，可用 -limit 指定）
  -limit <N>       无 -q 时：列出最新 N 条
```

**名称推断顺序：** `-name` > `chore.json` 中的 `name` 字段 > 可执行文件名（去掉 `_svr` 后缀）

## HTTP API

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/api/paste` | 上传内容（Body：纯文本 或 JSON `{"content":"...","title":"...","tags":["a","b"]}`）|
| `PATCH` | `/api/paste/:id` | 更新标题/标签（Body：JSON `{"title":"...","tags":["a","b"]}`，可省略任意字段）|
| `GET` | `/list[/:name]` | 分页历史列表；`?q=keyword` 搜索；`?page=1&per_page=20` 分页 |
| `GET` | `/detail/:name/:id` | 单条记录详情页 |
| `GET` | `/search[/:name]` | 兼容旧链接，301 重定向到 `/list` |

**上传请求示例：**

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

渲染优先级从高到低：`md` > `json` > `yaml` > `sh` > `url` > 纯文本（默认）

| 标签 | 渲染方式 | 依赖 |
|------|---------|------|
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

## 数据库

每个客户端名对应 `<dbDir>/<name>.db` 一个 SQLite 文件。
`paste` 表结构：

| 列 | 类型 | 说明 |
|----|------|------|
| `id` | INTEGER PK | 自增主键 |
| `title` | TEXT | 标题（空时取内容前 8 字符） |
| `tags` | TEXT | 规范化后的逗号分隔标签（去重、排序） |
| `content` | TEXT | 正文（最大 512 KB） |
| `created_at` | DATETIME | 写入时间（UTC） |
| `client_ip` | TEXT | 来源 IP |
| `user_agent` | TEXT | 来源 User-Agent |

## 项目结构

```
chore/
├── cmd/
│   ├── chore/          # 客户端入口
│   └── chore_svr/      # 服务端 / 本地工具入口
└── internal/
    ├── store/
    │   ├── store.go    # SQLite CRUD（Paste、Store、TagHide 等）
    │   └── manager.go  # 多数据库管理（Manager、SanitizeClientName）
    └── server/
        ├── handler.go         # HTTP 路由处理器、页面渲染
        ├── detail_registry.go # 详情页格式注册表（md/json/yaml/sh/url/plain）
        └── format_tree.go     # JSON/YAML 可折叠树渲染、附加 CSS
```
