package server

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"

	"chore/internal/store"
)

const (
	previewLen = 200
)

// StoreGetter 按客户端名返回 Store，用于按名选择 sqlite（如 abc -> abc.db）
type StoreGetter interface {
	GetStore(name string) (*store.Store, error)
}

type Server struct {
	storeGetter StoreGetter
}

func New(sg StoreGetter) *Server {
	return &Server{storeGetter: sg}
}

func (s *Server) storeFor(r *http.Request) (*store.Store, error) {
	name := r.Header.Get("X-Client-Name")
	return s.storeGetter.GetStore(name)
}

// POST /api/paste
// Body: 纯文本 或 JSON {"content": "..."}
func (s *Server) HandlePaste(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	contentType := r.Header.Get("Content-Type")
	var content string
	if strings.Contains(contentType, "application/json") {
		var body struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		content = body.Content
	} else {
		b := make([]byte, 512*1024) // 512KB
		n, _ := r.Body.Read(b)
		content = strings.TrimSpace(string(b[:n]))
	}

	clientIP := clientIP(r)
	userAgent := r.UserAgent()

	st, err := s.storeFor(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, createdAt, err := st.Add(r.Context(), content, clientIP, userAgent)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	nameForURL := store.SanitizeClientName(r.Header.Get("X-Client-Name"))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"id":         id,
		"created_at": createdAt.Format("2006-01-02 15:04:05"),
		"detail_url": "/detail/" + nameForURL + "/" + strconv.FormatInt(id, 10),
		"list_url":   "/list/" + nameForURL,
	})
}

// GET /list 或 /list/:name 分页历史列表，query: page=1&per_page=20
func (s *Server) HandleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pathRest := strings.TrimPrefix(r.URL.Path, "/list")
	pathRest = strings.Trim(pathRest, "/")
	clientName := pathRest
	if clientName == "" {
		clientName = r.Header.Get("X-Client-Name")
	}
	clientName = store.SanitizeClientName(clientName)

	page := 1
	perPage := 20
	if p := r.URL.Query().Get("page"); p != "" {
		if n, _ := strconv.Atoi(p); n > 0 {
			page = n
		}
	}
	if n := r.URL.Query().Get("per_page"); n != "" {
		if v, _ := strconv.Atoi(n); v > 0 && v <= 100 {
			perPage = v
		}
	}

	st, err := s.storeGetter.GetStore(clientName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	offset := (page - 1) * perPage
	list, total, err := st.List(r.Context(), offset, perPage)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/json") {
		w.Header().Set("Content-Type", "application/json")
		totalPages := (int(total) + perPage - 1) / perPage
		json.NewEncoder(w).Encode(map[string]any{
			"items":       list,
			"total":       total,
			"page":        page,
			"per_page":    perPage,
			"total_pages": totalPages,
		})
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	html := listPage(clientName, list, int(total), page, perPage)
	w.Write([]byte(html))
}

