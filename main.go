package main

import (
	"archive/zip"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	uploadDir = "./data"
	maxSize   = 1 << 30 // 1GB
)

// 文件列表口令，为空时列表功能关闭
var listPassword = os.Getenv("FILEHUB_PASSWORD")

// 上传页面隐藏路径，由 FILEHUB_SECRET_PATH 配置；为空时使用根路径
var secretPath string

func main() {
	os.MkdirAll(uploadDir, 0755)

	secretPath = normalizeSecretPath(os.Getenv("FILEHUB_SECRET_PATH"))
	if secretPath == "" {
		http.HandleFunc("/", indexHandler)
		http.HandleFunc("/upload", uploadHandler)
		http.HandleFunc("/files", filesHandler)
		http.HandleFunc("/zip", zipHandler)
		http.HandleFunc("/icon/", iconHandler)
	} else {
		http.HandleFunc(secretPath, indexHandler)
		http.HandleFunc(secretPath+"/", indexHandler)
		http.HandleFunc(secretPath+"/upload", uploadHandler)
		http.HandleFunc(secretPath+"/files", filesHandler)
		http.HandleFunc(secretPath+"/zip", zipHandler)
		http.HandleFunc(secretPath+"/icon/", iconHandler)
		// 根路径及其它未匹配路径统一返回 404，隐藏上传入口
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		})
		log.Printf("Upload page hidden at configured secret path")
	}
	http.HandleFunc("/f/", downloadHandler)

	addr := ":8080"
	server := &http.Server{
		Addr:              addr,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	log.Printf("FileHub listening on %s", addr)
	log.Fatal(server.ListenAndServe())
}

// 校验并规范化隐藏路径，必须是单个路径段，避免与内部路由冲突
func normalizeSecretPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "/" {
		return ""
	}
	if !strings.HasPrefix(raw, "/") {
		raw = "/" + raw
	}
	raw = strings.TrimRight(raw, "/")
	if raw == "" {
		return ""
	}
	if len(raw) < 7 || len(raw) > 65 {
		log.Fatalf("FILEHUB_SECRET_PATH must be 6-64 characters after '/', e.g. /my-secret-path")
	}
	for _, r := range raw[1:] {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			log.Fatalf("FILEHUB_SECRET_PATH may only contain letters, digits, '-' and '_'")
		}
	}
	switch raw {
	case "/f", "/upload", "/files", "/zip":
		log.Fatalf("FILEHUB_SECRET_PATH conflicts with a reserved route: %s", raw)
	}
	return raw
}

// 首页：极简上传页面
func indexHandler(w http.ResponseWriter, r *http.Request) {
	if secretPath == "" {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
	} else if r.URL.Path != secretPath && r.URL.Path != secretPath+"/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	fmt.Fprint(w, strings.ReplaceAll(indexHTML, "__BASE__", secretPath))
}

// 上传入口首页：隐藏路径为空时返回根路径
func homePath() string {
	if secretPath == "" {
		return "/"
	}
	return secretPath
}

