// Package store 的 manager.go 文件实现多数据库的并发管理。
// （包级注释见 store.go）
package store

import (
	"path/filepath"
	"regexp"
	"sync"
)

// Manager 按客户端名管理多个 SQLite Store，每个名称对应 dbDir 下的 {name}.db 文件。
//
// 设计原则：
//   - 懒加载：第一次 GetStore 时才打开数据库，之后复用缓存。
//   - 并发安全：读操作持读锁，写（创建新 Store）时升级为写锁，并做 double-check 避免重复创建。
//   - 名称白名单：仅允许 a-z A-Z 0-9 _ - 的名称，非法名称统一降级为 "chore"，防止路径穿越。
type Manager struct {
	dbDir  string
	mu     sync.RWMutex
	stores map[string]*Store
}

// safeClientName 只允许字母、数字、下划线、连字符，避免路径注入。
var safeClientName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// sanitizeClientName 是包内使用的简写，直接委托给导出版本。
func sanitizeClientName(name string) string {
	return SanitizeClientName(name)
}

// SanitizeClientName 过滤客户端名称，供 server 包拼接 URL 路径时调用。
// 空字符串或含非法字符时返回 "chore"，保证路径安全。
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
