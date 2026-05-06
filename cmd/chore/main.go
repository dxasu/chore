package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/dxasu/chore/client"

	"github.com/atotto/clipboard"
)

// Override at build time with -ldflags "-X main.defaultServerURL=..."
var defaultServerURL = "http://localhost:2026"

// 编译时通过 -ldflags 注入
var (
	buildTime = "unknown"
	commitID  = "unknown"
	gitTag    = ""
)

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

func printVersion() {
	if gitTag != "" {
		fmt.Printf("tag:    %s\n", gitTag)
	}
	fmt.Printf("commit: %s\n", commitID)
	fmt.Printf("built:  %s\n", buildTime)
}

func main() {
	serverURL := flag.String("s", defaultServerURL, "chore_svr server URL")
	verbose := flag.Bool("v", false, "on success print detail and list URLs")
	openList := flag.Bool("o", false, "do not send; open browser to list page only")
	getID := flag.String("i", "", "paste id to fetch and print")
	cp := flag.Bool("c", false, "with -i: copy content to clipboard instead of stdout")
	title := flag.String("title", "", "optional title for the paste")
	tags := flag.String("tags", "", "optional comma-separated tags (max 10)")
	version := flag.Bool("version", false, "print build info and exit")
	flag.Usage = func() {
		name := clientNameFromExec()
		fmt.Fprintf(os.Stderr, "%s - send clipboard to chore_svr, one DB per executable name (e.g. abc -> abc.db)\n\nUsage:\n  %s [options]\n\nOptions:\n", name, name)
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n  %s                 read clipboard and upload (text or image)\n  %s -v              print detail URL and list URL\n  %s -o              open browser to list page\n  %s -i 5            get and print content of paste #5\n  %s -i 5 -c         get content of paste #5 and copy it\n  %s -vc             combined short bool flags (equivalent to -v -c)\n  %s -icv 5          mixed short flags (equivalent to -c -v -i 5)\n  %s -title \"Note\" -tags a,b,c  upload with optional title and tags\n  %s -s http://host:9000         use custom server\n", name, name, name, name, name, name, name, name, name)
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

	if *version {
		printVersion()
		return
	}

	clientName := clientNameFromExec()
	baseURL := strings.TrimSuffix(*serverURL, "/")
	listURL := baseURL + "/list/" + clientName
	ctx := context.Background()
	c := client.New(baseURL, clientName)

	// -i: 从服务器按 id 获取内容
	if strings.TrimSpace(*getID) != "" {
		id, err := parseID(strings.TrimSpace(*getID))
		if err != nil {
			fail("%v", err)
		}
		p, err := c.Get(ctx, id)
		if errors.Is(err, client.ErrNotFound) {
			fail("id %d not found", id)
		}
		if err != nil {
			fail("get: %v", err)
		}

		if p.HasTag("png") {
			// 图片记录：从服务器拉取原始字节
			imgBytes, err := c.FetchImage(ctx, p.Content)
			if err != nil {
				fail("fetch image: %v", err)
			}
			if *cp {
				// -c：将图片复制到剪贴板
				if err := copyImageToClipboard(imgBytes); err != nil {
					fail("copy image to clipboard: %v", err)
				}
				return
			}
			// 无 -c：在终端以彩色字符画预览图片
			img, err := png.Decode(bytes.NewReader(imgBytes))
			if err != nil {
				fail("decode image: %v", err)
			}
			renderImageToTerminal(img, termWidth())
			return
		}

		// 文字记录
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

	tagsSlice := parseTags(*tags)

	// 尝试读文字剪贴板
	textContent, textErr := clipboard.ReadAll()
	if textErr == nil && trimSpace(textContent) != "" {
		result, err := c.UploadText(ctx, trimSpace(textContent), *title, tagsSlice)
		if err != nil {
			fail("upload: %v", err)
		}
		printResult(result, *serverURL, *verbose)
		return
	}

	// 尝试读图片剪贴板
	imgData, imgErr := readClipboardImage()
	if imgErr != nil {
		fail("read image from clipboard: %v", imgErr)
	}
	if imgData != nil {
		result, err := c.UploadImage(ctx, imgData, *title, tagsSlice)
		if err != nil {
			fail("upload image: %v", err)
		}
		printResult(result, *serverURL, *verbose)
		return
	}

	// 既不是文字也不是图片
	desc := describeClipboard()
	fail("clipboard contains unsupported content\nfound: %s", desc)
}

// ──────────────────────────────────────────────
// 终端图片渲染
// ──────────────────────────────────────────────

// renderImageToTerminal 将图片以 24-bit 真彩色字符画输出到终端。
// 使用 ▄（下半块）配合前景/背景色，每个字符表示 2 行像素，同比例压缩到 maxWidth 列。
func renderImageToTerminal(img image.Image, maxWidth int) {
	bounds := img.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()
	if srcW == 0 || srcH == 0 {
		return
	}

	// 等比缩放：终端字符约 2:1（高:宽），▄ 每字符覆盖 2 行像素，故像素比例不变
	targetW := srcW
	if targetW > maxWidth {
		targetW = maxWidth
	}
	targetH := targetW * srcH / srcW
	if targetH < 2 {
		targetH = 2
	}
	if targetH%2 != 0 {
		targetH++
	}

	scaled := resizeNearest(img, targetW, targetH)

	var sb strings.Builder
	for y := 0; y < targetH; y += 2 {
		for x := 0; x < targetW; x++ {
			tr, tg, tb := pixelRGB(scaled, x, y)
			br, bg, bb := pixelRGB(scaled, x, y+1) // targetH 已保证为偶数
			// 上半像素 → 背景色，下半像素 → 前景色，▄ 填充下半
			sb.WriteString(fmt.Sprintf("\x1b[48;2;%d;%d;%dm\x1b[38;2;%d;%d;%dm▄", tr, tg, tb, br, bg, bb))
		}
		sb.WriteString("\x1b[0m\n")
	}
	fmt.Print(sb.String())
}

// resizeNearest 使用最近邻算法将图片缩放到指定像素尺寸。
func resizeNearest(src image.Image, newW, newH int) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	b := src.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	for y := 0; y < newH; y++ {
		for x := 0; x < newW; x++ {
			dst.Set(x, y, src.At(b.Min.X+x*srcW/newW, b.Min.Y+y*srcH/newH))
		}
	}
	return dst
}

// pixelRGB 返回图片中 (x, y) 处像素的 8-bit RGB 分量。
func pixelRGB(img image.Image, x, y int) (r, g, b uint8) {
	b2 := img.Bounds()
	c := img.At(b2.Min.X+x, b2.Min.Y+y)
	r16, g16, b16, _ := c.RGBA()
	return uint8(r16 >> 8), uint8(g16 >> 8), uint8(b16 >> 8)
}

// termWidth 返回终端列数，优先读取 COLUMNS 环境变量，其次 tput cols，最后默认 80。
func termWidth() int {
	if col := os.Getenv("COLUMNS"); col != "" {
		if n, _ := strconv.Atoi(col); n > 10 {
			return n
		}
	}
	if runtime.GOOS != "windows" {
		if out, err := exec.Command("tput", "cols").Output(); err == nil {
			if n, _ := strconv.Atoi(strings.TrimSpace(string(out))); n > 10 {
				return n
			}
		}
	}
	return 80
}

// ──────────────────────────────────────────────
// 剪贴板：读取图片
// ──────────────────────────────────────────────

// readClipboardImage 尝试从剪贴板读取 PNG 图片字节。
// 返回 (nil, nil) 表示剪贴板中没有图片（不是错误）。
func readClipboardImage() ([]byte, error) {
	switch runtime.GOOS {
	case "darwin":
		return readClipboardImageMac()
	case "linux":
		return readClipboardImageLinux()
	case "windows":
		return readClipboardImageWindows()
	default:
		return nil, nil
	}
}

// readClipboardImageMac 用 AppleScript 将剪贴板 PNG 写入临时文件后读取。
func readClipboardImageMac() ([]byte, error) {
	tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("chore_clip_%d.png", time.Now().UnixNano()))
	script := `try
	set imgData to the clipboard as «class PNGf»
	set fd to open for access POSIX file "` + tmpFile + `" with write permission
	set eof of fd to 0
	write imgData to fd
	close access fd
	return "ok"
on error
	return "no"
end try`
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		return nil, nil
	}
	if strings.TrimSpace(string(out)) != "ok" {
		return nil, nil
	}
	defer os.Remove(tmpFile)
	return os.ReadFile(tmpFile)
}

