package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/atotto/clipboard"
)

// Override at build time with -ldflags "-X main.defaultServerURL=..."
var defaultServerURL = "http://localhost:2026"

// expandShortArgs expands combined short flags.
// Supports:
// - bool-only cluster: -voc -> -v -o -c
// - one value flag mixed with bool flags: -icv 1 -> -c -v -i 1
func expandShortArgs(args []string, boolFlags, valueFlags map[rune]bool) []string {
	if len(args) <= 1 {
		return args
	}
	out := make([]string, 0, len(args))
	out = append(out, args[0])
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			out = append(out, args[i:]...)
			break
		}
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && len(arg) > 2 && !strings.Contains(arg, "=") {
			cluster := []rune(arg[1:])
			valueCount := 0
			valueRune := rune(0)
			onlyKnown := true
			for _, ch := range cluster {
				if valueFlags[ch] {
					valueCount++
					valueRune = ch
					continue
				}
				if !boolFlags[ch] {
					onlyKnown = false
					break
				}
			}
			if !onlyKnown || valueCount > 1 {
				out = append(out, arg)
				continue
			}
			if valueCount == 0 {
				for _, ch := range cluster {
					out = append(out, "-"+string(ch))
				}
				continue
			}
			for _, ch := range cluster {
				if ch == valueRune {
					continue
				}
				out = append(out, "-"+string(ch))
			}
			out = append(out, "-"+string(valueRune))
			continue
		}
		out = append(out, arg)
	}
	return out
}

func main() {
	serverURL := flag.String("s", defaultServerURL, "chore_svr server URL")
	verbose := flag.Bool("v", false, "on success print detail and list URLs")
	openList := flag.Bool("o", false, "do not send; open browser to list page only")
	getID := flag.String("i", "", "paste id to fetch and print")
	cp := flag.Bool("c", false, "with -i: copy content to clipboard instead of stdout")
	title := flag.String("title", "", "optional title for the paste")
	tags := flag.String("tags", "", "optional comma-separated tags (max 10)")
	flag.Usage = func() {
		name := clientNameFromExec()
		fmt.Fprintf(os.Stderr, "%s - send clipboard to chore_svr, one DB per executable name (e.g. abc -> abc.db)\n\nUsage:\n  %s [options]\n\nOptions:\n", name, name)
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n  %s                 read clipboard and upload\n  %s -v              print detail URL and list URL\n  %s -o              open browser to list page\n  %s -i 5            get and print content of paste #5\n  %s -i 5 -c         get content of paste #5 and copy it\n  %s -vc             combined short bool flags (equivalent to -v -c)\n  %s -icv 5          mixed short flags (equivalent to -c -v -i 5)\n  %s -title \"Note\" -tags a,b,c  upload with optional title and tags\n  %s -s http://host:9000         use custom server\n", name, name, name, name, name, name, name, name, name)
	}
	expandedArgs := expandShortArgs(os.Args, map[rune]bool{
		'v': true,
		'o': true,
		'c': true,
	}, map[rune]bool{
		'i': true,
		's': true,
	})
	if err := flag.CommandLine.Parse(expandedArgs[1:]); err != nil {
		fail("parse flags: %v", err)
	}

	clientName := clientNameFromExec()
	baseURL := strings.TrimSuffix(*serverURL, "/")
	listURL := baseURL + "/list/" + clientName

	// -i: 从服务器按 id 获取并打印内容（或 -c 复制到剪贴板）
	if strings.TrimSpace(*getID) != "" {
		idStr := strings.TrimSpace(*getID)
		detailURL := baseURL + "/detail/" + clientName + "/" + idStr
		req, err := http.NewRequest(http.MethodGet, detailURL, nil)
		if err != nil {
			fail("build request: %v", err)
		}
		req.Header.Set("Accept", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fail("request: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			fail("server returned %d: %s", resp.StatusCode, string(b))
		}
		var p struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
			fail("decode response: %v", err)
		}
		if *cp {
			if err := clipboard.WriteAll(p.Content); err != nil {
				fail("copy to clipboard: %v", err)
			}
			return
		}
		fmt.Print(p.Content)
		return
	}

	if *openList {
		if err := openBrowser(listURL); err != nil {
			fail("open browser: %v", err)
		}
		return
	}

	content, err := clipboard.ReadAll()
	if err != nil {
		fail("read clipboard: %v", err)
	}

	content = trimSpace(content)
	if content == "" {
		fail("clipboard is empty")
	}

	payload := struct {
		Content string   `json:"content"`
		Title   string   `json:"title,omitempty"`
		Tags    []string `json:"tags,omitempty"`
	}{
		Content: content,
		Title:   strings.TrimSpace(*title),
	}
	if *tags != "" {
		for _, t := range strings.Split(*tags, ",") {
			if s := strings.TrimSpace(t); s != "" {
				payload.Tags = append(payload.Tags, s)
			}
		}
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/paste", bytes.NewReader(body))
	if err != nil {
		fail("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-Name", clientName)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fail("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		fail("server returned %d: %s", resp.StatusCode, string(b))
	}

	var out struct {
		ID        int64  `json:"id"`
		CreatedAt string `json:"created_at"`
		DetailURL string `json:"detail_url"`
		ListURL   string `json:"list_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		fail("decode response: %v", err)
	}

	if !*verbose {
		// fmt.Println(out.ID)
		return
	}
	fmt.Printf("saved #%d %s\n", out.ID, out.CreatedAt)
	fmt.Printf("detail: %s%s\n", *serverURL, out.DetailURL)
	if out.ListURL != "" {
		fmt.Printf("list: %s%s\n", *serverURL, out.ListURL)
	}
}

// fail prints to stderr and exits
func fail(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

// clientNameFromExec returns the executable base name (no path, no .exe), used as server DB name (e.g. abc -> abc.db)
func clientNameFromExec() string {
	name := os.Args[0]
	if name != "" {
		name = filepath.Base(name)
	}
	if name == "" {
		name = "chore"
	}
	name = strings.TrimSuffix(name, ".exe")
	if name == "" {
		name = "chore"
	}
	return name
}

func trimSpace(s string) string {
	runes := []rune(s)
	start, end := 0, len(runes)
	for start < end && (runes[start] == ' ' || runes[start] == '\t' || runes[start] == '\n' || runes[start] == '\r') {
		start++
	}
	for end > start && (runes[end-1] == ' ' || runes[end-1] == '\t' || runes[end-1] == '\n' || runes[end-1] == '\r') {
		end--
	}
	return string(runes[start:end])
}
