// Package server 的 detail_registry.go 文件实现详情页正文区的「格式注册表」。
//
// 设计思路：
//
//	detailPage（handler.go）渲染页面外壳（标题栏、元数据、样式、复制/QR 区域等），
//	而正文区的具体内容（HTML 片段、附加样式、页脚脚本等）由本文件的注册表统一生产，
//	使 detailPage 无需关心具体格式逻辑，新增格式时仅需：
//	  1. 实现一个 renderDetailXxx(*store.Paste) detailBodyFragment 函数；
//	  2. 在 detailFormatRegistry 中、plain 之前插入一行。
//
// safe 标签（访问控制）由 detailPage 在进入注册表之前提前处理，与格式渲染解耦。
package server

import "chore/internal/store"

// detailBodyFragment 封装详情页「正文区」所需的全部可变片段。
// detailPage 将这些字段嵌入固定的 HTML 页面框架中，自身不包含格式判断逻辑。
type detailBodyFragment struct {
	// MetaNote 在正文区上方显示的格式说明段落（HTML，可为空）。
	// 例如 "（Markdown 预览）"、"（Shell：仅语法高亮）"。
	MetaNote string

	// ContentInner 是 #content-area div 内的初始 HTML 内容（可为空，由 FootScript 填充）。
	// 对于服务端可以完整生成的格式（json/yaml/sh），在此直接写入；
	// 对于依赖前端库的格式（md），此字段留空，由 FootScript 动态填充。
	ContentInner string

	// FootScript 是页面底部 <script> 标签的完整字符串（含 <script> 标签本身）。
	// 至少应设置 window.rawContent 供复制功能使用。
	FootScript string

	// ExtraHead 是注入到 <head> 的额外内容（如 Prism CSS 的 <link> 标签），含换行。
	// 大多数格式不需要此字段，置空即可。
	ExtraHead string

	// ExtraFormatCSS 是注入到页内 <style> 的附加 CSS 字符串（不含 <style> 标签）。
	// 由 detailFormatTreeCSS() 生成，json/yaml/sh 格式共用同一份样式。
	ExtraFormatCSS string

	// ContentAreaClass 是附加到 #content-area 的 CSS class（含前导空格，如 " content-formatted"）。
	// 空串表示使用默认的 .content 样式。
	ContentAreaClass string
}

// scriptSetWindowRawFromEmbed 是多种格式共用的页脚脚本片段：
// 从嵌入的 #content-raw（application/json script 标签）读取原始正文并赋值给 window.rawContent，
// 供页面右上角「点击复制内容」功能使用。
const scriptSetWindowRawFromEmbed = `
  <script>
    (function(){ window.rawContent = document.getElementById('content-raw') ? JSON.parse(document.getElementById('content-raw').textContent) : ''; })();
  </script>`

// detailFormatEntry 描述「详情页格式注册表」中的一个条目。
//
//   - name：仅用于调试和文档，不参与匹配逻辑。
//   - match：判断当前记录的 tags 是否应使用该格式；返回 true 时 render 被调用。
//   - render：根据 Paste 生成 detailBodyFragment；仅在 match 返回 true 时调用。
type detailFormatEntry struct {
	name   string
	match  func(tags string) bool
	render func(p *store.Paste) detailBodyFragment
}

// detailFormatRegistry 是详情页正文格式的有序注册表。
//
// 匹配规则：自顶向下扫描，第一个 match(tags)==true 的条目生效，其余跳过（类似 if-else if 链）。
// 最后一项 plain 的 match 恒为 true，作为默认兜底，必须保留在末尾。
//
// 当前顺序（即优先级）：markdown > json > yaml > shell > url > plain。
var detailFormatRegistry = []detailFormatEntry{
	{name: "markdown", match: looksLikeMarkdown, render: renderDetailMarkdown},
	{name: "json", match: func(tags string) bool { return pasteHasTag(tags, specialTagJSON) }, render: renderDetailJSON},
	{name: "yaml", match: func(tags string) bool { return pasteHasTag(tags, specialTagYAML) }, render: renderDetailYAML},
	{name: "shell", match: func(tags string) bool { return pasteHasTag(tags, specialTagSH) }, render: renderDetailShell},
	{name: "url", match: func(tags string) bool { return pasteHasTag(tags, specialTagURL) }, render: renderDetailURL},
	{name: "plain", match: func(tags string) bool { return true }, render: renderDetailPlain},
}

// renderDetailMarkdown 渲染 Markdown 格式：
// 正文内容由前端通过 marked.js（CDN）动态解析并插入 #content-area，
// 服务端不预处理，ContentInner 留空，FootScript 负责完整渲染流程。
// marked.js 加载失败时降级为 pre-wrap 纯文本。
func renderDetailMarkdown(_ *store.Paste) detailBodyFragment {
	return detailBodyFragment{
		MetaNote: `<p class="meta">（Markdown 预览）</p>`,
		FootScript: `
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
  </script>`,
	}
}

// renderDetailJSON 渲染 JSON 格式：
// 服务端用 encoding/json 解析，合法则生成可折叠树 HTML（renderValueFoldable），
// 非法或含多段 JSON 时退回 <pre> 转义原文。无额外前端依赖。
func renderDetailJSON(p *store.Paste) detailBodyFragment {
	return detailBodyFragment{
		MetaNote:         `<p class="meta">（JSON：可折叠树；非法或多段 JSON 时为转义原文）</p>`,
		ContentInner:     jsonDetailHTML(p.Content),
		ExtraFormatCSS:   detailFormatTreeCSS(),
		ContentAreaClass: " content-formatted",
		FootScript:       scriptSetWindowRawFromEmbed,
	}
}