// readClipboardImageLinux 用 xclip 读取剪贴板中的 PNG。
func readClipboardImageLinux() ([]byte, error) {
	out, err := exec.Command("xclip", "-selection", "clipboard", "-t", "image/png", "-o").Output()
	if err != nil || len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// readClipboardImageWindows 用 PowerShell 将剪贴板图片保存为 PNG 后读取。
func readClipboardImageWindows() ([]byte, error) {
	tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("chore_clip_%d.png", time.Now().UnixNano()))
	// 用单引号包裹路径；单引号字符用 '' 转义
	escapedPath := strings.ReplaceAll(tmpFile, "'", "''")
	script := fmt.Sprintf(`
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
$img = [System.Windows.Forms.Clipboard]::GetImage()
if ($img -ne $null) {
    $img.Save('%s', [System.Drawing.Imaging.ImageFormat]::Png)
    $img.Dispose()
    Write-Output 'ok'
} else {
    Write-Output 'no'
}`, escapedPath)
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return nil, nil
	}
	if strings.TrimSpace(string(out)) != "ok" {
		return nil, nil
	}
	defer os.Remove(tmpFile)
	return os.ReadFile(tmpFile)
}

// ──────────────────────────────────────────────
// 剪贴板：写入图片
// ──────────────────────────────────────────────

