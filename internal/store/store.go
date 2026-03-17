package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const maxTags = 10

var ErrDuplicateLatestContent = errors.New("duplicate latest content")

type Paste struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	Tags      string    `json:"tags"` // 逗号分隔，最多 10 个，按字符顺序排列
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	ClientIP  string    `json:"client_ip"`
	UserAgent string    `json:"user_agent"`
}

type Store struct {
	db *sql.DB
}

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

// defaultTitle 取内容前 8 个字符作为默认标题
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

// normalizeTags 去重、取最多 maxTags 个、按字符顺序排列，返回逗号分隔字符串
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

func (s *Store) Get(ctx context.Context, id int64) (*Paste, error) {
	var p Paste
	err := s.db.QueryRowContext(ctx,
		`SELECT id, title, tags, content, created_at, client_ip, user_agent FROM paste WHERE id = ?`, id,
	).Scan(&p.ID, &p.Title, &p.Tags, &p.Content, &p.CreatedAt, &p.ClientIP, &p.UserAgent)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Update updates title and/or tags for the given id. Nil pointer means leave unchanged. Returns (true, nil) when updated, (false, nil) when not found.
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
func (s *Store) List(ctx context.Context, offset, limit int) ([]*Paste, int64, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	var total int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM paste`).Scan(&total)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, tags, content, created_at, client_ip, user_agent FROM paste ORDER BY created_at DESC LIMIT ? OFFSET ?`,
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

// Delete 删除指定 id 的记录，返回实际删除的行数（0 或 1）；不存在则返回 0。
func (s *Store) Delete(ctx context.Context, id int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM paste WHERE id = ?`, id)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// Search 按标题、内容或标签模糊搜索，按 created_at 倒序，最多返回 limit 条（内部限制最大 20）。返回本页列表与匹配总数。
func (s *Store) Search(ctx context.Context, query string, offset, limit int) ([]*Paste, int64, error) {
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

	var total int64
	countArgs := append([]any{}, whereArgs...)
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM paste WHERE `+whereClause, countArgs...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}
	listArgs := append([]any{}, whereArgs...)
	listArgs = append(listArgs, limit, offset)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, tags, content, created_at, client_ip, user_agent FROM paste WHERE `+whereClause+` ORDER BY created_at DESC LIMIT ? OFFSET ?`,
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

func (s *Store) Close() error {
	return s.db.Close()
}
