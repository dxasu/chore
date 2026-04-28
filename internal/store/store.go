// Package store 提供基于 SQLite 的 paste 持久化层。
//
// 每个"客户端名"（如可执行文件名 abc）对应一个独立的 SQLite 数据库文件（abc.db），
// 由 Manager 统一管理和缓存。Store 封装对单个数据库的 CRUD 及搜索操作。
//
// 内置标签约定（与 server 包共用）：
//   - TagHide：经 HTTP 接口的列表/搜索/详情查询不返回含该标签的行；
//     本地 CLI 直连数据库时传 excludeHideTag=false 仍可看到。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// maxTags 是单条记录允许携带的最大标签数量，超出部分在 normalizeTags 时截断。
const maxTags = 10

// TagHide 是内置的隐藏标签字面量。
// 经 HTTP 接口（列表/搜索/详情）查询时，含该标签的行不会出现在返回结果中。
// 本地 CLI 直连数据库（chore_svr -i / -q / -limit）时传 excludeHideTag=false，仍可查看全部记录。
const TagHide = "hide"

// ErrDuplicateLatestContent 在上传内容与数据库中最新一条完全相同时返回，
// 防止重复剪贴板内容产生冗余记录。
var ErrDuplicateLatestContent = errors.New("duplicate latest content")

// excludeHideTagSQL 排除 tags 中含独立 token TagHide 的行（与 normalizeTags 逗号分隔、有序存储一致）。
func excludeHideTagSQL() string {
	h := TagHide
	return fmt.Sprintf(
		` AND NOT (tags = '%[1]s' OR tags LIKE '%[1]s,%%' OR tags LIKE '%%%,%[1]s,%%' OR tags LIKE '%%%%,%[1]s')`,
		h,
	)
}

// Paste 表示数据库中的一条 paste 记录，与 SQLite paste 表行一一对应。
// Tags 由 normalizeTags 处理后存储：去重、最多 maxTags 个、按字符升序排列、逗号分隔。
// 例如上传时传入 ["sh","safe"] 最终存储为 "safe,sh"。
type Paste struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	Tags      string    `json:"tags"` // 逗号分隔，最多 10 个，按字符顺序排列
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	ClientIP  string    `json:"client_ip"`
	UserAgent string    `json:"user_agent"`
}

// Store 封装对单个 SQLite 数据库的所有操作。
// 通过 New 创建，关闭时调用 Close 释放底层连接。
// 并发安全性由 database/sql 连接池保证，无需外部加锁。
type Store struct {
	db *sql.DB
}

// New 打开（或创建）位于 dbPath 的 SQLite 数据库，执行 schema 迁移后返回 Store。
// dbPath 应为可写路径，如 "./chore.db"。
func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

