# chore

剪贴板内容同步到服务端并持久化，支持预览与详情查看。

## 组成

- **chore**：命令行客户端，读取剪贴板并发送到 chore_svr
- **chore_svr**：HTTP 服务，接收内容写入 SQLite，提供预览与详情接口

## 使用

### 启动服务端

```bash
cd cmd/chore_svr && go run . -addr :2026 -db ./chore.db
```

- `-addr`：监听地址，默认 `:2026`
- `-db`：SQLite 数据库路径，默认 `./chore.db`

### 使用客户端

```bash
cd cmd/chore && go run . -server http://localhost:2026
```

- 将当前剪贴板内容 POST 到服务端并打印预览/详情链接
- `-server`：chore_svr 的 base URL，默认 `http://localhost:2026`

### 接口说明

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | /api/paste | 提交内容（Body 为纯文本或 JSON `{"content":"..."}`） |
| GET  | /preview/:id | 预览（短摘要 + 时间、IP） |
| GET  | /detail/:id  | 详情（完整内容 + 全部元数据） |

## 数据

SQLite 表 `paste`：id、content、created_at、client_ip、user_agent 等。