// 上传处理
func uploadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 限制请求体大小
	r.Body = http.MaxBytesReader(w, r.Body, maxSize)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "file too large or bad form", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// 保留原始文件名；重名时自动追加时间戳
	originalName := header.Filename
	safeName := sanitizeFilename(originalName)
	dst, storedName, err := createUniqueFile(uploadDir, safeName)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	dstPath := filepath.Join(uploadDir, storedName)

	if _, err := io.Copy(dst, file); err != nil {
		dst.Close()
		os.Remove(dstPath)
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	if err := dst.Close(); err != nil {
		os.Remove(dstPath)
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}

	fileURL := "/f/" + url.PathEscape(storedName)
	// 如果是 API 调用返回 JSON，否则返回 HTML
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"url": fileURL, "name": originalName})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<html><body style="font-family:sans-serif;padding:2em">
<h3>上传成功</h3>
<p><a href="%s">%s</a></p>
<p><a href="%s">返回</a></p></body></html>`,
		html.EscapeString(fileURL), html.EscapeString(storedName), html.EscapeString(homePath()))
}

// 下载处理
func downloadHandler(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/f/")
	path, ok := storedFilePath(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}

	// 强制下载，避免上传的 HTML/SVG 等在同源下执行脚本
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if disposition := mime.FormatMediaType("attachment", map[string]string{"filename": name}); disposition != "" {
		w.Header().Set("Content-Disposition", disposition)
	}
	// 使用 ServeContent 支持 Range / 断点续传
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// 多选下载：将选中的文件打包为 ZIP
func zipHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	names := r.Form["files"]
	if len(names) == 0 {
		http.Error(w, "未选择文件", http.StatusBadRequest)
		return
	}
	if len(names) > 500 {
		http.Error(w, "一次最多选择 500 个文件", http.StatusBadRequest)
		return
	}

	type zipItem struct {
		name string
		path string
		info os.FileInfo
	}
	items := make([]zipItem, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] {
			continue
		}
		path, ok := storedFilePath(name)
		if !ok {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		items = append(items, zipItem{name, path, info})
		seen[name] = true
	}
	if len(items) == 0 {
		http.Error(w, "没有可下载的文件", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="filehub-%s.zip"`, time.Now().Format("20060102-150405")))
	zw := zip.NewWriter(w)
	for _, item := range items {
		header, err := zip.FileInfoHeader(item.info)
		if err != nil {
			log.Printf("zip header %s: %v", item.name, err)
			continue
		}
		header.Name = item.name
		header.Method = zip.Store
		entry, err := zw.CreateHeader(header)
		if err != nil {
			log.Printf("zip create %s: %v", item.name, err)
			break
		}
		src, err := os.Open(item.path)
		if err != nil {
			log.Printf("zip open %s: %v", item.name, err)
			continue
		}
		_, err = io.Copy(entry, src)
		src.Close()
		if err != nil {
			log.Printf("zip copy %s: %v", item.name, err)
			break
		}
	}
	if err := zw.Close(); err != nil {
		log.Printf("zip close: %v", err)
	}
}

// 校验并返回数据目录下的文件路径，拒绝路径穿越
func storedFilePath(name string) (string, bool) {
	if name == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
		return "", false
	}
	return filepath.Join(uploadDir, name), true
}

// 清洗上传文件名，只去掉路径分隔符与控制字符，尽量保留原始名称
func sanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	name = strings.Map(func(r rune) rune {
		if r == 0 || unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "file"
	}

	const maxNameBytes = 200
	if len(name) > maxNameBytes {
		ext := filepath.Ext(name)
		if len(ext) > 40 {
			ext = truncateUTF8(ext, 40)
		}
		base := truncateUTF8(strings.TrimSuffix(name, filepath.Ext(name)), maxNameBytes-len(ext))
		base = strings.TrimSpace(base)
		if base == "" {
			base = "file"
		}
		name = base + ext
	}
	return name
}

func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

