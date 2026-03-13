package store

import (
	"context"
	"database/sql"
	"time"

	_ "modernc.org/sqlite"
)

type Paste struct {
	ID        int64     `json:"id"`
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
			content TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT (datetime('now')),
			client_ip TEXT NOT NULL DEFAULT '',
			user_agent TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX IF NOT EXISTS idx_paste_created_at ON paste(created_at DESC);
	`)
	return err
}

func (s *Store) Add(ctx context.Context, content, clientIP, userAgent string) (int64, time.Time, error) {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO paste (content, created_at, client_ip, user_agent) VALUES (?, ?, ?, ?)`,
		content, now, clientIP, userAgent,
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
		`SELECT id, content, created_at, client_ip, user_agent FROM paste WHERE id = ?`, id,
	).Scan(&p.ID, &p.Content, &p.CreatedAt, &p.ClientIP, &p.UserAgent)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
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
		`SELECT id, content, created_at, client_ip, user_agent FROM paste ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		limit, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var list []*Paste
	for rows.Next() {
		var p Paste
		if err := rows.Scan(&p.ID, &p.Content, &p.CreatedAt, &p.ClientIP, &p.UserAgent); err != nil {
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

// Search 按内容模糊搜索，按 created_at 倒序，最多返回 limit 条（内部限制最大 20）。返回本页列表与匹配总数。
func (s *Store) Search(ctx context.Context, query string, offset, limit int) ([]*Paste, int64, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 20 {
		limit = 20
	}
	pattern := "%" + query + "%"
	var total int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM paste WHERE content LIKE ?`, pattern).Scan(&total)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, content, created_at, client_ip, user_agent FROM paste WHERE content LIKE ? ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		pattern, limit, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var list []*Paste
	for rows.Next() {
		var p Paste
		if err := rows.Scan(&p.ID, &p.Content, &p.CreatedAt, &p.ClientIP, &p.UserAgent); err != nil {
			return nil, 0, err
		}
		list = append(list, &p)
	}
	return list, total, rows.Err()
}

func (s *Store) Close() error {
	return s.db.Close()
}
