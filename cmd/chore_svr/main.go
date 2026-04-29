package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"chore/internal/server"
	"chore/internal/store"
	"github.com/atotto/clipboard"
)

// Override at build time with -ldflags "-X main.defaultAddr=..."
var defaultAddr = ":2026"

// 编译时通过 -ldflags 注入
var (
	buildTime = "unknown"
	commitID  = "unknown"
	gitTag    = ""
)

type localConfig struct {
	Name string `json:"name"`
}

// parseIDs parses -i such as "3", "2-10", or "1-4,7,10". Returns deduplicated ids.
func parseIDs(s string) ([]int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	seen := make(map[int64]bool)
	var list []int64
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			rangeParts := strings.SplitN(part, "-", 2)
			if len(rangeParts) != 2 || strings.TrimSpace(rangeParts[0]) == "" || strings.TrimSpace(rangeParts[1]) == "" {
				return nil, fmt.Errorf("invalid range %q, use start-end", part)
			}
			start, err1 := strconv.ParseInt(strings.TrimSpace(rangeParts[0]), 10, 64)
			end, err2 := strconv.ParseInt(strings.TrimSpace(rangeParts[1]), 10, 64)
			if err1 != nil || err2 != nil || start < 1 || end < 1 || start > end {
				return nil, fmt.Errorf("invalid range %q, use start-end with start<=end", part)
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
				return nil, fmt.Errorf("invalid id %q", part)
			}
			if !seen[id] {
				seen[id] = true
				list = append(list, id)
			}
		}
	}
	return list, nil
}