// 创建文件；若同名已存在，则在扩展名前追加时间戳，仍然冲突则追加序号
func createUniqueFile(dir, name string) (*os.File, string, error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	stamp := time.Now().Format("20060102-150405")

	for i := 0; ; i++ {
		candidate := name
		if i == 1 {
			candidate = base + "_" + stamp + ext
		} else if i > 1 {
			candidate = fmt.Sprintf("%s_%s_%d%s", base, stamp, i-1, ext)
		}

		f, err := os.OpenFile(filepath.Join(dir, candidate), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err == nil {
			return f, candidate, nil
		}
		if !os.IsExist(err) {
			return nil, "", err
		}
	}
}

// 文件列表与内嵌图标共用口令校验
func requireListAuth(w http.ResponseWriter, r *http.Request) bool {
	if listPassword == "" {
		http.Error(w, "文件列表未启用：请设置 FILEHUB_PASSWORD 环境变量", http.StatusNotFound)
		return false
	}
	_, pass, ok := r.BasicAuth()
	if !ok || subtle.ConstantTimeCompare([]byte(pass), []byte(listPassword)) != 1 {
		w.Header().Set("WWW-Authenticate", `Basic realm="FileHub", charset="UTF-8"`)
		http.Error(w, "需要口令", http.StatusUnauthorized)
		return false
	}
	return true
}

// 文件列表与图标接口的路径前缀，隐藏模式下随上传入口一起移动
func iconPrefix() string {
	return secretPath + "/icon/"
}

// 内嵌图标：exe/dll 等 PE 文件从资源中提取，.ico 直接返回
func iconHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requireListAuth(w, r) {
		return
	}
	name := strings.TrimPrefix(r.URL.Path, iconPrefix())
	path, ok := storedFilePath(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, ok := loadEmbeddedIcon(path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/x-icon")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	if r.Method == http.MethodHead {
		return
	}
	w.Write(data)
}

// 文件列表：需设置 FILEHUB_PASSWORD，通过 HTTP Basic Auth 校验口令
func filesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requireListAuth(w, r) {
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")

	entries, err := os.ReadDir(uploadDir)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	type fileItem struct {
		name    string
		size    int64
		modTime time.Time
	}
	files := make([]fileItem, 0, len(entries))
	var total int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		files = append(files, fileItem{entry.Name(), info.Size(), info.ModTime()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.After(files[j].modTime) })

	var b strings.Builder
	b.WriteString(`<!DOCTYPE html>
<html lang="zh">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>文件列表 - FileHub</title>
<style>
  body{font-family:system-ui,sans-serif;max-width:880px;margin:3em auto;padding:0 1em;color:#333}
  h1{font-weight:600}
  a{color:#4a90e2}
  table{width:100%;border-collapse:collapse;margin-top:1em}
  th,td{text-align:left;padding:.65em .4em;border-bottom:1px solid #eee;font-size:.95em}
  th{color:#888;font-weight:500}
  td.size,th.size{white-space:nowrap;text-align:right}
  td.ops,th.ops{white-space:nowrap;text-align:right}
  td.ops button{background:#fff;color:#4a90e2;border:1px solid #4a90e2;border-radius:6px;padding:.3em .7em;margin-left:.35em;cursor:pointer;font-size:.85em}
  td.ops button:hover{background:#f0f7ff}
  td.ops button.copied{background:#4a90e2;color:#fff}
  .check{width:2.2em;text-align:center}
  .check input{width:1.05em;height:1.05em;cursor:pointer;accent-color:#4a90e2}
  .icon{display:inline-block;width:1.6em;text-align:center}
  .icon img{width:1.25em;height:1.25em;object-fit:contain;vertical-align:-.2em}
  .bulk{display:flex;align-items:center;gap:.8em;margin-top:1.2em;flex-wrap:wrap}
  .bulk button{background:#4a90e2;color:#fff;border:none;border-radius:8px;padding:.65em 1.3em;cursor:pointer;font-size:.95em}
  .bulk button:hover{background:#357abd}
  .bulk button:disabled{background:#c9d6e6;cursor:not-allowed}
  #selCount{color:#888;font-size:.9em}
  .empty{color:#999;padding:2em 0}
</style>
</head>
<body>
<h1>📁 文件列表</h1>
`)
	fmt.Fprintf(&b, `<p>共 %d 个文件，总计 %s ｜ <a href="%s">上传文件</a></p>`, len(files), formatSize(total), html.EscapeString(homePath()))
	if len(files) == 0 {
		b.WriteString(`<p class="empty">还没有上传任何文件。</p>`)
	} else {
		fmt.Fprintf(&b, `<form method="post" action="%s">
<table>
<thead><tr><th class="check"><input type="checkbox" id="checkAll" aria-label="全选"></th><th>文件名</th><th class="size">大小</th><th>上传时间</th><th class="ops">操作</th></tr></thead>
<tbody>`, html.EscapeString(secretPath+"/zip"))
		for _, f := range files {
			name := html.EscapeString(f.name)
			href := html.EscapeString("/f/" + url.PathEscape(f.name))
			iconHTML := `<span class="icon">` + fileIcon(f.name) + `</span>`
			if hasEmbeddedIcon(f.name) {
				src := html.EscapeString(iconPrefix() + url.PathEscape(f.name))
				iconHTML = fmt.Sprintf(`<span class="icon"><img src="%s" alt="" loading="lazy" onerror="this.style.display='none';this.nextElementSibling.style.display='inline'"><span style="display:none">%s</span></span>`, src, fileIcon(f.name))
			}
			fmt.Fprintf(&b, `<tr><td class="check"><input type="checkbox" class="file-check" name="files" value="%s" aria-label="选择 %s"></td><td>%s<a href="%s">%s</a></td><td class="size">%s</td><td>%s</td><td class="ops"><button type="button" data-copy="%s">复制</button><button type="button" data-share="%s">分享</button></td></tr>`,
				name, name, iconHTML, href, name, formatSize(f.size), f.modTime.Format("2006-01-02 15:04:05"), href, href)
		}
		b.WriteString(`</tbody></table>
<div class="bulk">
<button type="submit" id="downloadSelected">⬇️ 打包下载选中</button>
<span id="selCount">已选 0 个</span>
</div>
</form>`)
	}
	b.WriteString(`<script>
function copyText(text){
  if(navigator.clipboard && window.isSecureContext){
    return navigator.clipboard.writeText(text).catch(function(){return legacyCopy(text)});
  }
  return legacyCopy(text);
}
function legacyCopy(text){
  return new Promise(function(resolve,reject){
    var ta=document.createElement('textarea');
    ta.value=text;ta.style.position='fixed';ta.style.top='-9999px';
    document.body.appendChild(ta);ta.select();
    try{document.execCommand('copy')?resolve():reject(new Error('copy failed'))}
    catch(e){reject(e)}
    finally{ta.remove()}
  });
}
function absolute(path){return location.origin+path}
var checks=Array.prototype.slice.call(document.querySelectorAll('.file-check'));
var checkAll=document.getElementById('checkAll');
var selCount=document.getElementById('selCount');
var dlBtn=document.getElementById('downloadSelected');
function refreshSelection(){
  var n=checks.filter(function(c){return c.checked}).length;
  if(selCount) selCount.textContent='已选 '+n+' 个';
  if(dlBtn) dlBtn.disabled=n===0;
  if(checkAll){
    checkAll.checked=n>0 && n===checks.length;
    checkAll.indeterminate=n>0 && n<checks.length;
  }
}
if(checkAll){
  checkAll.addEventListener('change',function(){
    checks.forEach(function(c){c.checked=checkAll.checked});
    refreshSelection();
  });
}
checks.forEach(function(c){c.addEventListener('change',refreshSelection)});
refreshSelection();
document.querySelectorAll('[data-copy]').forEach(function(btn){
  btn.addEventListener('click',function(){
    copyText(absolute(btn.dataset.copy)).then(function(){
      btn.textContent='已复制';btn.classList.add('copied');
      setTimeout(function(){btn.textContent='复制';btn.classList.remove('copied')},1500);
    }).catch(function(){
      btn.textContent='复制失败';
      setTimeout(function(){btn.textContent='复制'},1500);
    });
  });
});
document.querySelectorAll('[data-share]').forEach(function(btn){
  if(!navigator.share){btn.remove();return}
  btn.addEventListener('click',function(){
    navigator.share({title:'FileHub 文件',url:absolute(btn.dataset.share)}).catch(function(){});
  });
});
</script>
</body></html>`)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, b.String())
}

func formatSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func fileIcon(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".svg", ".bmp", ".ico", ".avif", ".heic":
		return "🖼️"
	case ".mp4", ".mkv", ".avi", ".mov", ".webm", ".flv", ".m4v", ".wmv":
		return "🎬"
	case ".mp3", ".wav", ".flac", ".aac", ".ogg", ".m4a", ".wma", ".opus":
		return "🎵"
	case ".zip", ".rar", ".7z", ".tar", ".gz", ".bz2", ".xz", ".tgz":
		return "🗜️"
	case ".pdf":
		return "📕"
	case ".doc", ".docx", ".rtf":
		return "📝"
	case ".xls", ".xlsx", ".csv", ".ods":
		return "📊"
	case ".ppt", ".pptx", ".odp":
		return "📽️"
	case ".go", ".js", ".ts", ".jsx", ".tsx", ".py", ".java", ".c", ".cpp", ".h",
		".rs", ".php", ".rb", ".sh", ".ps1", ".html", ".css", ".json", ".yml",
		".yaml", ".xml", ".toml", ".sql":
		return "💻"
	case ".txt", ".md", ".log":
		return "📄"
	case ".exe", ".msi", ".apk", ".dmg", ".deb", ".rpm", ".iso":
		return "⚙️"
	case ".ttf", ".otf", ".woff", ".woff2":
		return "🔤"
	default:
		return "📎"
	}
}