// GET /search 或 /search/:name?q=...&page=1 搜索结果的 HTML 分页页（从列表页搜索框进入）
func (s *Server) HandleSearchPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pathRest := strings.TrimPrefix(r.URL.Path, "/search")
	pathRest = strings.Trim(pathRest, "/")
	clientName := store.SanitizeClientName(pathRest)
	if clientName == "" {
		clientName = store.SanitizeClientName(r.Header.Get("X-Client-Name"))
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		if n, _ := strconv.Atoi(p); n > 0 {
			page = n
		}
	}
	perPage := 20
	st, err := s.storeGetter.GetStore(clientName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var list []*store.Paste
	var total int64
	if q != "" {
		offset := (page - 1) * perPage
		list, total, err = st.Search(r.Context(), q, offset, perPage)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		total = 0
	}
	totalPages := (int(total) + perPage - 1) / perPage
	if totalPages < 1 {
		totalPages = 1
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	html := searchPage(clientName, q, list, int(total), page, perPage)
	w.Write([]byte(html))
}

// GET /detail/:id 或 /detail/:name/:id
func (s *Server) HandleDetail(w http.ResponseWriter, r *http.Request) {
	pathRest := strings.TrimPrefix(r.URL.Path, "/detail/")
	pathRest = strings.Trim(pathRest, "/")
	parts := strings.Split(pathRest, "/")
	var clientName, idStr string
	switch len(parts) {
	case 1:
		clientName, idStr = "chore", parts[0]
	case 2:
		clientName, idStr = parts[0], parts[1]
	default:
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	st, err := s.storeGetter.GetStore(clientName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	p, err := st.Get(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if p == nil {
		http.NotFound(w, r)
		return
	}

	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/json") {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(p)
		return
	}

	// HTML 详情页
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	html := detailPage(clientName, p)
	w.Write([]byte(html))
}

func clientIP(r *http.Request) string {
	if x := r.Header.Get("X-Forwarded-For"); x != "" {
		return strings.TrimSpace(strings.Split(x, ",")[0])
	}
	if x := r.Header.Get("X-Real-IP"); x != "" {
		return strings.TrimSpace(x)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func searchPage(clientName, query string, list []*store.Paste, total, page, perPage int) string {
	totalPages := (total + perPage - 1) / perPage
	if totalPages < 1 {
		totalPages = 1
	}
	base := "/search/" + clientName
	qEsc := strings.ReplaceAll(query, "&", "&amp;")
	qEsc = strings.ReplaceAll(qEsc, "\"", "&quot;")
	queryParam := "q=" + qEsc
	prev := ""
	if page > 1 {
		prev = `<a href="` + base + `?` + queryParam + `&page=` + strconv.Itoa(page-1) + `">上一页</a>`
	}
	next := ""
	if page < totalPages {
		next = `<a href="` + base + `?` + queryParam + `&page=` + strconv.Itoa(page+1) + `">下一页</a>`
	}
	rows := ""
	for _, p := range list {
		preview := p.Content
		if len([]rune(preview)) > previewLen {
			preview = string([]rune(preview)[:previewLen]) + "..."
		}
		detailURL := "/detail/" + clientName + "/" + strconv.FormatInt(p.ID, 10)
		rows += `<tr>
  <td>` + strconv.FormatInt(p.ID, 10) + `</td>
  <td>` + p.CreatedAt.Format("2006-01-02 15:04:05") + `</td>
  <td>` + escapeHTML(preview) + `</td>
  <td><a href="` + detailURL + `">详情</a></td>
</tr>`
	}
	if rows == "" {
		rows = `<tr><td colspan="4">无匹配记录</td></tr>`
	}
	title := "搜索"
	if query != "" {
		title = "搜索: " + escapeHTML(query)
	}
	return `<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <title>` + title + `</title>
  <style>
    body { font-family: system-ui, sans-serif; max-width: 900px; margin: 2rem auto; padding: 0 1rem; }
    table { width: 100%; border-collapse: collapse; }
    th, td { border: 1px solid #ddd; padding: 0.5rem 0.75rem; text-align: left; }
    th { background: #f5f5f5; }
    .pagination { margin-top: 1rem; }
    .pagination a { margin-right: 1rem; color: #2563eb; }
    a { color: #2563eb; }
  </style>
</head>
<body>
  <h1>` + title + `</h1>
  <p class="meta">共 ` + strconv.Itoa(total) + ` 条，第 ` + strconv.Itoa(page) + ` / ` + strconv.Itoa(totalPages) + ` 页</p>
  <form method="get" action="` + base + `"><input name="q" value="` + escapeHTML(query) + `" placeholder="搜索内容" /><button type="submit">搜索</button></form>
  <table>
    <thead><tr><th>ID</th><th>时间</th><th>内容预览</th><th></th></tr></thead>
    <tbody>` + rows + `</tbody>
  </table>
  <div class="pagination">` + prev + next + `</div>
  <p><a href="/list/` + clientName + `">返回列表</a></p>
</body>
</html>`
}

func listPage(clientName string, list []*store.Paste, total, page, perPage int) string {
	totalPages := (total + perPage - 1) / perPage
	if totalPages < 1 {
		totalPages = 1
	}
	base := "/list/" + clientName
	prev := ""
	if page > 1 {
		prev = `<a href="` + base + `?page=` + strconv.Itoa(page-1) + `&per_page=` + strconv.Itoa(perPage) + `">上一页</a>`
	}
	next := ""
	if page < totalPages {
		next = `<a href="` + base + `?page=` + strconv.Itoa(page+1) + `&per_page=` + strconv.Itoa(perPage) + `">下一页</a>`
	}

	rows := ""
	for _, p := range list {
		preview := p.Content
		if len([]rune(preview)) > previewLen {
			preview = string([]rune(preview)[:previewLen]) + "..."
		}
		detailURL := "/detail/" + clientName + "/" + strconv.FormatInt(p.ID, 10)
		timeStr := p.CreatedAt.Format("2006-01-02 15:04:05")
		rows += `<tr>
  <td>` + strconv.FormatInt(p.ID, 10) + `</td>
  <td class="preview-cell"><span class="preview-text">` + escapeHTML(preview) + `</span><span class="row-time">` + timeStr + `</span></td>
  <td><a href="` + detailURL + `">详情</a></td>
</tr>`
	}
	if rows == "" {
		rows = `<tr><td colspan="3">暂无记录</td></tr>`
	}

	return `<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <title>历史记录</title>
  <style>
    body { font-family: system-ui, sans-serif; max-width: 900px; margin: 2rem auto; padding: 0 1rem; }
    table { width: 100%; border-collapse: collapse; table-layout: fixed; }
    th, td { border: 1px solid #ddd; padding: 0.5rem 0.75rem; text-align: left; }
    th { background: #f5f5f5; }
    th.col-id { width: 3em; }
    th.col-detail { width: 4em; }
    .preview-cell { display: flex; align-items: center; gap: 0.5rem; overflow: hidden; }
    .preview-cell .preview-text { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .preview-cell .row-time { flex-shrink: 0; font-size: 0.75rem; color: #666; }
    .pagination { margin-top: 1rem; }
    .pagination a { margin-right: 1rem; color: #2563eb; }
  </style>
</head>
<body>
  <h1>历史记录</h1>
  <p class="meta">共 ` + strconv.Itoa(total) + ` 条，第 ` + strconv.Itoa(page) + ` / ` + strconv.Itoa(totalPages) + ` 页</p>
  <form method="get" action="/search/` + clientName + `" style="margin-bottom:1rem;">
    <input name="q" placeholder="搜索内容" />
    <button type="submit">搜索</button>
  </form>
  <table>
    <thead><tr><th class="col-id">ID</th><th>内容预览</th><th class="col-detail"></th></tr></thead>
    <tbody>` + rows + `</tbody>
  </table>
  <div class="pagination">` + prev + next + `</div>
</body>
</html>`
}

// looksLikeMarkdown 简单启发式：是否像 Markdown（标题、加粗、代码块、列表等）
func looksLikeMarkdown(s string) bool {
	lines := strings.Split(s, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "##") || strings.HasPrefix(trimmed, "###") {
			return true
		}
		if strings.Contains(trimmed, "**") || strings.Contains(trimmed, "__") || strings.Contains(trimmed, "```") {
			return true
		}
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "1. ") {
			return true
		}
		if strings.Contains(trimmed, "](http") || strings.Contains(trimmed, "](https://") {
			return true
		}
	}
	return false
}

func detailPage(clientName string, p *store.Paste) string {
	contentJSON, _ := json.Marshal(p.Content)
	contentJSONStr := string(contentJSON)
	contentJSONStr = strings.ReplaceAll(contentJSONStr, "</script>", "<\\/script>")
	isMD := looksLikeMarkdown(p.Content)

	mdNote := ""
	mdScript := ""
	if isMD {
		mdNote = `<p class="meta">（Markdown 预览）</p>`
		mdScript = `
  <script>
    (function(){
      var rawContent = (function(){ var e = document.getElementById('content-raw'); return e ? JSON.parse(e.textContent) : ''; })();
      var el = document.getElementById('content-area');
      function render(){
        if (typeof marked !== 'undefined') {
          el.innerHTML = marked.parse(rawContent);
          el.classList.add('markdown-body');
        } else {
          el.textContent = rawContent;
          el.style.whiteSpace = 'pre-wrap';
        }
      }
      var s = document.createElement('script');
      s.src = 'https://cdn.jsdelivr.net/npm/marked/marked.min.js';
      s.onload = render;
      s.onerror = function(){ el.textContent = rawContent; el.style.whiteSpace = 'pre-wrap'; };
      document.head.appendChild(s);
    })();
  </script>`
	} else {
		mdScript = `
  <script>
    (function(){
      var e = document.getElementById('content-raw');
      var rawContent = e ? JSON.parse(e.textContent) : '';
      var el = document.getElementById('content-area');
      el.textContent = rawContent;
      el.style.whiteSpace = 'pre-wrap';
      window.rawContent = rawContent;
    })();
  </script>`
	}

	return `<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <title>Detail #` + strconv.FormatInt(p.ID, 10) + `</title>
  <style>
    body { font-family: system-ui, sans-serif; max-width: 720px; margin: 2rem auto; padding: 0 1rem; }
    .meta { color: #666; font-size: 0.9rem; margin-bottom: 1rem; }
    .content { word-break: break-word; background: #f5f5f5; padding: 1rem; border-radius: 8px; }
    .content.markdown-body { background: #fff; padding: 1rem 0; }
    .markdown-body h1,.markdown-body h2,.markdown-body h3 { margin: 1em 0 0.5em; }
    .markdown-body pre { background: #f5f5f5; padding: 0.75rem; border-radius: 6px; overflow-x: auto; }
    .markdown-body code { background: #f0f0f0; padding: 0.2em 0.4em; border-radius: 4px; font-size: 0.9em; }
    .markdown-body pre code { background: none; padding: 0; }
    .markdown-body ul,.markdown-body ol { margin: 0.5em 0; padding-left: 1.5rem; }
    .markdown-body a { color: #2563eb; }
    .toolbar { margin-bottom: 0.5rem; }
    .toolbar button { padding: 0.35rem 0.75rem; cursor: pointer; border: 1px solid #ccc; border-radius: 6px; background: #fff; }
    .toolbar button:hover { background: #f0f0f0; }
    a { color: #2563eb; }
  </style>
</head>
<body>
  <h1>Detail #` + strconv.FormatInt(p.ID, 10) + `</h1>
  <div class="meta">` + p.CreatedAt.Format("2006-01-02 15:04:05") + " · IP: " + escapeHTML(p.ClientIP) + " · " + escapeHTML(p.UserAgent) + `</div>
  <div class="toolbar"><button type="button" onclick="var c=window.rawContent;if(c!==undefined){navigator.clipboard.writeText(c).then(function(){alert('已复制');}).catch(function(){alert('复制失败');});}else{alert('复制失败');}">复制</button></div>
  ` + mdNote + `
  <script type="application/json" id="content-raw">` + contentJSONStr + `</script>
  <div id="content-area" class="content"></div>
  ` + mdScript + `
  <script>window.rawContent = document.getElementById('content-raw') ? JSON.parse(document.getElementById('content-raw').textContent) : '';</script>
  <p><a href="/list/` + clientName + `">返回列表</a></p>
</body>
</html>`
}

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}
