// Package client 是 chore_svr HTTP API 的 Go 客户端封装。
//
// 其他程序通过 import "chore/client" 即可直接调用，无需手动处理 HTTP 细节：
//
//	c := client.New("http://localhost:2026", "myapp")
//
//	// 写操作
//	result, err := c.UploadText(ctx, "hello", "标题", []string{"md"})
//	result, err  = c.UploadImage(ctx, pngBytes, "", nil)
//	         err  = c.Update(ctx, id, &newTitle, &newTags)
//
//	// 读操作
//	paste,  err := c.Get(ctx, result.ID)
//	if paste.HasTag("png") {
//	    imgBytes, err := c.FetchImage(ctx, paste.Content) // 拉取图片原始字节
//	}
//	list,   err  = c.List(ctx, 1, 20)
//	results,err  = c.Search(ctx, "keyword", 1, 20)
//
// 所有方法均接受 context.Context，可统一控制超时和取消。
// 自定义超时示例：
//
//	c := client.New(url, name).WithHTTPClient(&http.Client{Timeout: 5 * time.Second})
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ──────────────────────────────────────────────
// 公共类型
// ──────────────────────────────────────────────

// Paste 表示服务端存储的一条记录，与 GET /detail 和 GET /list 返回的 JSON 对应。
type Paste struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	Tags      string    `json:"tags"`    // 逗号分隔，经规范化（去重、升序排列）
	Content   string    `json:"content"` // 文字内容或图片 Web 路径（含 png 标签时）
	CreatedAt time.Time `json:"created_at"`
	ClientIP  string    `json:"client_ip"`
	UserAgent string    `json:"user_agent"`
}

// UploadResult 是上传成功后服务端返回的摘要。
type UploadResult struct {
	ID        int64  `json:"id"`
	CreatedAt string `json:"created_at"` // "2006-01-02 15:04:05"
	DetailURL string `json:"detail_url"` // 相对路径，如 /detail/myapp/5
	ListURL   string `json:"list_url"`   // 相对路径，如 /list/myapp
}

// ListResult 是分页列表或搜索结果。
type ListResult struct {
	Items      []*Paste `json:"items"`
	Total      int64    `json:"total"`
	Page       int      `json:"page"`
	PerPage    int      `json:"per_page"`
	TotalPages int      `json:"total_pages"`
	Query      string   `json:"query"`
}

// ServerError 在服务端返回非 200 状态码时由各方法返回。
type ServerError struct {
	StatusCode int
	Body       string
}

func (e *ServerError) Error() string {
	return fmt.Sprintf("chore server %d: %s", e.StatusCode, e.Body)
}

// ErrNotFound 在查询的记录不存在（或含 hide 标签而对 HTTP 不可见）时返回。
var ErrNotFound = errors.New("chore: record not found")

// HasTag 判断该记录是否含有指定标签（精确匹配，大小写敏感）。
func (p *Paste) HasTag(tag string) bool {
	for _, t := range strings.Split(p.Tags, ",") {
		if strings.TrimSpace(t) == tag {
			return true
		}
	}
	return false
}

// ──────────────────────────────────────────────
// Client
// ──────────────────────────────────────────────

// Client 封装对 chore_svr HTTP API 的所有操作，并发安全。
// 通过 New 创建；零值不可用。
type Client struct {
	baseURL    string       // 服务端地址，不含尾部斜线
	clientName string       // 客户端名（对应服务端数据库名，如 "myapp" → myapp.db）
	httpClient *http.Client // 可通过 WithHTTPClient 替换
}

// New 创建 Client。
// baseURL 为服务端地址（如 "http://localhost:2026"）；
// clientName 为客户端名，决定服务端使用哪个数据库（同 chore 可执行文件名约定）。
func New(baseURL, clientName string) *Client {
	return &Client{
		baseURL:    strings.TrimSuffix(baseURL, "/"),
		clientName: clientName,
		httpClient: http.DefaultClient,
	}
}

// WithHTTPClient 替换内部使用的 *http.Client，返回自身以支持链式调用。
// 可用于设置超时、代理、TLS 配置等：
//
//	c := client.New(url, name).WithHTTPClient(&http.Client{Timeout: 5 * time.Second})
func (c *Client) WithHTTPClient(hc *http.Client) *Client {
	c.httpClient = hc
	return c
}

// ──────────────────────────────────────────────
// 写操作
// ──────────────────────────────────────────────

