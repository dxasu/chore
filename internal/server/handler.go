package server

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"chore/internal/store"
)

const previewLen = 200

// 内置特殊标签：上传时写在 tags 里（逗号分隔），经 trim 后与下列字面量完全匹配即由服务端识别。
// 便于查阅与维护，新增内置语义时请在此补充常量并在注释中说明用途。
const (
	specialTagMarkdown = "md"   // 详情页：正文按 Markdown 渲染
	specialTagJSON     = "json" // 详情页：合法 JSON 为可折叠树 + 简易配色；非法或多值 JSON 为转义原文（与 md 并存时 md 优先）
	specialTagYAML     = "yaml" // 详情页：YAML 可折叠树（gopkg.in/yaml.v3）；解析失败为转义原文（与 md 并存时 md 优先）
	specialTagSH       = "sh"   // 详情页：Shell 脚本仅 bash 语法高亮（Prism，MIT），不执行（与 md 并存时 md 优先）
	specialTagURL      = "url"  // 详情页（非 md）：正文中 http(s) URL 转为可点击链接
	specialTagSafe     = "safe" // 经 HTTP（浏览器、chore 等）的列表/详情 JSON 与 HTML 均不展示正文；库内仍存全文，chore_svr 本地读库命令可查看
)

// pasteHasTag 判断逗号分隔的 tags 中是否存在指定内置标签名（trim 后精确匹配）
func pasteHasTag(tags, name string) bool {
	for _, part := range strings.Split(tags, ",") {
		if strings.TrimSpace(part) == name {
			return true
		}
	}
	return false
}

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
		if errors.Is(err, store.ErrDuplicateLatestContent) {
			http.Error(w, "duplicate latest content", http.StatusConflict)
			return
		}
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
		Title *string   `json:"title"`
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

// GET /list 或 /list/:name 分页历史列表，query: q=...&page=1&per_page=20
// q 为空时返回历史列表，q 非空时返回搜索结果；/search 路由仅作兼容跳转到 /list。
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
	q := strings.TrimSpace(r.URL.Query().Get("q"))

	st, err := s.storeGetter.GetStore(clientName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	offset := (page - 1) * perPage
	var (
		list  []*store.Paste
		total int64
	)
	if q != "" {
		list, total, err = st.Search(r.Context(), q, offset, perPage)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		list, total, err = st.List(r.Context(), offset, perPage)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/json") {
		w.Header().Set("Content-Type", "application/json")
		totalPages := (int(total) + perPage - 1) / perPage
		items := redactSafeContentCopies(list)
		json.NewEncoder(w).Encode(map[string]any{
			"items":       items,
			"total":       total,
			"page":        page,
			"per_page":    perPage,
			"query":       q,
			"total_pages": totalPages,
		})
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	html := listPage(clientName, q, list, int(total), page, perPage)
	w.Write([]byte(html))
}

// GET /search 或 /search/:name?q=...&page=1
// 兼容旧链接：重定向到 /list（同样支持 q/page/per_page）。
func (s *Server) HandleSearchPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	toPath := strings.TrimPrefix(r.URL.Path, "/search")
	toPath = "/list" + toPath
	toURL := toPath
	if r.URL.RawQuery != "" {
		toURL += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, toURL, http.StatusMovedPermanently)
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
		json.NewEncoder(w).Encode(redactSafePasteForHTTP(p))
		return
	}

	// HTML 详情页
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	html := detailPage(clientName, redactSafePasteForHTTP(p))
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

// redactSafePasteForHTTP 对含 safe 的条目返回正文已清空的副本，供 HTTP JSON/HTML 使用（不修改 store）
func redactSafePasteForHTTP(p *store.Paste) *store.Paste {
	if p == nil || !pasteHasTag(p.Tags, specialTagSafe) {
		return p
	}
	cpy := *p
	cpy.Content = ""
	return &cpy
}

// redactSafeContentCopies 在 HTTP 列表 API 返回中，对含 safe 标签的条目清空 content（浅拷贝）
func redactSafeContentCopies(list []*store.Paste) []*store.Paste {
	out := make([]*store.Paste, len(list))
	for i, p := range list {
		if p == nil {
			out[i] = nil
			continue
		}
		if pasteHasTag(p.Tags, specialTagSafe) {
			cpy := *p
			cpy.Content = ""
			out[i] = &cpy
		} else {
			out[i] = p
		}
	}
	return out
}