// renderDetailYAML 渲染 YAML 格式：
// 服务端用 gopkg.in/yaml.v3 解析，经 normalizeYAMLValue 归一化 key 类型后，
// 与 JSON 共用同一套 renderValueFoldable 树渲染；解析失败退回 <pre> 转义原文。
func renderDetailYAML(p *store.Paste) detailBodyFragment {
	return detailBodyFragment{
		MetaNote:         `<p class="meta">（YAML：可折叠树；解析失败时为转义原文）</p>`,
		ContentInner:     yamlDetailHTML(p.Content),
		ExtraFormatCSS:   detailFormatTreeCSS(),
		ContentAreaClass: " content-formatted",
		FootScript:       scriptSetWindowRawFromEmbed,
	}
}

// renderDetailShell 渲染 Shell 脚本格式：
// 服务端将正文转义后包裹在 <pre><code class="language-bash"> 中；
// 前端按需从 CDN 加载 Prism core + bash grammar 做语法高亮（MIT 许可）。
// 无论 Prism 是否加载成功，正文均以代码块形式展示；脚本不会被执行。
func renderDetailShell(p *store.Paste) detailBodyFragment {
	return detailBodyFragment{
		MetaNote:         `<p class="meta">（Shell：仅语法高亮，不会在浏览器或服务端执行）</p>`,
		ContentInner:     `<pre><code class="language-bash">` + escapeHTML(p.Content) + `</code></pre>`,
		ExtraFormatCSS:   detailFormatTreeCSS(),
		ContentAreaClass: " content-formatted content-sh",
		ExtraHead:        `  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/prismjs@1.29.0/themes/prism.min.css">` + "\n",
		FootScript: `
  <script>
    (function(){
      window.rawContent = document.getElementById('content-raw') ? JSON.parse(document.getElementById('content-raw').textContent) : '';
      var el = document.getElementById('content-area');
      function hl(){
        var code = el ? el.querySelector('code.language-bash') : null;
        if (code && window.Prism) { Prism.highlightElement(code); }
      }
      var urls = [
        'https://cdn.jsdelivr.net/npm/prismjs@1.29.0/components/prism-core.min.js',
        'https://cdn.jsdelivr.net/npm/prismjs@1.29.0/components/prism-bash.min.js'
      ];
      var i = 0;
      function next(){
        if (i >= urls.length) { hl(); return; }
        var s = document.createElement('script');
        s.src = urls[i++];
        s.onload = next;
        s.onerror = function(){};
        document.head.appendChild(s);
      }
      next();
    })();
  </script>`,
	}
}

// renderDetailURL 渲染 URL 链接格式：
// 服务端通过 contentToHTML 将正文中 http(s):// 文本转为 <a> 标签，其余内容转义展示。
// 适合粘贴纯 URL 列表或含链接的普通文本。
func renderDetailURL(p *store.Paste) detailBodyFragment {
	return detailBodyFragment{
		ContentInner: contentToHTML(p.Content),
		FootScript:   scriptSetWindowRawFromEmbed,
	}
}

// renderDetailPlain 是默认纯文本格式（detailFormatRegistry 末尾兜底项）。
// 通过 FootScript 将 #content-raw 的内容以 pre-wrap 方式展示，无任何额外处理。
func renderDetailPlain(_ *store.Paste) detailBodyFragment {
	return detailBodyFragment{
		FootScript: `
  <script>
    (function(){
      var e = document.getElementById('content-raw');
      var rawContent = e ? JSON.parse(e.textContent) : '';
      var el = document.getElementById('content-area');
      el.textContent = rawContent;
      el.style.whiteSpace = 'pre-wrap';
      window.rawContent = rawContent;
    })();
  </script>`,
	}
}

// detailBodyForSafeHidden 为含 safe 标签的记录生成「正文已隐藏」片段。
// 此函数在 detailPage 中先于注册表调用，正文区显示占位说明而非实际内容，
// window.rawContent 置为空串以防止复制功能泄露内容。
func detailBodyForSafeHidden() detailBodyFragment {
	return detailBodyFragment{
		MetaNote:     `<p class="meta">（含 safe 标签：浏览器与远程 API 不展示正文；可在运行 chore_svr 的机器上使用本地 <code>chore_svr -name … -i id</code> 查看）</p>`,
		ContentInner: `<span class="content-hidden-placeholder">（已隐藏）</span>`,
		FootScript: `
  <script>
    (function(){
      window.rawContent = '';
      var el = document.getElementById('content-area');
      if (el) { el.style.whiteSpace = 'normal'; }
    })();
  </script>`,
	}
}

// selectDetailBodyByTags 按 detailFormatRegistry 的顺序扫描，
// 返回第一个 match(p.Tags)==true 的条目调用 render(p) 的结果。
// 若注册表末尾的默认项被意外移除，会以 panic 暴露问题而非静默返回空页面。
func selectDetailBodyByTags(p *store.Paste) detailBodyFragment {
	for i := range detailFormatRegistry {
		e := &detailFormatRegistry[i]
		if e.match(p.Tags) {
			return e.render(p)
		}
	}
	panic("chore: detailFormatRegistry must end with a default entry (match always true)")
}