// UploadText 上传文字内容（POST /api/paste），返回记录摘要。
// title 为空时服务端自动取内容前 8 个字符；tags 为 nil 时不携带标签。
func (c *Client) UploadText(ctx context.Context, content, title string, tags []string) (*UploadResult, error) {
	body := struct {
		Content string   `json:"content"`
		Title   string   `json:"title,omitempty"`
		Tags    []string `json:"tags,omitempty"`
	}{Content: content, Title: title, Tags: tags}
	data, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/paste", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-Name", c.clientName)
	return c.doUpload(req)
}

// UploadImage 上传 PNG 图片字节（POST /api/image），返回记录摘要。
// 服务端会校验 PNG 魔数；title 为空时默认 "png"；tags 中若无 "png" 服务端会自动补充。
func (c *Client) UploadImage(ctx context.Context, data []byte, title string, tags []string) (*UploadResult, error) {
	endpoint := c.baseURL + "/api/image"
	params := url.Values{}
	if title != "" {
		params.Set("title", title)
	}
	if len(tags) > 0 {
		params.Set("tags", strings.Join(tags, ","))
	}
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Client-Name", c.clientName)
	return c.doUpload(req)
}

// Update 更新指定 id 记录的 title 和/或 tags（PATCH /api/paste/:id）。
// title 或 tags 传 nil 表示对应字段保持不变；两者均 nil 时返回错误。
// 记录不存在时返回 ErrNotFound。
func (c *Client) Update(ctx context.Context, id int64, title *string, tags *[]string) error {
	if title == nil && tags == nil {
		return errors.New("client.Update: title and tags cannot both be nil")
	}
	body := struct {
		Title *string   `json:"title,omitempty"`
		Tags  *[]string `json:"tags,omitempty"`
	}{Title: title, Tags: tags}
	data, _ := json.Marshal(body)

	endpoint := fmt.Sprintf("%s/api/paste/%d", c.baseURL, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, endpoint, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-Name", c.clientName)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return &ServerError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	}
	return nil
}

// ──────────────────────────────────────────────
// 读操作
// ──────────────────────────────────────────────

// Get 按 id 查询单条记录（GET /detail/:name/:id，Accept: application/json）。
// 含 hide 标签的记录对 HTTP 不可见，与不存在的记录同样返回 ErrNotFound。
func (c *Client) Get(ctx context.Context, id int64) (*Paste, error) {
	endpoint := fmt.Sprintf("%s/detail/%s/%d", c.baseURL, c.clientName, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, &ServerError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	}
	var p Paste
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, fmt.Errorf("decode paste: %w", err)
	}
	return &p, nil
}

// List 分页获取历史记录（GET /list/:name，按 created_at 倒序）。
// page 从 1 开始；perPage 最大 100，超出服务端会截断；传 0 使用服务端默认值（20）。
func (c *Client) List(ctx context.Context, page, perPage int) (*ListResult, error) {
	return c.query(ctx, "", page, perPage)
}

// Search 按关键词搜索记录（GET /list/:name?q=...），搜索范围覆盖 title、content、tags。
// 支持字段前缀语法：title:xxx、content:xxx、tags:xxx。
// page 从 1 开始；perPage 最大 20（服务端搜索上限）。
func (c *Client) Search(ctx context.Context, query string, page, perPage int) (*ListResult, error) {
	return c.query(ctx, query, page, perPage)
}

// FetchImage 通过图片 Web 路径获取原始字节（GET {baseURL}{webPath}）。
// webPath 通常为含 png 标签的 Paste.Content，例如 /img/myapp/20260430.png。
// 可与 Paste.HasTag("png") 配合使用：先判断标签，再拉取图片字节。
func (c *Client) FetchImage(ctx context.Context, webPath string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+webPath, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, &ServerError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	}
	return io.ReadAll(resp.Body)
}

// ──────────────────────────────────────────────
// 内部辅助
// ──────────────────────────────────────────────

func (c *Client) doUpload(req *http.Request) (*UploadResult, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, &ServerError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	}
	var out UploadResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode upload response: %w", err)
	}
	return &out, nil
}

func (c *Client) query(ctx context.Context, q string, page, perPage int) (*ListResult, error) {
	params := url.Values{}
	if q != "" {
		params.Set("q", q)
	}
	if page > 1 {
		params.Set("page", strconv.Itoa(page))
	}
	if perPage > 0 {
		params.Set("per_page", strconv.Itoa(perPage))
	}
	endpoint := c.baseURL + "/list/" + c.clientName
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, &ServerError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	}
	var result ListResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode list: %w", err)
	}
	return &result, nil
}
