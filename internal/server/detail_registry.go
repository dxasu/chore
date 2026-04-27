package server

import "chore/internal/store"

// detailBodyFragment 为详情页「正文区」在 safe 之外的展示片段（由注册表驱动生成）。
type detailBodyFragment struct {
	MetaNote         string
	ContentInner     string
	FootScript       string
	ExtraHead        string
	ExtraFormatCSS   string
	ContentAreaClass string
}

const scriptSetWindowRawFromEmbed = `
  <script>
    (function(){ window.rawContent = document.getElementById('content-raw') ? JSON.parse(document.getElementById('content-raw').textContent) : ''; })();
  </script>`

// detailFormatRegistry 顺序即优先级：靠前先匹配（与原先 else-if 一致）。
var detailFormatRegistry = []struct {
	match  func(tags string) bool
	render func(p *store.Paste) detailBodyFragment
}{
	{
		match: looksLikeMarkdown,
		render: func(p *store.Paste) detailBodyFragment {
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
		},
	},
	{
		match: func(tags string) bool { return pasteHasTag(tags, specialTagJSON) },
		render: func(p *store.Paste) detailBodyFragment {
			return detailBodyFragment{
				MetaNote:         `<p class="meta">（JSON：可折叠树；非法或多段 JSON 时为转义原文）</p>`,
				ContentInner:     jsonDetailHTML(p.Content),
				ExtraFormatCSS:   detailFormatTreeCSS(),
				ContentAreaClass: " content-formatted",
				FootScript:       scriptSetWindowRawFromEmbed,
			}
		},
	},
	{
		match: func(tags string) bool { return pasteHasTag(tags, specialTagYAML) },
		render: func(p *store.Paste) detailBodyFragment {
			return detailBodyFragment{
				MetaNote:         `<p class="meta">（YAML：可折叠树；解析失败时为转义原文）</p>`,
				ContentInner:     yamlDetailHTML(p.Content),
				ExtraFormatCSS:   detailFormatTreeCSS(),
				ContentAreaClass: " content-formatted",
				FootScript:       scriptSetWindowRawFromEmbed,
			}
		},
	},
	{
		match: func(tags string) bool { return pasteHasTag(tags, specialTagSH) },
		render: func(p *store.Paste) detailBodyFragment {
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
		},
	},
	{
		match: func(tags string) bool { return pasteHasTag(tags, specialTagURL) },
		render: func(p *store.Paste) detailBodyFragment {
			return detailBodyFragment{
				ContentInner: contentToHTML(p.Content),
				FootScript:   scriptSetWindowRawFromEmbed,
			}
		},
	},
	{
		match: func(tags string) bool { return true },
		render: func(p *store.Paste) detailBodyFragment {
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
		},
	},
}

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

func selectDetailBodyByTags(p *store.Paste) detailBodyFragment {
	for i := range detailFormatRegistry {
		e := &detailFormatRegistry[i]
		if e.match(p.Tags) {
			return e.render(p)
		}
	}
	return detailBodyFragment{}
}