const indexHTML = `<!DOCTYPE html>
<html lang="zh">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>FileHub</title>
<style>
  body{font-family:system-ui,sans-serif;max-width:640px;margin:4em auto;padding:0 1em;color:#333}
  h1{font-weight:600}
  a{color:#4a90e2}
  .box{border:2px dashed #ccc;border-radius:12px;padding:2em;text-align:center;transition:.2s}
  .box.drag{border-color:#4a90e2;background:#f0f7ff}
  input[type=file]{display:none}
  button{background:#4a90e2;color:#fff;border:none;padding:.7em 1.6em;border-radius:8px;cursor:pointer;font-size:1em}
  button:hover{background:#357abd}
  button.secondary{background:#fff;color:#4a90e2;border:1px solid #4a90e2}
  button.secondary:hover{background:#f0f7ff}
  .actions{display:flex;gap:.6em;flex-wrap:wrap;align-items:center;margin-top:.8em}
  #result{margin-top:1em;word-break:break-all}
  #result a{color:#4a90e2}
  .progress{margin-top:1em;height:6px;background:#eee;border-radius:3px;overflow:hidden;display:none}
  .progress>div{height:100%;width:0;background:#4a90e2;transition:width .2s}
  .status{margin-top:.6em;color:#666;font-size:.9em;min-height:1.2em}
</style>
</head>
<body>
<h1>📦 FileHub</h1>
<p><a href="__BASE__/files">📁 文件列表</a></p>
<div class="box" id="box">
  <p>拖拽文件到这里，或</p>
  <button onclick="document.getElementById('f').click()">选择文件</button>
  <input type="file" id="f">
</div>
<div class="progress" id="p"><div id="pb"></div></div>
<div class="status" id="status" aria-live="polite"></div>
<div id="result"></div>
<script>
const box=document.getElementById('box'),f=document.getElementById('f'),
      p=document.getElementById('p'),pb=document.getElementById('pb'),res=document.getElementById('result'),
      status=document.getElementById('status');
['dragover','dragenter'].forEach(e=>box.addEventListener(e,ev=>{ev.preventDefault();box.classList.add('drag')}));
['dragleave','drop'].forEach(e=>box.addEventListener(e,ev=>{ev.preventDefault();box.classList.remove('drag')}));
box.addEventListener('drop',ev=>{if(ev.dataTransfer.files.length)upload(ev.dataTransfer.files[0])});
f.addEventListener('change',()=>{if(f.files.length)upload(f.files[0])});

function showResult(r){
  res.textContent='';
  const p=document.createElement('p');
  p.textContent='✅ 上传成功：';
  const a=document.createElement('a');
  a.href=r.url;a.textContent=decodeURIComponent(r.url);a.target='_blank';a.rel='noopener';
  p.appendChild(a);
  res.appendChild(p);

  const actions=document.createElement('div');
  actions.className='actions';
  actions.appendChild(makeCopyButton(location.origin+r.url));
  if(navigator.share){
    const share=document.createElement('button');
    share.type='button';share.className='secondary';share.textContent='分享';
    share.onclick=()=>navigator.share({title:'FileHub 文件',url:location.origin+r.url}).catch(()=>{});
    actions.appendChild(share);
  }
  res.appendChild(actions);
}

function makeCopyButton(url){
  const btn=document.createElement('button');
  btn.type='button';btn.className='secondary';btn.textContent='复制链接';
  btn.onclick=()=>copyText(url).then(()=>{
    btn.textContent='已复制 ✓';
    setTimeout(()=>btn.textContent='复制链接',1500);
  }).catch(()=>{
    btn.textContent='复制失败';
    setTimeout(()=>btn.textContent='复制链接',1500);
  });
  return btn;
}

function copyText(text){
  if(navigator.clipboard && window.isSecureContext){
    return navigator.clipboard.writeText(text).catch(()=>legacyCopy(text));
  }
  return legacyCopy(text);
}
function legacyCopy(text){
  return new Promise((resolve,reject)=>{
    const ta=document.createElement('textarea');
    ta.value=text;ta.style.position='fixed';ta.style.top='-9999px';
    document.body.appendChild(ta);ta.select();
    try{document.execCommand('copy')?resolve():reject(new Error('copy failed'))}
    catch(e){reject(e)}
    finally{ta.remove()}
  });
}

function formatBytes(n){
  if(!isFinite(n)||n<0)n=0;
  if(n<1024)return Math.round(n)+' B';
  const units=['KB','MB','GB','TB'];
  let i=-1;
  do{n/=1024;i++}while(n>=1024&&i<units.length-1);
  return n.toFixed(n>=100?0:n>=10?1:2)+' '+units[i];
}
function formatSpeed(bytesPerSec){
  if(!isFinite(bytesPerSec)||bytesPerSec<=0)return '0 B/s';
  return formatBytes(bytesPerSec)+'/s';
}
function now(){
  return window.performance&&performance.now?performance.now():Date.now();
}

function upload(file){
  const fd=new FormData();
  fd.append('file',file);
  const xhr=new XMLHttpRequest();
  xhr.open('POST','__BASE__/upload');
  xhr.setRequestHeader('Accept','application/json');
  const started=now();
  p.style.display='block';pb.style.width='0';res.textContent='';
  status.textContent='准备上传…';
  xhr.upload.onprogress=e=>{
    if(!e.lengthComputable||e.total<=0)return;
    const pct=Math.min(100,e.loaded/e.total*100);
    const secs=Math.max((now()-started)/1000,0.2);
    pb.style.width=pct+'%';
    status.textContent=pct.toFixed(1)+'% · '+formatBytes(e.loaded)+' / '+formatBytes(e.total)+' · 平均 '+formatSpeed(e.loaded/secs);
  };
  xhr.onload=()=>{
    p.style.display='none';pb.style.width='0';
    const secs=Math.max((now()-started)/1000,0.001);
    if(xhr.status===200){
      const r=JSON.parse(xhr.responseText);
      status.textContent='完成 · 用时 '+secs.toFixed(secs<10?1:0)+' 秒 · 平均 '+formatSpeed(file.size/secs);
      showResult(r);
    }else{
      status.textContent='';
      res.textContent='❌ 上传失败：'+xhr.responseText;
    }
  };
  xhr.onerror=()=>{p.style.display='none';status.textContent='网络错误';res.textContent='❌ 网络错误'};
  xhr.send(fd);
}
</script>
</body>
</html>`
