package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	uploadDir = "./data"
	maxSize   = 1 << 30 // 1GB
)

func main() {
	os.MkdirAll(uploadDir, 0755)

	http.HandleFunc("/", indexHandler)
	http.HandleFunc("/upload", uploadHandler)
	http.HandleFunc("/f/", downloadHandler)

	addr := ":8080"
	log.Printf("FileHub listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

// 首页：极简上传页面
func indexHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, indexHTML)
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

	// 生成安全文件名：随机串 + 原扩展名
	ext := filepath.Ext(header.Filename)
	randName := randomHex(8) + ext
	dstPath := filepath.Join(uploadDir, randName)

	dst, err := os.Create(dstPath)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	if _, err := io.Copy(dst, file); err != nil {
		os.Remove(dstPath)
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}

	url := "/f/" + randName
	// 如果是 API 调用返回 JSON，否则返回 HTML
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"url":%q,"name":%q}`, url, header.Filename)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<html><body style="font-family:sans-serif;padding:2em">
<h3>上传成功</h3>
<p><a href="%s">%s</a></p>
<p><a href="/">返回</a></p></body></html>`, url, url)
}

// 下载处理
func downloadHandler(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/f/")
	// 防路径穿越
	if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(uploadDir, name)
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	// 使用 ServeFile 支持 Range / 断点续传
	http.ServeFile(w, r, path)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
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
  .box{border:2px dashed #ccc;border-radius:12px;padding:2em;text-align:center;transition:.2s}
  .box.drag{border-color:#4a90e2;background:#f0f7ff}
  input[type=file]{display:none}
  button{background:#4a90e2;color:#fff;border:none;padding:.7em 1.6em;border-radius:8px;cursor:pointer;font-size:1em}
  button:hover{background:#357abd}
  #result{margin-top:1em;word-break:break-all}
  #result a{color:#4a90e2}
  .progress{margin-top:1em;height:6px;background:#eee;border-radius:3px;overflow:hidden;display:none}
  .progress>div{height:100%;width:0;background:#4a90e2;transition:width .2s}
</style>
</head>
<body>
<h1>📦 FileHub</h1>
<div class="box" id="box">
  <p>拖拽文件到这里，或</p>
  <button onclick="document.getElementById('f').click()">选择文件</button>
  <input type="file" id="f">
</div>
<div class="progress" id="p"><div id="pb"></div></div>
<div id="result"></div>
<script>
const box=document.getElementById('box'),f=document.getElementById('f'),
      p=document.getElementById('p'),pb=document.getElementById('pb'),res=document.getElementById('result');
['dragover','dragenter'].forEach(e=>box.addEventListener(e,ev=>{ev.preventDefault();box.classList.add('drag')}));
['dragleave','drop'].forEach(e=>box.addEventListener(e,ev=>{ev.preventDefault();box.classList.remove('drag')}));
box.addEventListener('drop',ev=>{if(ev.dataTransfer.files.length)upload(ev.dataTransfer.files[0])});
f.addEventListener('change',()=>{if(f.files.length)upload(f.files[0])});

function upload(file){
  const fd=new FormData();
  fd.append('file',file);
  const xhr=new XMLHttpRequest();
  xhr.open('POST','/upload');
  xhr.setRequestHeader('Accept','application/json');
  p.style.display='block';res.innerHTML='';
  xhr.upload.onprogress=e=>{if(e.lengthComputable)pb.style.width=(e.loaded/e.total*100)+'%'};
  xhr.onload=()=>{
    p.style.display='none';pb.style.width='0';
    if(xhr.status===200){
      const r=JSON.parse(xhr.responseText);
      res.innerHTML='✅ 上传成功：<a href="'+r.url+'" target="_blank">'+r.url+'</a>';
    }else{res.textContent='❌ 上传失败：'+xhr.responseText}
  };
  xhr.onerror=()=>{p.style.display='none';res.textContent='❌ 网络错误'};
  xhr.send(fd);
}
</script>
</body>
</html>`