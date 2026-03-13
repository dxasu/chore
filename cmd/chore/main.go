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

func main() {
	serverURL := flag.String("server", defaultServerURL, "chore_svr server URL")
	verbose := flag.Bool("v", false, "on success print detail and list URLs")
	openList := flag.Bool("o", false, "do not send; open browser to list page only")
	title := flag.String("title", "", "optional title for the paste")
	tags := flag.String("tags", "", "optional comma-separated tags (max 6)")
	flag.Usage = func() {
		name := clientNameFromExec()
		fmt.Fprintf(os.Stderr, "%s - send clipboard to chore_svr, one DB per executable name (e.g. abc -> abc.db)\n\nUsage:\n  %s [options]\n\nOptions:\n", name, name)
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n  %s          read clipboard and upload, print ok on success\n  %s -v       print detail URL and list URL\n  %s -o       open browser to list page\n  %s -title \"Note\" -tags a,b,c  upload with optional title and tags\n  %s -server http://host:9000  use custom server\n", name, name, name, name, name)
	}
	flag.Parse()

	clientName := clientNameFromExec()
	baseURL := strings.TrimSuffix(*serverURL, "/")
	listURL := baseURL + "/list/" + clientName
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
		fmt.Println("ok")
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
