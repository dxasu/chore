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
	tagMarkdown = "md"
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
// Body: 纯文本 或 JSON {"content": "...", "title": "...", "tags": ["a","b"]}
// Query: title=...&tags=a,b,c 也可传标题和标签（与 JSON 二选一或合并，JSON 优先）
func (s *Server) HandlePaste(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	contentType := r.Header.Get("Content-Type")
	var content, title string
	var tags []string
	if strings.Contains(contentType, "application/json") {
		var body struct {
			Content string   `json:"content"`
			Title   string   `json:"title"`
			Tags    []string `json:"tags"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		content = body.Content
		title = strings.TrimSpace(body.Title)
		tags = body.Tags
	} else {
		b := make([]byte, 512*1024) // 512KB
		n, _ := r.Body.Read(b)
		content = strings.TrimSpace(string(b[:n]))
	}
	if title == "" {
		title = strings.TrimSpace(r.URL.Query().Get("title"))
	}
	if len(tags) == 0 {
		if t := r.URL.Query().Get("tags"); t != "" {
			for _, v := range strings.Split(t, ",") {
				tags = append(tags, strings.TrimSpace(v))
			}
		}
	}

	clientIP := clientIP(r)
	userAgent := r.UserAgent()

	st, err := s.storeFor(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, createdAt, err := st.Add(r.Context(), content, title, tags, clientIP, userAgent)
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

// PATCH /api/paste/:id — update title and/or tags. Body: JSON {"title": "...", "tags": ["a","b"]}, omit fields to leave unchanged.
func (s *Server) HandlePatchPaste(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pathRest := strings.TrimPrefix(r.URL.Path, "/api/paste/")
	pathRest = strings.Trim(pathRest, "/")
	if pathRest == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	id, err := strconv.ParseInt(pathRest, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	var body struct {
		Title *string  `json:"title"`
		Tags  *[]string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if body.Title == nil && body.Tags == nil {
		http.Error(w, "provide title and/or tags", http.StatusBadRequest)
		return
	}
	st, err := s.storeFor(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	updated, err := st.Update(r.Context(), id, body.Title, body.Tags)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !updated {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"ok": "updated"})
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

// listTableRows 与 listTableStyles 供 list 与 search 共用，保证布局一致
func listTableRows(clientName string, list []*store.Paste, emptyColspan int, emptyMsg string) string {
	rows := ""
	for _, p := range list {
		preview := p.Content
		if len([]rune(preview)) > previewLen {
			preview = string([]rune(preview)[:previewLen]) + "..."
		}
		detailURL := "/detail/" + clientName + "/" + strconv.FormatInt(p.ID, 10)
		titleCell := p.Title
		if titleCell == "" {
			titleCell = "-"
		}
		tagsCell := p.Tags
		if tagsCell == "" {
			tagsCell = "-"
		}
		rows += `<tr>
  <td class="col-id">` + strconv.FormatInt(p.ID, 10) + `</td>
  <td class="col-title">` + escapeHTML(titleCell) + `</td>
  <td class="col-preview"><span class="preview-text">` + escapeHTML(preview) + `</span></td>
  <td class="col-tags">` + escapeHTML(tagsCell) + `</td>
  <td class="col-time">` + p.CreatedAt.Format("2006-01-02 15:04:05") + `</td>
  <td class="col-detail"><a href="` + detailURL + `">详情</a></td>
</tr>`
	}
	if rows == "" {
		rows = `<tr><td colspan="` + strconv.Itoa(emptyColspan) + `">` + emptyMsg + `</td></tr>`
	}
	return rows
}

const listTableStyles = `
    body { font-family: system-ui, sans-serif; max-width: 960px; margin: 2rem auto; padding: 0 1rem; }
    table { width: 100%; border-collapse: collapse; table-layout: fixed; }
    th, td { border: 1px solid #ddd; padding: 0.5rem 0.75rem; text-align: left; }
    th { background: #f5f5f5; }
    th.col-id { width: 4em; }
    th.col-title { width: 7em; }
    th.col-preview { width: 22em; }
    th.col-tags { width: 9em; }
    th.col-time { width: 11em; font-size: 0.85rem; }
    th.col-detail { width: 4em; }
    .col-time { font-size: 0.85rem; color: #555; }
    .col-preview .preview-text { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .pagination { margin-top: 1rem; }
    .pagination a { margin-right: 1rem; color: #2563eb; }
    a { color: #2563eb; }
`

const listTableHeader = `
    <thead><tr><th class="col-id">ID</th><th class="col-title">标题</th><th class="col-preview">内容预览</th><th class="col-tags">标签</th><th class="col-time">时间</th><th class="col-detail"></th></tr></thead>`

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
	rows := listTableRows(clientName, list, 6, "无匹配记录")
	title := "搜索"
	if query != "" {
		title = "搜索: " + escapeHTML(query)
	}
	return `<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <title>` + title + `</title>
  <style>` + listTableStyles + `
  </style>
</head>
<body>
  <h1>` + title + `</h1>
  <p class="meta">共 ` + strconv.Itoa(total) + ` 条，第 ` + strconv.Itoa(page) + ` / ` + strconv.Itoa(totalPages) + ` 页</p>
  <form method="get" action="` + base + `" style="margin-bottom:1rem;"><input name="q" value="` + escapeHTML(query) + `" placeholder="搜索内容或标签" /><button type="submit">搜索</button></form>
  <table>
` + listTableHeader + `
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
	rows := listTableRows(clientName, list, 6, "暂无记录")
	return `<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <title>历史记录</title>
  <style>` + listTableStyles + `
  </style>
</head>
<body>
  <h1>历史记录</h1>
  <p class="meta">共 ` + strconv.Itoa(total) + ` 条，第 ` + strconv.Itoa(page) + ` / ` + strconv.Itoa(totalPages) + ` 页</p>
  <form method="get" action="/search/` + clientName + `" style="margin-bottom:1rem;">
    <input name="q" placeholder="搜索内容或标签" />
    <button type="submit">搜索</button>
  </form>
  <table>
` + listTableHeader + `
    <tbody>` + rows + `</tbody>
  </table>
  <div class="pagination">` + prev + next + `</div>
</body>
</html>`
}

// looksLikeMarkdown checks if the tags indicate Markdown content
func looksLikeMarkdown(tags string) bool {
	tagsList := strings.Split(tags, ",")
	for _, tag := range tagsList {
		tag = strings.TrimSpace(tag)
		if tag == tagMarkdown {
			return true
		}
	}
	return false
}

func detailPage(clientName string, p *store.Paste) string {
	contentJSON, _ := json.Marshal(p.Content)
	contentJSONStr := string(contentJSON)
	contentJSONStr = strings.ReplaceAll(contentJSONStr, "</script>", "<\\/script>")
	isMD := looksLikeMarkdown(p.Tags)

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
  <title>` + escapeHTML(trimTitleForPage(p.Title, p.ID)) + `</title>
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
    .toolbar { margin-bottom: 0.5rem; position: relative; }
    .toolbar button { padding: 0.35rem 0.75rem; cursor: pointer; border: 1px solid #ccc; border-radius: 6px; background: #fff; }
    .toolbar button:hover { background: #f0f0f0; }
    .toast { position: fixed; left: 50%; top: 2rem; transform: translateX(-50%); padding: 0.4rem 0.9rem; background: #333; color: #fff; border-radius: 6px; font-size: 0.9rem; z-index: 9999; opacity: 0; transition: opacity 0.2s; pointer-events: none; }
    .toast.show { opacity: 1; }
    a { color: #2563eb; }
  </style>
</head>
<body>
  <h1>` + escapeHTML(trimTitleForPage(p.Title, p.ID)) + `</h1>
  <div class="meta">#` + strconv.FormatInt(p.ID, 10) + " · " + p.CreatedAt.Format("2006-01-02 15:04:05") + " · 标签: " + escapeHTML(p.Tags) + " · IP: " + escapeHTML(p.ClientIP) + " · " + escapeHTML(p.UserAgent) + `</div>
  <div class="toolbar"><button type="button" onclick="(function(){var t=document.getElementById('copy-toast');function show(m){t.textContent=m;t.classList.add('show');setTimeout(function(){t.classList.remove('show');},1500);}var c=window.rawContent;if(c===undefined){show('复制失败');return;}navigator.clipboard.writeText(c).then(function(){show('已复制');}).catch(function(){show('复制失败');});})();">复制</button><span id="copy-toast" class="toast" aria-live="polite"></span></div>
  ` + mdNote + `
  <script type="application/json" id="content-raw">` + contentJSONStr + `</script>
  <div id="content-area" class="content"></div>
  ` + mdScript + `
  <script>window.rawContent = document.getElementById('content-raw') ? JSON.parse(document.getElementById('content-raw').textContent) : '';</script>
  <p><a href="/list/` + clientName + `">返回列表</a></p>
</body>
</html>`
}

func trimTitleForPage(title string, id int64) string {
	if strings.TrimSpace(title) != "" {
		return title
	}
	return "Detail #" + strconv.FormatInt(id, 10)
}

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}
