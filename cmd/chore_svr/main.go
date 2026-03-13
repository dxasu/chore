package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"chore/internal/server"
	"chore/internal/store"
)

// 编译时可通过 -ldflags "-X main.defaultAddr=..." 覆盖
var defaultAddr = ":2026"

// parseIDs 解析 -id 参数：支持单 id、范围 2-10、空格分隔 2 5 8、混合 2 5-8 10。返回去重后的 id 列表。
func parseIDs(s string) ([]int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	seen := make(map[int64]bool)
	var list []int64
	for _, part := range strings.Fields(s) {
		if strings.Contains(part, "-") {
			idx := strings.Index(part, "-")
			start, err1 := strconv.ParseInt(strings.TrimSpace(part[:idx]), 10, 64)
			end, err2 := strconv.ParseInt(strings.TrimSpace(part[idx+1:]), 10, 64)
			if err1 != nil || err2 != nil || start < 1 || end < 1 || start > end {
				return nil, fmt.Errorf("无效范围 %q，应为 起始-结束 且起始≤结束", part)
			}
			for i := start; i <= end; i++ {
				if !seen[i] {
					seen[i] = true
					list = append(list, i)
				}
			}
		} else {
			id, err := strconv.ParseInt(part, 10, 64)
			if err != nil || id < 1 {
				return nil, fmt.Errorf("无效 id %q", part)
			}
			if !seen[id] {
				seen[id] = true
				list = append(list, id)
			}
		}
	}
	return list, nil
}

func main() {
	addr := flag.String("addr", defaultAddr, "监听地址（仅服务模式）")
	dbDir := flag.String("dbDir", "./", "sqlite 所在目录，按客户端名分库（如 abc.db）")
	name := flag.String("n", "", "本地删除/搜索时必须指定：sqlite 库名（如 chore 即 chore.db）")
	idArg := flag.String("id", "", "指定时：本地删除指定 id；支持范围 2-10 或空格分隔 2 5 8 或混合 2 5-8 10")
	q := flag.String("q", "", "指定时：本地按内容搜索并打印后退出")
	limit := flag.Int("limit", 0, "与 -q 合用：搜索最多条数；无 -q 时：显示最近 N 条（需配合 -n）")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `chore_svr - 接收 chore 上传内容，按客户端名分库存储；也可作本地删除/搜索/列表工具

用法:
  启动服务（默认）   chore_svr [-addr :2026] [-dbDir ./]
  本地删除          chore_svr -n <sqlite库名> -id <id或范围或列表>
  本地搜索并打印    chore_svr -n <sqlite库名> -q "关键词" [-limit 20]
  最近 N 条         chore_svr -n <sqlite库名> -limit N（无 -q 时，每条显示不超过 20 字）

  -id 示例: -id 3  |  -id 2-10  |  -id 2 -id 5 -id 8

选项:
`)
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
服务模式下的 API（未指定 -id/-q/-limit 时）:
  POST /api/paste           上传内容（Body: JSON {"content":"..."} 或纯文本）
  GET  /list /list/:name    历史列表（分页，页上有搜索框）
  GET  /detail/:name/:id    单条详情
  GET  /search/:name?q=...   搜索页（从列表页搜索框进入，分页）
`)
	}
	flag.Parse()

	manager := store.NewManager(*dbDir)
	defer manager.Close()

	if strings.TrimSpace(*idArg) != "" {
		if strings.TrimSpace(*name) == "" {
			log.Fatal("删除时请指定 -n 指定 sqlite 库名，如 chore_svr -n chore -id 1")
		}
		ids, err := parseIDs(*idArg)
		if err != nil {
			log.Fatalf("-id 解析失败: %v", err)
		}
		if len(ids) == 0 {
			log.Fatal("-id 未解析出有效 id，支持单 id、范围 2-10、空格分隔 2 5 8")
		}
		dbName := store.SanitizeClientName(*name)
		st, err := manager.GetStore(dbName)
		if err != nil {
			log.Fatalf("打开库 %s: %v", dbName, err)
		}
		var deleted int64
		for _, id := range ids {
			n, err := st.Delete(context.Background(), id)
			if err != nil {
				log.Fatalf("删除 id %d 失败: %v", id, err)
			}
			deleted += n
		}
		fmt.Printf("ok，已删除 %d 条\n", deleted)
		return
	}
	const previewRunes = 100 // 每条记录内容预览最多字数
	if *q != "" {
		if strings.TrimSpace(*name) == "" {
			log.Fatal("搜索时请指定 -n 指定 sqlite 库名，如 chore_svr -n chore -q \"关键词\"")
		}
		dbName := store.SanitizeClientName(*name)
		searchLimit := *limit
		if searchLimit <= 0 {
			searchLimit = 5
		}
		if searchLimit > 5 {
			searchLimit = 5
		}
		st, err := manager.GetStore(dbName)
		if err != nil {
			log.Fatalf("打开库 %s: %v", dbName, err)
		}
		list, total, err := st.Search(context.Background(), *q, 0, searchLimit)
		if err != nil {
			log.Fatalf("搜索失败: %v", err)
		}
		for _, p := range list {
			preview := p.Content
			if len([]rune(preview)) > previewRunes {
				preview = string([]rune(preview)[:previewRunes]) + "..."
			}
			fmt.Printf("#%d %s\n%s\n\n", p.ID, p.CreatedAt.Format("2006-01-02 15:04:05"), preview)
		}
		fmt.Fprintf(os.Stderr, "共 %d 条（显示最多 %d 条）\n", total, len(list))
		return
	}
	if *limit > 0 {
		if strings.TrimSpace(*name) == "" {
			log.Fatal("显示最近 N 条时请指定 -n，如 chore_svr -n chore -limit 10")
		}
		dbName := store.SanitizeClientName(*name)
		st, err := manager.GetStore(dbName)
		if err != nil {
			log.Fatalf("打开库 %s: %v", dbName, err)
		}
		list, total, err := st.List(context.Background(), 0, *limit)
		if err != nil {
			log.Fatalf("列表失败: %v", err)
		}
		for _, p := range list {
			preview := p.Content
			if len([]rune(preview)) > previewRunes {
				preview = string([]rune(preview)[:previewRunes]) + "..."
			}
			fmt.Printf("#%d %s\n%s\n\n", p.ID, p.CreatedAt.Format("2006-01-02 15:04:05"), preview)
		}
		fmt.Fprintf(os.Stderr, "共 %d 条（显示最近 %d 条）\n", total, len(list))
		return
	}

	h := server.New(manager)
	http.HandleFunc("/api/paste", h.HandlePaste)
	http.HandleFunc("/search", h.HandleSearchPage)
	http.HandleFunc("/search/", h.HandleSearchPage)
	http.HandleFunc("/list", h.HandleList)
	http.HandleFunc("/list/", h.HandleList)
	http.HandleFunc("/detail/", h.HandleDetail)

	log.Printf("chore_svr listening on %s", *addr)
	if err := http.ListenAndServe(*addr, nil); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
