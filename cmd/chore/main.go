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

// 编译时可通过 -ldflags "-X main.defaultServerURL=..." 覆盖
var defaultServerURL = "http://localhost:2026"

func main() {
	serverURL := flag.String("server", defaultServerURL, "chore_svr 服务地址")
	verbose := flag.Bool("v", false, "成功时输出详情链接与历史链接")
	openList := flag.Bool("o", false, "不发送数据，仅打开浏览器到历史列表页")
	flag.Usage = func() {
		name := clientNameFromExec()
		fmt.Fprintf(os.Stderr, "%s - 将剪贴板内容发送到 chore_svr，按可执行文件名分库（如 abc -> abc.db）\n\n用法:\n  %s [选项]\n\n选项:\n", name, name)
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\n示例:\n  %s          读取剪贴板并上传，成功输出 ok\n  %s -v       上传并输出详情链接、历史链接\n  %s -o       不上传，打开浏览器到历史列表页（页内可搜索）\n  %s -server http://host:9000  指定服务地址\n", name, name, name, name)
	}
	flag.Parse()

	clientName := clientNameFromExec()
	baseURL := strings.TrimSuffix(*serverURL, "/")
	listURL := baseURL + "/list/" + clientName
	if *openList {
		if err := openBrowser(listURL); err != nil {
			fail("打开浏览器失败: %v", err)
		}
		fmt.Println("ok")
		return
	}

	content, err := clipboard.ReadAll()
	if err != nil {
		fail("读取剪贴板失败: %v", err)
	}

	content = trimSpace(content)
	if content == "" {
		fail("剪贴板为空")
	}

	body, _ := json.Marshal(map[string]string{"content": content})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/paste", bytes.NewReader(body))
	if err != nil {
		fail("请求构造失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-Name", clientName)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fail("请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		fail("服务器返回 %d: %s", resp.StatusCode, string(b))
	}

	var out struct {
		ID        int64  `json:"id"`
		CreatedAt string `json:"created_at"`
		DetailURL string `json:"detail_url"`
		ListURL   string `json:"list_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		fail("解析响应失败: %v", err)
	}

	if !*verbose {
		fmt.Println("ok")
		return
	}
	fmt.Printf("已保存 #%d %s\n", out.ID, out.CreatedAt)
	fmt.Printf("详情: %s%s\n", *serverURL, out.DetailURL)
	if out.ListURL != "" {
		fmt.Printf("历史: %s%s\n", *serverURL, out.ListURL)
	}
}

// fail 向 stderr 输出失败原因并退出，默认模式下仅此一行
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

// clientNameFromExec 返回当前可执行文件名（不含路径与 .exe），用作服务端 db 名，如 abc -> abc.db
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
