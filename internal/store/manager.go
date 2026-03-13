package store

import (
	"path/filepath"
	"regexp"
	"sync"
)

// Manager 按客户端名管理多个 SQLite Store，每个名称对应一个 {name}.db 文件。
type Manager struct {
	dbDir  string
	mu     sync.RWMutex
	stores map[string]*Store
}

// 只允许字母、数字、下划线、连字符，避免路径注入
var safeClientName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func sanitizeClientName(name string) string {
	return SanitizeClientName(name)
}

// SanitizeClientName 供外部（如 server 拼 URL）使用，仅保留安全字符，空或非法返回 "chore"。
func SanitizeClientName(name string) string {
	if name == "" {
		return "chore"
	}
	if !safeClientName.MatchString(name) {
		return "chore"
	}
	return name
}

// NewManager 创建按 dbDir 下 {name}.db 管理 Store 的 Manager。
func NewManager(dbDir string) *Manager {
	return &Manager{dbDir: dbDir, stores: make(map[string]*Store)}
}

// GetStore 返回指定客户端名对应的 Store（无则创建并缓存）。name 会做安全过滤，空或非法则用 "chore"。
func (m *Manager) GetStore(name string) (*Store, error) {
	name = sanitizeClientName(name)
	m.mu.RLock()
	s, ok := m.stores[name]
	m.mu.RUnlock()
	if ok {
		return s, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok = m.stores[name]; ok {
		return s, nil
	}
	dbPath := filepath.Join(m.dbDir, name+".db")
	s, err := New(dbPath)
	if err != nil {
		return nil, err
	}
	m.stores[name] = s
	return s, nil
}

// Close 关闭所有已打开的 Store。
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var err error
	for _, s := range m.stores {
		if e := s.Close(); e != nil && err == nil {
			err = e
		}
	}
	m.stores = make(map[string]*Store)
	return err
}
