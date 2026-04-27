package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const formatTreeMaxDepth = 64

// jsonDetailHTML 合法 JSON 则渲染可折叠 + 简易语法色；否则返回转义后的原文 pre。
func jsonDetailHTML(content string) string {
	raw := strings.TrimSpace(content)
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return `<pre class="format-fallback"><code>` + escapeHTML(content) + `</code></pre>`
	}
	if dec.More() {
		return `<pre class="format-fallback"><code>` + escapeHTML(content) + `</code></pre>`
	}
	return `<div class="format-tree format-json">` + renderValueFoldable(v, 0) + `</div>`
}

// yamlDetailHTML 合法 YAML 则解析为树并折叠展示；失败则转义原文。
func yamlDetailHTML(content string) string {
	var v any
	if err := yaml.Unmarshal([]byte(content), &v); err != nil {
		return `<pre class="format-fallback"><code>` + escapeHTML(content) + `</code></pre>`
	}
	v = normalizeYAMLRoot(v)
	return `<div class="format-tree format-yaml">` + renderValueFoldable(v, 0) + `</div>`
}

// normalizeYAMLRoot 将 map[any]any 等转为 string key 的 map，便于统一渲染。
func normalizeYAMLRoot(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = normalizeYAMLValue(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprint(k)] = normalizeYAMLValue(val)
		}
		return out
	default:
		return normalizeYAMLValue(v)
	}
}

func normalizeYAMLValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = normalizeYAMLValue(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprint(k)] = normalizeYAMLValue(val)
		}
		return out
	case []any:
		for i := range t {
			t[i] = normalizeYAMLValue(t[i])
		}
		return t
	case []byte:
		return string(t)
	default:
		return v
	}
}

func renderValueFoldable(v any, depth int) string {
	if depth > formatTreeMaxDepth {
		return `<span class="j-trunc">…</span>`
	}
	switch t := v.(type) {
	case nil:
		return `<span class="j-null">null</span>`
	case bool:
		return `<span class="j-bool">` + strconv.FormatBool(t) + `</span>`
	case json.Number:
		return `<span class="j-num">` + escapeHTML(t.String()) + `</span>`
	case float64:
		return `<span class="j-num">` + formatFloat(t) + `</span>`
	case int:
		return `<span class="j-num">` + strconv.Itoa(t) + `</span>`
	case int64:
		return `<span class="j-num">` + strconv.FormatInt(t, 10) + `</span>`
	case int32:
		return `<span class="j-num">` + strconv.FormatInt(int64(t), 10) + `</span>`
	case uint32:
		return `<span class="j-num">` + strconv.FormatUint(uint64(t), 10) + `</span>`
	case uint64:
		return `<span class="j-num">` + strconv.FormatUint(t, 10) + `</span>`
	case float32:
		return `<span class="j-num">` + formatFloat(float64(t)) + `</span>`
	case string:
		return `<span class="j-str">"` + escapeHTML(t) + `"</span>`
	case []any:
		if len(t) == 0 {
			return `<span class="j-bracket">[]</span>`
		}
		var b strings.Builder
		b.WriteString(`<div class="j-array">`)
		for i, el := range t {
			b.WriteString(`<details class="json-fold"` + foldOpenAttr(i < 3) + `>`)
			b.WriteString(`<summary><span class="j-idx">[` + strconv.Itoa(i) + `]</span></summary>`)
			b.WriteString(`<div class="json-nested">`)
			b.WriteString(renderValueFoldable(el, depth+1))
			b.WriteString(`</div></details>`)
		}
		b.WriteString(`</div>`)
		return b.String()
	case map[string]any:
		if len(t) == 0 {
			return `<span class="j-bracket">{}</span>`
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteString(`<div class="j-obj">`)
		for i, k := range keys {
			b.WriteString(`<details class="json-fold"` + foldOpenAttr(i < 5) + `>`)
			b.WriteString(`<summary><span class="j-key">` + escapeHTML(k) + `</span></summary>`)
			b.WriteString(`<div class="json-nested">`)
			b.WriteString(renderValueFoldable(t[k], depth+1))
			b.WriteString(`</div></details>`)
		}
		b.WriteString(`</div>`)
		return b.String()
	default:
		// yaml 时间等非常见类型
		return `<span class="j-str">` + escapeHTML(fmt.Sprint(t)) + `</span>`
	}
}

func foldOpenAttr(open bool) string {
	if open {
		return ` open`
	}
	return ""
}

func formatFloat(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return escapeHTML(fmt.Sprint(f))
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	return escapeHTML(s)
}

// detailFormatTreeCSS 为 JSON/YAML 树与 Shell 代码块补充样式（不含 Prism 主题，主题仅 sh 分支单独引入）。
func detailFormatTreeCSS() string {
	return `
    .content-formatted { white-space: normal !important; }
    .format-tree { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 0.86rem; line-height: 1.45; }
    .format-fallback { margin: 0; padding: 0.75rem; overflow: auto; max-height: 70vh; background: #0f172a; color: #e2e8f0; border-radius: 8px; font-size: 0.85rem; white-space: pre-wrap; word-break: break-all; }
    .format-fallback code { font-family: inherit; }
    .json-fold { border: 1px solid var(--border); border-radius: 8px; margin: 0.35rem 0; padding: 0.2rem 0.45rem; background: #fff; }
    .json-fold > summary { cursor: pointer; font-weight: 600; color: #334155; }
    .json-fold > summary::-webkit-details-marker { color: #94a3b8; }
    .json-nested { margin: 0.35rem 0 0.15rem 0.6rem; padding-left: 0.35rem; border-left: 2px solid #e2e8f0; }
    .j-key { color: #0369a1; }
    .j-str { color: #15803d; }
    .j-num { color: #b45309; }
    .j-bool { color: #7c3aed; }
    .j-null { color: #94a3b8; font-style: italic; }
    .j-idx { color: #64748b; font-weight: normal; }
    .j-bracket { color: #64748b; }
    .j-trunc { color: var(--muted); }
    .content-sh pre { margin: 0; padding: 0; background: transparent; max-height: 70vh; overflow: auto; }
    .content-sh code.language-bash { display: block; padding: 0.85rem; background: #0f172a; color: #e2e8f0; border-radius: 8px; font-size: 0.84rem; line-height: 1.5; }
`
}