func loadConfig(path string) (localConfig, error) {
	var cfg localConfig
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

func serviceNameFromExec() string {
	name := filepath.Base(os.Args[0])
	name = strings.TrimSuffix(name, ".exe")
	name = strings.TrimSpace(name)
	if name == "" {
		return "chore"
	}
	if idx := strings.Index(name, "_"); idx > 0 {
		name = strings.TrimSpace(name[:idx])
	}
	if name == "" {
		return "chore"
	}
	return name
}

func resolveName(cliName string, cfg localConfig) string {
	if v := strings.TrimSpace(cliName); v != "" {
		return v
	}
	if v := strings.TrimSpace(cfg.Name); v != "" {
		return v
	}
	return serviceNameFromExec()
}

func printVersion() {
	if gitTag != "" {
		fmt.Printf("tag:    %s\n", gitTag)
	}
	fmt.Printf("commit: %s\n", commitID)
	fmt.Printf("built:  %s\n", buildTime)
}

func main() {
	addr := flag.String("addr", defaultAddr, "listen address (server mode only)")
	dbDir := flag.String("dbDir", "./", "directory for sqlite DBs, one per client name (e.g. abc.db)")
	name := flag.String("name", "", "DB name (optional). Fallback order: -name > chore.json:name > service name")
	idArg := flag.String("i", "", "get/update/delete by id; delete requires -delete (supports 1-4,7,10)")
	deleteMode := flag.Bool("delete", false, "with -i: delete records by id list/range")
	cp := flag.Bool("c", false, "with -i get mode: copy content to clipboard instead of stdout")
	title := flag.String("title", "", "with -i: set title (update mode, single id)")
	tags := flag.String("tags", "", "with -i: set tags, comma-separated (update mode, single id)")
	q := flag.String("q", "", "local search by content and print, then exit")
	limit := flag.Int("limit", 0, "with -q: max results; without -q: show last N items")
	version := flag.Bool("version", false, "print build info and exit")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `chore_svr - receive paste from chore, store per-client sqlite; or use as local delete/search/list/update tool

Usage:
  Start server (default)   chore_svr [-addr :2026] [-dbDir ./]
  Get content by id        chore_svr [-name <dbname>] -i <id> [-c]
  Local delete             chore_svr [-name <dbname>] -i <1-4,7,10> -delete
  Local update             chore_svr [-name <dbname>] -i <single-id> [-title "x"] [-tags "a,b"]
  Local search & print     chore_svr [-name <dbname>] -q "keyword" [-limit 20]
  Last N items             chore_svr [-name <dbname>] -limit N (no -q; preview truncated)

  -i examples: -i 3 | -i 2-10 | -i 1-4,7,10
  Name fallback: -name > chore.json("name") > executable name

Options:
`)
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
API when running as server (no -i/-q/-limit):
  POST   /api/paste         upload (Body: JSON {"content":"..."} or plain text)
  PATCH  /api/paste/:id    update title and/or tags (Body: JSON {"title":"...", "tags":["a","b"]})
  GET    /list /list/:name history list + search (q=...; paginated)
  GET    /detail/:name/:id single item detail
  (tags 含 "hide" 的记录仅 HTTP 列表/搜索/详情不返回；本工具 -i/-q/-limit 直连库仍可见)
`)
	}
	flag.Parse()

	if *version {
		printVersion()
		return
	}

	cfg, err := loadConfig("chore.json")
	if err != nil {
		log.Fatalf("load config chore.json: %v", err)
	}
	resolvedName := store.SanitizeClientName(resolveName(*name, cfg))
	idValue := strings.TrimSpace(*idArg)
	cpValue := *cp

	manager := store.NewManager(*dbDir)
	defer manager.Close()

	if idValue != "" {
		ids, err := parseIDs(idValue)
		if err != nil {
			log.Fatalf("parse -i: %v", err)
		}
		if len(ids) == 0 {
			log.Fatal("-i yielded no valid ids; use single id, range 2-10, or mixed 1-4,7,10")
		}
		st, err := manager.GetStore(resolvedName)
		if err != nil {
			log.Fatalf("open DB %s: %v", resolvedName, err)
		}
		doUpdate := strings.TrimSpace(*title) != "" || strings.TrimSpace(*tags) != ""
		if doUpdate {
			if len(ids) != 1 {
				log.Fatal("for update (-title/-tags) use a single -i")
			}
			var titlePtr *string
			var tagsPtr *[]string
			if t := strings.TrimSpace(*title); t != "" {
				titlePtr = &t
			}
			if t := strings.TrimSpace(*tags); t != "" {
				list := strings.Split(t, ",")
				for i := range list {
					list[i] = strings.TrimSpace(list[i])
				}
				tagsPtr = &list
			}
			if titlePtr == nil && tagsPtr == nil {
				log.Fatal("for update specify -title and/or -tags")
			}
			updated, err := st.Update(context.Background(), ids[0], titlePtr, tagsPtr)
			if err != nil {
				log.Fatalf("update id %d: %v", ids[0], err)
			}
			if !updated {
				log.Fatalf("id %d not found", ids[0])
			}
			fmt.Printf("updated id %d\n", ids[0])
			return
		}
		if *deleteMode {
			var deleted int64
			for _, id := range ids {
				n, err := st.Delete(context.Background(), id)
				if err != nil {
					log.Fatalf("delete id %d: %v", id, err)
				}
				deleted += n
			}
			fmt.Printf("deleted %d\n", deleted)
			return
		}
		if len(ids) != 1 {
			log.Fatal("get mode requires a single -i; for multiple ids, use -delete")
		}
		p, err := st.Get(context.Background(), ids[0])
		if err != nil {
			log.Fatalf("get id %d: %v", ids[0], err)
		}
		if p == nil {
			log.Fatalf("id %d not found", ids[0])
		}
		if cpValue {
			if err := clipboard.WriteAll(p.Content); err != nil {
				log.Fatalf("copy to clipboard: %v", err)
			}
			return
		}
		fmt.Print(p.Content)
		return

	}
	if *deleteMode {
		log.Fatal("-delete requires -i")
	}
	const previewRunes = 100
	if *q != "" {
		searchLimit := *limit
		if searchLimit <= 0 {
			searchLimit = 20
		}
		if searchLimit > 20 {
			searchLimit = 20
		}
		st, err := manager.GetStore(resolvedName)
		if err != nil {
			log.Fatalf("open DB %s: %v", resolvedName, err)
		}
		list, total, err := st.Search(context.Background(), *q, 0, searchLimit, false)
		if err != nil {
			log.Fatalf("search: %v", err)
		}
		for _, p := range list {
			preview := p.Content
			if len([]rune(preview)) > previewRunes {
				preview = string([]rune(preview)[:previewRunes]) + "..."
			}
			fmt.Printf("#%d %s\n%s\n\n", p.ID, p.CreatedAt.Format("2006-01-02 15:04:05"), preview)
		}
		fmt.Fprintf(os.Stderr, "total %d (showing up to %d)\n", total, len(list))
		return
	}
	if *limit > 0 {
		st, err := manager.GetStore(resolvedName)
		if err != nil {
			log.Fatalf("open DB %s: %v", resolvedName, err)
		}
		list, total, err := st.List(context.Background(), 0, *limit, false)
		if err != nil {
			log.Fatalf("list: %v", err)
		}
		for _, p := range list {
			preview := p.Content
			if len([]rune(preview)) > previewRunes {
				preview = string([]rune(preview)[:previewRunes]) + "..."
			}
			fmt.Printf("#%d %s\n%s\n\n", p.ID, p.CreatedAt.Format("2006-01-02 15:04:05"), preview)
		}
		fmt.Fprintf(os.Stderr, "total %d (showing last %d)\n", total, len(list))
		return
	}

	logRequest := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			log.Printf("[%d] %s %s %s", r.ContentLength, r.URL.Path, r.RemoteAddr, r.UserAgent())
			next(w, r)
		}
	}
	h := server.New(manager, *dbDir)
	http.HandleFunc("/api/paste", logRequest(h.HandlePaste))
	http.HandleFunc("/api/paste/", logRequest(h.HandlePatchPaste))
	http.HandleFunc("/api/image", logRequest(h.HandleImage))
	http.HandleFunc("/img/", h.HandleServeImage)
	http.HandleFunc("/list", logRequest(h.HandleList))
	http.HandleFunc("/list/", logRequest(h.HandleList))
	http.HandleFunc("/detail/", logRequest(h.HandleDetail))

	log.Printf("chore_svr listening on %s", *addr)
	if err := http.ListenAndServe(*addr, nil); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
