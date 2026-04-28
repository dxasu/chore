// Package server 的 format_tree.go 文件实现结构化数据（JSON/YAML）的服务端树渲染，
// 以及详情页 JSON/YAML/Shell 格式共用的附加 CSS 样式。
//
// 渲染结果是纯 HTML 字符串，由 detailFormatRegistry 中的 renderDetailJSON/renderDetailYAML 调用后
// 直接嵌入详情页 #content-area，无需前端解析库即可展示可折叠结构树。
//
// 树渲染规则：
//   - map 键按字母升序排列，前 5 项默认展开；
//   - 数组元素按下标，前 3 项默认展开；
//   - 嵌套深度超过 formatTreeMaxDepth 时截断为「…」，防止极深结构卡住渲染；
//   - 基础类型（string/number/bool/null）直接以对应 CSS class 的 <span> 展示。
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

// formatTreeMaxDepth 是树渲染的最大递归深度，超出时显示省略号，避免极深 JSON/YAML 卡住页面渲染。
const formatTreeMaxDepth = 64

// jsonDetailHTML 将 content 作为 JSON 解析并渲染为可折叠树 HTML。
// 使用 json.Decoder.UseNumber 保留原始数字字符串，避免大整数精度丢失。
// 若解析失败或输入含多段 JSON（dec.More() 为真），退回 <pre> 转义原文。
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

// yamlDetailHTML 将 content 作为 YAML 解析并渲染为可折叠树 HTML。
// 解析后经 normalizeYAMLRoot 统一 key 类型，再复用与 JSON 相同的 renderValueFoldable。
// 解析失败时退回 <pre> 转义原文。
func yamlDetailHTML(content string) string {
	var v any
	if err := yaml.Unmarshal([]byte(content), &v); err != nil {
		return `<pre class="format-fallback"><code>` + escapeHTML(content) + `</code></pre>`
	}
	v = normalizeYAMLRoot(v)
	return `<div class="format-tree format-yaml">` + renderValueFoldable(v, 0) + `</div>`
}

// normalizeYAMLRoot 是 yaml.Unmarshal 结果的顶层归一化入口。
// yaml.v3 对映射产生 map[string]any，但嵌套映射可能产生 map[any]any，
// 此函数递归转换为统一的 map[string]any，保证 renderValueFoldable 的类型 switch 覆盖所有情况。
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

// normalizeYAMLValue 递归处理 YAML 解析树中的每个节点：
//   - map[string]any / map[any]any → map[string]any（key 统一 fmt.Sprint 转字符串）
//   - []any → 递归处理每个元素
//   - []byte（YAML binary）→ string
//   - 其他类型原样返回（string、int、float64、bool、nil 等）
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

// renderValueFoldable 将任意 Go 值递归渲染为带 <details> 折叠的 HTML 树字符串。
// 被 jsonDetailHTML 与 yamlDetailHTML 共同调用，统一 JSON 和 YAML 的视觉风格。
//
// CSS class 约定（与 detailFormatTreeCSS 对应）：
//   - .j-null .j-bool .j-num .j-str：标量类型配色
//   - .j-key .j-idx：对象键与数组下标
//   - .j-bracket：空集合字面量
//   - .json-fold / .json-nested：可折叠容器与缩进层
//   - .j-trunc：超深截断占位
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

// foldOpenAttr 返回 <details> 标签的 open 属性字符串（含前导空格）或空串。
// 用于控制树节点的默认展开/折叠状态：对象前 5 项、数组前 3 项默认展开。
func foldOpenAttr(open bool) string {
	if open {
		return ` open`
	}
	return ""
}

// formatFloat 将 float64 格式化为简洁字符串（%g 格式），并做 HTML 转义。
// NaN/Inf 等非常规浮点值通过 fmt.Sprint 兜底，避免 strconv 恐慌。
func formatFloat(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return escapeHTML(fmt.Sprint(f))
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	return escapeHTML(s)
}

// detailFormatTreeCSS 返回 JSON/YAML 树与 Shell 代码块所需的附加内联 CSS 字符串。
// 该字符串由 renderDetailJSON/renderDetailYAML/renderDetailShell 通过 detailBodyFragment.ExtraFormatCSS 注入页内 <style>。
// Prism 主题（用于 sh 分支）单独通过 ExtraHead <link> 引入，不包含在此处。
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