// listTableRows 与 listTableStyles 供 list 与 search 共用，保证布局一致。
// 带 safe 标签的条目在「内容预览」列不展示正文（HTTP 列表页）。
func listTableRows(clientName string, list []*store.Paste, emptyColspan int, emptyMsg string) string {
	rows := ""
	for _, p := range list {
		var preview string
		if pasteHasTag(p.Tags, specialTagSafe) {
			preview = "（已隐藏）"
		} else {
			preview = p.Content
			if len([]rune(preview)) > previewLen {
				preview = string([]rune(preview)[:previewLen]) + "..."
			}
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
		previewClass := "preview-text"
		if pasteHasTag(p.Tags, specialTagSafe) {
			previewClass = "preview-text preview-hidden"
		}
		rows += `<tr>
  <td class="col-id">` + strconv.FormatInt(p.ID, 10) + `</td>
  <td class="col-title">` + escapeHTML(titleCell) + `</td>
  <td class="col-preview"><span class="` + previewClass + `">` + escapeHTML(preview) + `</span></td>
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
    :root {
      --bg: #f7f9fc;
      --panel: #ffffff;
      --border: #e5eaf2;
      --text: #1f2937;
      --muted: #6b7280;
      --primary: #2563eb;
      --primary-soft: #eff6ff;
    }
    body {
      font-family: "Segoe UI", "PingFang SC", "Microsoft YaHei", system-ui, sans-serif;
      color: var(--text);
      background: linear-gradient(180deg, #f8fbff 0%, var(--bg) 100%);
      max-width: 1080px;
      margin: 0 auto;
      padding: 2rem 1.25rem 2.5rem;
      line-height: 1.5;
    }
    h1 {
      margin: 0 0 0.35rem;
      font-size: 1.75rem;
      letter-spacing: 0.2px;
    }
    .meta {
      margin: 0 0 1rem;
      color: var(--muted);
      font-size: 0.95rem;
    }
    form {
      margin: 0 0 1rem;
      display: flex;
      gap: 0.5rem;
      align-items: center;
      flex-wrap: wrap;
    }
    form input {
      flex: 1 1 18rem;
      min-width: 16rem;
      border: 1px solid var(--border);
      border-radius: 10px;
      padding: 0.55rem 0.75rem;
      background: #fff;
      font-size: 0.95rem;
      outline: none;
    }
    form input:focus {
      border-color: #93c5fd;
      box-shadow: 0 0 0 3px var(--primary-soft);
    }
    form button {
      border: 0;
      border-radius: 10px;
      padding: 0.58rem 1rem;
      background: var(--primary);
      color: #fff;
      font-size: 0.92rem;
      cursor: pointer;
    }
    form button:hover {
      background: #1d4ed8;
    }
    table {
      width: 100%;
      border-collapse: separate;
      border-spacing: 0;
      table-layout: fixed;
      background: var(--panel);
      border: 1px solid var(--border);
      border-radius: 12px;
      overflow: hidden;
      box-shadow: 0 6px 20px rgba(15, 23, 42, 0.06);
    }
    th, td {
      border-bottom: 1px solid var(--border);
      padding: 0.62rem 0.8rem;
      text-align: left;
      vertical-align: middle;
    }
    tbody tr:last-child td {
      border-bottom: none;
    }
    tbody tr:hover {
      background: #fafcff;
    }
    th {
      background: #f3f6fb;
      color: #374151;
      font-weight: 600;
      font-size: 0.92rem;
    }
    th.col-id { width: 4em; }
    th.col-title { width: 8em; }
    th.col-preview { width: 24em; }
    th.col-tags { width: 10em; }
    th.col-time { width: 12em; font-size: 0.85rem; }
    th.col-detail { width: 4.5em; }
    .col-time { font-size: 0.85rem; color: var(--muted); }
    .col-tags { color: #4b5563; }
    .col-preview .preview-text { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .col-preview .preview-hidden { color: var(--muted); font-style: italic; }
    .pagination {
      margin-top: 1rem;
      display: flex;
      gap: 0.5rem;
      align-items: center;
    }
    .pagination a {
      color: var(--primary);
      text-decoration: none;
      border: 1px solid #c7dafc;
      background: var(--primary-soft);
      padding: 0.35rem 0.72rem;
      border-radius: 999px;
      font-size: 0.9rem;
    }
    .pagination a:hover {
      background: #dbeafe;
    }
    a {
      color: var(--primary);
      text-decoration: none;
    }
    a:hover {
      text-decoration: underline;
    }
`

const listTableHeader = `
    <thead><tr><th class="col-id">ID</th><th class="col-title">标题</th><th class="col-preview">内容预览</th><th class="col-tags">标签</th><th class="col-time">时间</th><th class="col-detail"></th></tr></thead>`

func listPage(clientName, query string, list []*store.Paste, total, page, perPage int) string {
	totalPages := (total + perPage - 1) / perPage
	if totalPages < 1 {
		totalPages = 1
	}
	base := "/list/" + clientName
	queryLink := ""
	if query != "" {
		queryLink = "&q=" + url.QueryEscape(query)
	}
	prev := ""
	if page > 1 {
		prev = `<a href="` + base + `?page=` + strconv.Itoa(page-1) + `&per_page=` + strconv.Itoa(perPage) + queryLink + `">上一页</a>`
	}
	next := ""
	if page < totalPages {
		next = `<a href="` + base + `?page=` + strconv.Itoa(page+1) + `&per_page=` + strconv.Itoa(perPage) + queryLink + `">下一页</a>`
	}
	title := "历史记录"
	emptyMsg := "暂无记录"
	if query != "" {
		title = "搜索: " + escapeHTML(query)
		emptyMsg = "无匹配记录"
	}
	rows := listTableRows(clientName, list, 6, emptyMsg)
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
  <form method="get" action="` + base + `">
    <input name="q" value="` + escapeHTML(query) + `" placeholder="搜索标题、内容或标签" />
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
	return pasteHasTag(tags, specialTagMarkdown)
}

func detailPage(clientName string, p *store.Paste) string {
	if p == nil {
		return ""
	}
	rawForEmbed := p.Content
	if pasteHasTag(p.Tags, specialTagSafe) {
		rawForEmbed = ""
	}
	contentJSON, _ := json.Marshal(rawForEmbed)
	contentJSONStr := string(contentJSON)
	contentJSONStr = strings.ReplaceAll(contentJSONStr, "</script>", "<\\/script>")
	isHiddenSafe := pasteHasTag(p.Tags, specialTagSafe)

	var frag detailBodyFragment
	if isHiddenSafe {
		frag = detailBodyForSafeHidden()
	} else {
		frag = selectDetailBodyByTags(p)
	}

	qrBoxClass := "qr-box"
	qrBoxTitle := "点击二维码复制内容"
	qrLabel := "点击复制内容"
	if isHiddenSafe {
		qrBoxClass = "qr-box qr-disabled"
		qrBoxTitle = "safe 记录不在此页复制正文"
		qrLabel = "不展示正文"
	}

	return `<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
` + frag.ExtraHead + `  <title>` + escapeHTML(trimTitleForPage(p.Title, p.ID)) + `</title>
  <style>
    :root {
      --bg: #f3f6fb;
      --panel: #ffffff;
      --border: #e5eaf2;
      --text: #1f2937;
      --muted: #9ca3af;
      --muted-soft: #b3bac5;
      --primary: #2563eb;
      --primary-soft: #eff6ff;
    }
    body {
      font-family: "Segoe UI", "PingFang SC", "Microsoft YaHei", system-ui, sans-serif;
      color: var(--text);
      background: radial-gradient(circle at 20% -20%, #ffffff 0%, var(--bg) 55%, #edf2f9 100%);
      max-width: 980px;
      margin: 0 auto;
      padding: 2.2rem 1.25rem 2.8rem;
      line-height: 1.6;
    }
    .page-shell {
      background: linear-gradient(180deg, rgba(255,255,255,0.94) 0%, rgba(255,255,255,0.9) 100%);
      border: 1px solid rgba(255,255,255,0.7);
      border-radius: 20px;
      box-shadow: 0 18px 40px rgba(15, 23, 42, 0.08);
      padding: 1.4rem 1.5rem 1.2rem;
      backdrop-filter: blur(2px);
    }
    .header-row {
      display: flex;
      align-items: flex-start;
      gap: 0.7rem;
      margin-bottom: 0.8rem;
    }
    .left-stack {
      min-width: 0;
      flex: 1;
      display: flex;
      flex-direction: column;
      height: 90px;
      justify-content: flex-start;
    }
    h1 {
      margin: 0;
      font-size: 1.62rem;
      color: #111111;
      font-weight: 700;
      font-family: "SimHei", "Microsoft YaHei", "PingFang SC", sans-serif;
      letter-spacing: 0.2px;
      line-height: 1.25;
      padding-bottom: 1px;
      flex-shrink: 0;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }
    .meta {
      color: var(--muted);
      font-size: 0.9rem;
      margin: 0.24rem 0 0;
      line-height: 1.35;
      padding-bottom: 1px;
      flex-shrink: 0;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }
    .content {
      word-break: break-word;
      background: #f8fbffcc;
      border: 1px solid var(--border);
      padding: 1rem 1.15rem;
      border-radius: 14px;
      box-shadow: inset 0 1px 0 rgba(255,255,255,0.8), 0 6px 20px rgba(15, 23, 42, 0.04);
    }
    .content.markdown-body { background: #fff; padding: 1rem; }
    .markdown-body h1,.markdown-body h2,.markdown-body h3 { margin: 1em 0 0.5em; }
    .markdown-body pre { background: #f3f6fb; border: 1px solid var(--border); padding: 0.75rem; border-radius: 8px; overflow-x: auto; }
    .markdown-body code { background: #f3f6fb; border: 1px solid var(--border); padding: 0.16em 0.35em; border-radius: 5px; font-size: 0.9em; }
    .markdown-body pre code { background: none; padding: 0; }
    .markdown-body ul,.markdown-body ol { margin: 0.5em 0; padding-left: 1.5rem; }
    .markdown-body a { color: var(--primary); }
    .toast { position: fixed; left: 50%; top: 2rem; transform: translateX(-50%); padding: 0.4rem 0.9rem; background: #333; color: #fff; border-radius: 6px; font-size: 0.9rem; z-index: 9999; opacity: 0; transition: opacity 0.2s; pointer-events: none; }
    .toast.show { opacity: 1; }
    a { color: var(--primary); text-decoration: none; }
    a:hover { text-decoration: underline; }
    .qr-box {
      background: #ffffffd9;
      border: 1px solid var(--border);
      border-radius: 10px;
      box-shadow: 0 6px 16px rgba(15, 23, 42, 0.08);
      padding: 0.3rem;
      text-align: center;
      width: 86px;
      flex: 0 0 auto;
      display: flex;
      flex-direction: column;
      align-items: center;
      justify-content: center;
      gap: 0.12rem;
      height: 90px;
      overflow: hidden;
      box-sizing: border-box;
      cursor: pointer;
    }
    .qr-box:hover { border-color: #bfd4f7; }
    .qr-box .label {
      margin: 0;
      color: #a4adb9;
      font-size: 0.58rem;
      line-height: 1.2;
    }
    .qr-box img {
      width: 66px;
      height: 66px;
      display: block;
      margin: 0 auto;
      border-radius: 6px;
      background: #fff;
    }
    .page-footer {
      margin-top: 0.95rem;
      font-size: 0.92rem;
      color: #8b95a3;
    }
    .content-hidden-placeholder { color: var(--muted); font-style: italic; }
    .qr-box.qr-disabled { opacity: 0.45; cursor: default; pointer-events: none; }
    @media (max-width: 760px) {
      .page-shell { padding: 1rem; border-radius: 16px; }
      .header-row { gap: 0.55rem; }
      .left-stack { height: 82px; }
      h1 { font-size: 1.38rem; }
      .meta { font-size: 0.82rem; }
      .qr-box { width: auto; height: 82px; padding: 0.28rem; }
      .qr-box img { width: 62px; height: 62px; }
    }
` + frag.ExtraFormatCSS + `
  </style>
</head>
<body>
  <main class="page-shell">
    <div class="header-row">
      <div class="left-stack">
        <h1>` + escapeHTML(trimTitleForPage(p.Title, p.ID)) + `</h1>
        <div class="meta">#` + strconv.FormatInt(p.ID, 10) + " · " + p.CreatedAt.Format("2006-01-02 15:04:05") + " · 标签: " + tagsToHTML(p.Tags) + " · IP: " + escapeHTML(p.ClientIP) + " · " + escapeHTML(p.UserAgent) + `</div>
      </div>
      <aside class="` + qrBoxClass + `" id="copy-via-qr" title="` + escapeHTMLAttr(qrBoxTitle) + `">
        <p class="label">` + escapeHTML(qrLabel) + `</p>
        <img id="page-qr" alt="当前页面二维码" />
      </aside>
    </div>
    <span id="copy-toast" class="toast" aria-live="polite"></span>
    ` + frag.MetaNote + `
    <script type="application/json" id="content-raw">` + contentJSONStr + `</script>
    <div id="content-area" class="content` + frag.ContentAreaClass + `" style="white-space: pre-wrap;">` + frag.ContentInner + `</div>
    ` + frag.FootScript + `
    <p class="page-footer"><a href="/list/` + clientName + `">返回列表</a></p>
  </main>
  <script>
    (function(){
      function showToast(msg){
        var t = document.getElementById('copy-toast');
        if (!t) return;
        t.textContent = msg;
        t.classList.add('show');
        setTimeout(function(){ t.classList.remove('show'); }, 1500);
      }
      function fallbackCopy(text){
        var ta = document.createElement('textarea');
        ta.value = text;
        ta.setAttribute('readonly', '');
        ta.style.position = 'fixed';
        ta.style.left = '-9999px';
        ta.style.top = '0';
        document.body.appendChild(ta);
        ta.select();
        ta.setSelectionRange(0, ta.value.length);
        var ok = false;
        try {
          ok = document.execCommand('copy');
        } catch (e) {}
        document.body.removeChild(ta);
        return ok;
      }
      function copyText(text){
        if (navigator.clipboard && window.isSecureContext) {
          return navigator.clipboard.writeText(text);
        }
        return new Promise(function(resolve, reject){
          if (fallbackCopy(text)) {
            resolve();
          } else {
            reject(new Error('copy failed'));
          }
        });
      }
      var img = document.getElementById('page-qr');
      var u = window.location.href || '';
      if (img) {
        img.src = 'https://api.qrserver.com/v1/create-qr-code/?size=240x240&margin=10&data=' + encodeURIComponent(u);
      }
      var copyArea = document.getElementById('copy-via-qr');
      if (!copyArea) return;
      copyArea.addEventListener('click', function(){
        var raw = '';
        var rawEl = document.getElementById('content-raw');
        if (rawEl) {
          try { raw = JSON.parse(rawEl.textContent || '""'); } catch (e) {}
        }
        if (!raw) {
          showToast('复制失败');
          return;
        }
        copyText(raw).then(function(){
          showToast('已复制');
        }).catch(function(){
          showToast('复制失败');
        });
      });
    })();
  </script>
  <script>window.rawContent = document.getElementById('content-raw') ? JSON.parse(document.getElementById('content-raw').textContent) : '';</script>
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

// escapeHTMLAttr 用于 HTML 属性值（如 href），避免断属性与 XSS
func escapeHTMLAttr(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}

// urlPattern 匹配正文中的 http/https URL（至空格或结束）
var urlPattern = regexp.MustCompile(`https?://[^\s<>"']+`)

// contentToHTML 将正文中的 URL 文本转为可点击链接，其余转义展示（保留换行，white-space: pre-wrap）
func contentToHTML(content string) string {
	inds := urlPattern.FindAllStringIndex(content, -1)
	if len(inds) == 0 {
		return escapeHTML(content)
	}
	var buf strings.Builder
	last := 0
	for _, ab := range inds {
		a, b := ab[0], ab[1]
		buf.WriteString(escapeHTML(content[last:a]))
		url := content[a:b]
		buf.WriteString(`<a href="`)
		buf.WriteString(escapeHTMLAttr(url))
		buf.WriteString(`" target="_blank" rel="noopener">`)
		buf.WriteString(escapeHTML(url))
		buf.WriteString("</a>")
		last = b
	}
	buf.WriteString(escapeHTML(content[last:]))
	return buf.String()
}

// tagsToHTML 将逗号分隔的标签转为 HTML（标签中不含 URL，仅转义展示）
func tagsToHTML(tags string) string {
	if strings.TrimSpace(tags) == "" {
		return "-"
	}
	var buf strings.Builder
	for i, part := range strings.Split(tags, ",") {
		tag := strings.TrimSpace(part)
		if tag == "" {
			continue
		}
		if i > 0 {
			buf.WriteString(", ")
		}
		buf.WriteString(escapeHTML(tag))
	}
	if buf.Len() == 0 {
		return "-"
	}
	return buf.String()
}