// migrate 确保 paste 表及索引存在，并调用 ensureColumns 处理旧库的列缺失。
func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS paste (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT NOT NULL DEFAULT '',
			tags TEXT NOT NULL DEFAULT '',
			content TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT (datetime('now')),
			client_ip TEXT NOT NULL DEFAULT '',
			user_agent TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX IF NOT EXISTS idx_paste_created_at ON paste(created_at DESC);
	`)
	if err != nil {
		return err
	}
	return s.ensureColumns()
}

// ensureColumns 为已存在的表补充 title、tags 列（新建表已包含则无操作）
func (s *Store) ensureColumns() error {
	rows, err := s.db.Query("PRAGMA table_info(paste)")
	if err != nil {
		return err
	}
	defer rows.Close()
	var hasTitle, hasTags bool
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt *string
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == "title" {
			hasTitle = true
		}
		if name == "tags" {
			hasTags = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !hasTitle {
		if _, err := s.db.Exec("ALTER TABLE paste ADD COLUMN title TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	if !hasTags {
		if _, err := s.db.Exec("ALTER TABLE paste ADD COLUMN tags TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	return nil
}

// defaultTitle 在调用方未提供标题时，取内容前 8 个 Unicode 字符作为默认标题。
// 若内容为空则返回空串，由上层决定是否保留空标题。
func defaultTitle(content string) string {
	r := []rune(strings.TrimSpace(content))
	if len(r) == 0 {
		return ""
	}
	if len(r) > 8 {
		return string(r[:8])
	}
	return string(r)
}

// normalizeTags 对传入的标签切片做规范化，规则：
//  1. 逐项 trim 空白，跳过空串和重复项；
//  2. 截取最多 maxTags 个（超出部分丢弃）；
//  3. 按字符升序排列后以逗号连接。
//
// 规范化后的字符串可直接存入数据库，也便于 excludeHideTagSQL 做前缀/后缀匹配。
func normalizeTags(tags []string) string {
	seen := make(map[string]bool)
	var list []string
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		list = append(list, t)
	}
	if len(list) > maxTags {
		list = list[:maxTags]
	}
	sort.Strings(list)
	return strings.Join(list, ",")
}

// Add 写入一条新记录，返回自增 id 与写入时间。
// 若 title 为空则自动取 content 前 8 个字符。tags 经 normalizeTags 规范化后存储。
// 当新内容与数据库最新一条完全相同时，返回 ErrDuplicateLatestContent。
func (s *Store) Add(ctx context.Context, content, title string, tags []string, clientIP, userAgent string) (int64, time.Time, error) {
	var latest string
	err := s.db.QueryRowContext(ctx, `SELECT content FROM paste ORDER BY created_at DESC, id DESC LIMIT 1`).Scan(&latest)
	if err != nil && err != sql.ErrNoRows {
		return 0, time.Time{}, err
	}
	if err == nil && latest == content {
		return 0, time.Time{}, ErrDuplicateLatestContent
	}

	if title == "" {
		title = defaultTitle(content)
	}
	tagsStr := normalizeTags(tags)
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO paste (title, tags, content, created_at, client_ip, user_agent) VALUES (?, ?, ?, ?, ?, ?)`,
		title, tagsStr, content, now, clientIP, userAgent,
	)
	if err != nil {
		return 0, time.Time{}, err
	}
	id, err := res.LastInsertId()
	return id, now, err
}

// Get 按 id 查询单条记录，包含 TagHide 的记录同样返回（供本地 CLI 及内部 Update 调用）。
// 记录不存在时返回 (nil, nil)。
func (s *Store) Get(ctx context.Context, id int64) (*Paste, error) {
	return s.getPaste(ctx, id, false)
}

// GetVisibleHTTP 供 HTTP 处理器使用：含 TagHide 标签的记录视为不存在，返回 (nil, nil)，
// 使 HTTP 客户端和浏览器收到 404，与 List/Search 的过滤语义保持一致。
func (s *Store) GetVisibleHTTP(ctx context.Context, id int64) (*Paste, error) {
	return s.getPaste(ctx, id, true)
}

