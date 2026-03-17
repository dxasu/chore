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

// Override at build time with -ldflags "-X main.defaultAddr=..."
var defaultAddr = ":2026"

// parseIDs parses -id: single id, range 2-10, space-separated 2 5 8, or mixed 2 5-8 10. Returns deduplicated ids.
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

func main() {
	addr := flag.String("addr", defaultAddr, "listen address (server mode only)")
	dbDir := flag.String("dbDir", "./", "directory for sqlite DBs, one per client name (e.g. abc.db)")
	name := flag.String("n", "", "required for local delete/search/update: sqlite DB name (e.g. chore -> chore.db)")
	idArg := flag.String("id", "", "local delete by id(s); or with -title/-tags: update that id (single id only)")
	title := flag.String("title", "", "with -id -n: set title (update mode, single id)")
	tags := flag.String("tags", "", "with -id -n: set tags, comma-separated (update mode, single id)")
	q := flag.String("q", "", "local search by content and print, then exit")
	limit := flag.Int("limit", 0, "with -q: max results; without -q: show last N items (requires -n)")
	getContent := flag.Bool("get", false, "with -n -id: print content of the given id only, then exit")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `chore_svr - receive paste from chore, store per-client sqlite; or use as local delete/search/list/update tool

Usage:
  Start server (default)   chore_svr [-addr :2026] [-dbDir ./]
  Get content by id        chore_svr -n <dbname> -id <id> -get
  Local delete             chore_svr -n <dbname> -id <id or range or list>
  Local update             chore_svr -n <dbname> -id <single-id> [-title "x"] [-tags "a,b"]
  Local search & print     chore_svr -n <dbname> -q "keyword" [-limit 20]
  Last N items             chore_svr -n <dbname> -limit N (no -q; preview truncated)

  -id for delete: -id 3  |  -id 2-10  |  -id 2 -id 5 -id 8

Options:
`)
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
API when running as server (no -id/-q/-limit):
  POST   /api/paste         upload (Body: JSON {"content":"..."} or plain text)
  PATCH  /api/paste/:id    update title and/or tags (Body: JSON {"title":"...", "tags":["a","b"]})
  GET    /list /list/:name history list (paginated, with search)
  GET    /detail/:name/:id single item detail
  GET    /search/:name?q=... search page (paginated)
`)
	}
	flag.Parse()

	manager := store.NewManager(*dbDir)
	defer manager.Close()

	// -get: 按 id 获取并打印内容
	if *getContent {
		if strings.TrimSpace(*name) == "" {
			log.Fatal("for -get, specify -n (DB name), e.g. chore_svr -n chore -id 1 -get")
		}
		if strings.TrimSpace(*idArg) == "" {
			log.Fatal("for -get, specify -id (single id), e.g. chore_svr -n chore -id 1 -get")
		}
		ids, err := parseIDs(*idArg)
		if err != nil {
			log.Fatalf("parse -id: %v", err)
		}
		if len(ids) != 1 {
			log.Fatal("for -get use a single -id")
		}
		dbName := store.SanitizeClientName(*name)
		st, err := manager.GetStore(dbName)
		if err != nil {
			log.Fatalf("open DB %s: %v", dbName, err)
		}
		p, err := st.Get(context.Background(), ids[0])
		if err != nil {
			log.Fatalf("get id %d: %v", ids[0], err)
		}
		if p == nil {
			log.Fatalf("id %d not found", ids[0])
		}
		fmt.Print(p.Content)
		return
	}

	if strings.TrimSpace(*idArg) != "" {
		if strings.TrimSpace(*name) == "" {
			log.Fatal("specify -n (sqlite DB name), e.g. chore_svr -n chore -id 1")
		}
		ids, err := parseIDs(*idArg)
		if err != nil {
			log.Fatalf("parse -id: %v", err)
		}
		if len(ids) == 0 {
			log.Fatal("-id yielded no valid ids; use single id, range 2-10, or space-separated 2 5 8")
		}
		dbName := store.SanitizeClientName(*name)
		st, err := manager.GetStore(dbName)
		if err != nil {
			log.Fatalf("open DB %s: %v", dbName, err)
		}
		doUpdate := strings.TrimSpace(*title) != "" || strings.TrimSpace(*tags) != ""
		if doUpdate {
			if len(ids) != 1 {
				log.Fatal("for update (-title/-tags) use a single -id")
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
			fmt.Printf("ok, updated id %d\n", ids[0])
			return
		}
		var deleted int64
		for _, id := range ids {
			n, err := st.Delete(context.Background(), id)
			if err != nil {
				log.Fatalf("delete id %d: %v", id, err)
			}
			deleted += n
		}
		fmt.Printf("ok, deleted %d\n", deleted)
		return
	}
	const previewRunes = 100
	if *q != "" {
		if strings.TrimSpace(*name) == "" {
			log.Fatal("for search, specify -n (DB name), e.g. chore_svr -n chore -q \"keyword\"")
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
			log.Fatalf("open DB %s: %v", dbName, err)
		}
		list, total, err := st.Search(context.Background(), *q, 0, searchLimit)
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
		if strings.TrimSpace(*name) == "" {
			log.Fatal("for last N items, specify -n, e.g. chore_svr -n chore -limit 10")
		}
		dbName := store.SanitizeClientName(*name)
		st, err := manager.GetStore(dbName)
		if err != nil {
			log.Fatalf("open DB %s: %v", dbName, err)
		}
		list, total, err := st.List(context.Background(), 0, *limit)
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
	h := server.New(manager)
	http.HandleFunc("/api/paste", logRequest(h.HandlePaste))
	http.HandleFunc("/api/paste/", logRequest(h.HandlePatchPaste))
	http.HandleFunc("/search", logRequest(h.HandleSearchPage))
	http.HandleFunc("/search/", logRequest(h.HandleSearchPage))
	http.HandleFunc("/list", logRequest(h.HandleList))
	http.HandleFunc("/list/", logRequest(h.HandleList))
	http.HandleFunc("/detail/", logRequest(h.HandleDetail))

	log.Printf("chore_svr listening on %s", *addr)
	if err := http.ListenAndServe(*addr, nil); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