// copyImageToClipboard 将 PNG 字节写入系统剪贴板。
func copyImageToClipboard(data []byte) error {
	switch runtime.GOOS {
	case "darwin":
		return copyImageToClipboardMac(data)
	case "linux":
		return copyImageToClipboardLinux(data)
	case "windows":
		return copyImageToClipboardWindows(data)
	default:
		return fmt.Errorf("copy image to clipboard not supported on %s", runtime.GOOS)
	}
}

func copyImageToClipboardMac(data []byte) error {
	tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("chore_paste_%d.png", time.Now().UnixNano()))
	if err := os.WriteFile(tmpFile, data, 0o600); err != nil {
		return err
	}
	defer os.Remove(tmpFile)
	script := `set the clipboard to (read (POSIX file "` + tmpFile + `") as «class PNGf»)`
	return exec.Command("osascript", "-e", script).Run()
}

func copyImageToClipboardLinux(data []byte) error {
	cmd := exec.Command("xclip", "-selection", "clipboard", "-t", "image/png")
	cmd.Stdin = bytes.NewReader(data)
	return cmd.Run()
}

func copyImageToClipboardWindows(data []byte) error {
	tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("chore_paste_%d.png", time.Now().UnixNano()))
	if err := os.WriteFile(tmpFile, data, 0o600); err != nil {
		return err
	}
	defer os.Remove(tmpFile)
	escapedPath := strings.ReplaceAll(tmpFile, "'", "''")
	script := fmt.Sprintf(`
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
$img = [System.Drawing.Image]::FromFile('%s')
[System.Windows.Forms.Clipboard]::SetImage($img)
$img.Dispose()`, escapedPath)
	return exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Run()
}

// ──────────────────────────────────────────────
// 剪贴板信息
// ──────────────────────────────────────────────

// describeClipboard 返回剪贴板当前内容类型的描述，用于不支持内容的错误提示。
func describeClipboard() string {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("osascript", "-e", `clipboard info`).Output()
		if err != nil {
			return "unknown (osascript failed)"
		}
		info := strings.TrimSpace(string(out))
		if info == "" {
			return "empty"
		}
		return info
	default:
		return fmt.Sprintf("unknown (unsupported platform: %s)", runtime.GOOS)
	}
}

// ──────────────────────────────────────────────
// 辅助函数
// ──────────────────────────────────────────────

// printResult 在 verbose 模式下打印上传结果。
func printResult(r *client.UploadResult, serverURL string, verbose bool) {
	if !verbose {
		return
	}
	fmt.Printf("saved #%d %s\n", r.ID, r.CreatedAt)
	fmt.Printf("detail: %s%s\n", serverURL, r.DetailURL)
	if r.ListURL != "" {
		fmt.Printf("list: %s%s\n", serverURL, r.ListURL)
	}
}

// parseTags 将逗号分隔的标签字符串解析为切片。
func parseTags(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, t := range strings.Split(s, ",") {
		if v := strings.TrimSpace(t); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// parseID 将字符串解析为正整数 id。
func parseID(s string) (int64, error) {
	var id int64
	if _, err := fmt.Sscanf(s, "%d", &id); err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid id %q: must be a positive integer", s)
	}
	return id, nil
}

// fail prints to stderr and exits
func fail(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func openBrowser(rawURL string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", rawURL).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL).Start()
	default:
		return exec.Command("xdg-open", rawURL).Start()
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