// getPaste 是 Get 与 GetVisibleHTTP 的共同实现。
// excludeHideTag 为 true 时在 WHERE 条件后附加 excludeHideTagSQL()。
func (s *Store) getPaste(ctx context.Context, id int64, excludeHideTag bool) (*Paste, error) {
	suffix := ""
	if excludeHideTag {
		suffix = excludeHideTagSQL()
	}
	var p Paste
	err := s.db.QueryRowContext(ctx,
		`SELECT id, title, tags, content, created_at, client_ip, user_agent FROM paste WHERE id = ?`+suffix, id,
	).Scan(&p.ID, &p.Title, &p.Tags, &p.Content, &p.CreatedAt, &p.ClientIP, &p.UserAgent)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Update 更新指定 id 记录的 title 和/或 tags。
// 传入 nil 指针表示该字段保持不变；两个都为 nil 时调用方应提前检查并返回错误。
// 记录不存在返回 (false, nil)，更新成功返回 (true, nil)。
// 内部使用 Get（不过滤 hide），保证即使是隐藏记录也可被本地工具更新。
func (s *Store) Update(ctx context.Context, id int64, title *string, tags *[]string) (updated bool, err error) {
	p, err := s.Get(ctx, id)
	if err != nil {
		return false, err
	}
	if p == nil {
		return false, nil
	}
	newTitle := p.Title
	if title != nil {
		newTitle = strings.TrimSpace(*title)
	}
	newTags := p.Tags
	if tags != nil {
		newTags = normalizeTags(*tags)
	}
	_, err = s.db.ExecContext(ctx, `UPDATE paste SET title = ?, tags = ? WHERE id = ?`, newTitle, newTags, id)
	if err != nil {
		return false, err
	}
	return true, nil
}

// List 分页列出历史记录，按 created_at 倒序。offset/limit 为分页参数，返回本页列表与总数。
// excludeHideTag 为 true 时不返回 tags 中含 hide 的行（供 Web/远程客户端）；本地工具传 false 查看全部。
func (s *Store) List(ctx context.Context, offset, limit int, excludeHideTag bool) ([]*Paste, int64, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	hideSQL := ""
	if excludeHideTag {
		hideSQL = excludeHideTagSQL()
	}
	var total int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM paste WHERE 1=1`+hideSQL).Scan(&total)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, tags, content, created_at, client_ip, user_agent FROM paste WHERE 1=1`+hideSQL+` ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		limit, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var list []*Paste
	for rows.Next() {
		var p Paste
		if err := rows.Scan(&p.ID, &p.Title, &p.Tags, &p.Content, &p.CreatedAt, &p.ClientIP, &p.UserAgent); err != nil {
			return nil, 0, err
		}
		list = append(list, &p)
	}
	return list, total, rows.Err()
}

// Delete 删除指定 id 的记录，返回实际删除的行数（0 表示记录不存在，1 表示成功删除）。
// 不过滤 hide 标签，本地 CLI 可以删除任意记录。
func (s *Store) Delete(ctx context.Context, id int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM paste WHERE id = ?`, id)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// Search 按标题、内容或标签模糊搜索，按 created_at 倒序，最多返回 limit 条（内部限制最大 20）。返回本页列表与匹配总数。
// excludeHideTag 为 true 时匹配结果中再排除含 hide 标签的行（供 Web/远程客户端）。
func (s *Store) Search(ctx context.Context, query string, offset, limit int, excludeHideTag bool) ([]*Paste, int64, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 20 {
		limit = 20
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return []*Paste{}, 0, nil
	}

	field := "all"
	value := query
	if idx := strings.Index(query, ":"); idx > 0 {
		key := strings.ToLower(strings.TrimSpace(query[:idx]))
		if key == "title" || key == "content" || key == "tags" {
			field = key
			value = strings.TrimSpace(query[idx+1:])
		}
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return []*Paste{}, 0, nil
	}
	pattern := "%" + value + "%"

	var (
		whereClause string
		whereArgs   []any
	)
	switch field {
	case "title":
		whereClause = "title LIKE ?"
		whereArgs = []any{pattern}
	case "content":
		whereClause = "content LIKE ?"
		whereArgs = []any{pattern}
	case "tags":
		whereClause = "tags LIKE ?"
		whereArgs = []any{pattern}
	default:
		whereClause = "(title LIKE ? OR content LIKE ? OR tags LIKE ?)"
		whereArgs = []any{pattern, pattern, pattern}
	}

	hideSQL := ""
	if excludeHideTag {
		hideSQL = excludeHideTagSQL()
	}
	fullWhere := "(" + whereClause + ")" + hideSQL

	var total int64
	countArgs := append([]any{}, whereArgs...)
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM paste WHERE `+fullWhere, countArgs...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}
	listArgs := append([]any{}, whereArgs...)
	listArgs = append(listArgs, limit, offset)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, tags, content, created_at, client_ip, user_agent FROM paste WHERE `+fullWhere+` ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		listArgs...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var list []*Paste
	for rows.Next() {
		var p Paste
		if err := rows.Scan(&p.ID, &p.Title, &p.Tags, &p.Content, &p.CreatedAt, &p.ClientIP, &p.UserAgent); err != nil {
			return nil, 0, err
		}
		list = append(list, &p)
	}
	return list, total, rows.Err()
}

// Close 关闭底层 SQLite 连接，Store 之后不可再使用。
func (s *Store) Close() error {
	return s.db.Close()
}
