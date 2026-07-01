package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//go:embed static/*
var staticFiles embed.FS

var prevRx, prevTx uint64
var prevNetTime time.Time

func main() {
	port := ":9090"
	if p := os.Getenv("PORT"); p != "" {
		port = ":" + p
	}

	http.HandleFunc("/api/system", cors(systemHandler))
	http.HandleFunc("/api/containers", cors(containersHandler))
	http.HandleFunc("/api/container/", cors(containerActionHandler))
	http.HandleFunc("/api/services", cors(servicesHandler))
	http.HandleFunc("/api/service/", cors(serviceActionHandler))
	http.HandleFunc("/api/disks", cors(disksHandler))
	http.HandleFunc("/api/processes", cors(processesHandler))
	http.HandleFunc("/api/files/read", cors(filesReadHandler))
	http.HandleFunc("/api/files/upload", cors(filesUploadHandler))
	http.HandleFunc("/api/files/delete", cors(filesDeleteHandler))
	http.HandleFunc("/api/files/mkdir", cors(filesMkdirHandler))
	http.HandleFunc("/api/apps", cors(appsHandler))
	http.HandleFunc("/api/apps/install", cors(appsInstallHandler))

	staticSub, _ := fs.Sub(staticFiles, "static")
	http.Handle("/", http.FileServer(http.FS(staticSub)))

	fmt.Printf("\033[92m=== WK Panel v2.0 on http://0.0.0.0%s ===\033[0m\n", port)
	log.Fatal(http.ListenAndServe(port, nil))
}

func cors(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		if r.Method == "OPTIONS" { return }
		h(w, r)
	}
}

// ---------- System ----------
type SystemInfo struct {
	Hostname string  `json:"hostname"`
	OS       string  `json:"os"`
	Kernel   string  `json:"kernel"`
	Uptime   string  `json:"uptime"`
	CPU      float64 `json:"cpu"`
	CPUCores int     `json:"cpuCores"`
	MemTotal uint64  `json:"memTotal"`
	MemUsed  uint64  `json:"memUsed"`
	MemPct   float64 `json:"memPct"`
	SwapT    uint64  `json:"swapTotal"`
	SwapU    uint64  `json:"swapUsed"`
	Load1    float64 `json:"load1"`
	Load5    float64 `json:"load5"`
	Load15   float64 `json:"load15"`
	NetRx    uint64  `json:"netRx"`
	NetTx    uint64  `json:"netTx"`
}

func systemHandler(w http.ResponseWriter, r *http.Request) {
	info := SystemInfo{
		Hostname: readString("/proc/sys/kernel/hostname"),
		OS: readOS(), Kernel: readString("/proc/sys/kernel/ostype") + " " + readString("/proc/sys/kernel/osrelease"),
		Uptime: readUptime(), CPUCores: readCPUCores(), CPU: readCPUPercent(),
		Load1: readLoad(0), Load5: readLoad(1), Load15: readLoad(2),
	}
	if mem, err := parseMemInfo(); err == nil {
		info.MemTotal = mem["MemTotal"]; info.MemUsed = mem["MemTotal"] - mem["MemFree"] - mem["Buffers"] - mem["Cached"]
		info.MemPct = float64(info.MemUsed) / float64(info.MemTotal) * 100
		info.SwapT = mem["SwapTotal"]; info.SwapU = mem["SwapTotal"] - mem["SwapFree"]
	}
	if rx, tx, err := readNetStats("eth0"); err == nil {
		now := time.Now()
		if !prevNetTime.IsZero() {
			elapsed := now.Sub(prevNetTime).Seconds()
			if elapsed > 0 { info.NetRx = uint64(float64(rx-prevRx) / elapsed); info.NetTx = uint64(float64(tx-prevTx) / elapsed) }
		}
		prevRx, prevTx, prevNetTime = rx, tx, now
	}
	json.NewEncoder(w).Encode(info)
}

// ---------- Docker ----------
type Container struct {
	ID string `json:"id"` Name string `json:"name"` Image string `json:"image"`
	Status string `json:"status"` State string `json:"state"` Ports string `json:"ports"` Created string `json:"created"`
}

func containersHandler(w http.ResponseWriter, r *http.Request) {
	out, _ := exec.Command("docker", "ps", "-a", "--format", "{{.ID}}\t{{.Names}}\t{{.Image}}\t{{.Status}}\t{{.State}}\t{{.Ports}}\t{{.CreatedAt}}").Output()
	var containers []Container
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" { continue }
		parts := strings.Split(line, "\t")
		if len(parts) < 7 { continue }
		containers = append(containers, Container{ID: parts[0][:12], Name: parts[1], Image: parts[2], Status: parts[3], State: parts[4], Ports: parts[5], Created: parts[6]})
	}
	json.NewEncoder(w).Encode(containers)
}

func containerActionHandler(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/container/"), "/")
	if len(parts) < 2 { http.Error(w, "bad request", 400); return }
	var cmd *exec.Cmd
	switch parts[1] {
	case "start": cmd = exec.Command("docker", "start", parts[0])
	case "stop": cmd = exec.Command("docker", "stop", parts[0])
	case "restart": cmd = exec.Command("docker", "restart", parts[0])
	default: http.Error(w, "unknown action", 400); return
	}
	if err := cmd.Run(); err != nil { json.NewEncoder(w).Encode(map[string]string{"status": "error", "msg": err.Error()}); return }
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// ---------- Services ----------
func servicesHandler(w http.ResponseWriter, r *http.Request) {
	svcs := []string{"nginx", "php8.2-fpm", "docker", "sshd", "music-dl", "cloudflared", "ttyd", "smbd", "nmbd"}
	var result []map[string]string
	for _, s := range svcs {
		out, _ := exec.Command("systemctl", "is-active", s).Output()
		result = append(result, map[string]string{"name": s, "status": strings.TrimSpace(string(out))})
	}
	json.NewEncoder(w).Encode(result)
}

func serviceActionHandler(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/service/"), "/")
	if len(parts) < 2 { http.Error(w, "bad request", 400); return }
	var cmd *exec.Cmd
	switch parts[1] {
	case "start": cmd = exec.Command("systemctl", "start", parts[0])
	case "stop": cmd = exec.Command("systemctl", "stop", parts[0])
	case "restart": cmd = exec.Command("systemctl", "restart", parts[0])
	default: http.Error(w, "unknown action", 400); return
	}
	if err := cmd.Run(); err != nil { json.NewEncoder(w).Encode(map[string]string{"status": "error"}); return }
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// ---------- Disks ----------
func disksHandler(w http.ResponseWriter, r *http.Request) {
	out, _ := exec.Command("df", "-h", "--output=target,source,size,used,avail,pcent").Output()
	var disks []map[string]string
	for i, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if i == 0 || line == "" { continue }
		parts := strings.Fields(line)
		if len(parts) >= 6 {
			if strings.Contains(line, "overlay") || strings.Contains(line, "docker") { continue }
			disks = append(disks, map[string]string{"mount": parts[0], "device": parts[1], "size": parts[2], "used": parts[3], "avail": parts[4], "pct": parts[5]})
		}
	}
	json.NewEncoder(w).Encode(disks)
}

// ---------- Processes ----------
func processesHandler(w http.ResponseWriter, r *http.Request) {
	out, _ := exec.Command("ps", "aux", "--sort=-%mem").Output()
	var procs []map[string]string
	for i, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if i == 0 || i > 20 || line == "" { continue }
		fields := strings.Fields(line)
		if len(fields) < 11 { continue }
		procs = append(procs, map[string]string{"user": fields[0], "cpu": fields[2], "mem": fields[3], "rss": fields[5], "cmd": strings.Join(fields[10:], " ")})
	}
	json.NewEncoder(w).Encode(procs)
}

// ---------- File Manager ----------
func filesReadHandler(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" { path = "/" }
	entries, err := os.ReadDir(path)
	if err != nil { json.NewEncoder(w).Encode([]map[string]interface{}{{"error": err.Error()}}); return }
	type Entry struct { Name string `json:"name"` IsDir bool `json:"isDir"` Size int64 `json:"size"` Mode string `json:"mode"` }
	var files []Entry
	for _, e := range entries {
		info, _ := e.Info()
		f := Entry{Name: e.Name(), IsDir: e.IsDir()}
		if info != nil { f.Size = info.Size(); f.Mode = info.Mode().Perm().String() }
		files = append(files, f)
	}
	json.NewEncoder(w).Encode(files)
}

func filesUploadHandler(w http.ResponseWriter, r *http.Request) {
	r.ParseMultipartForm(32 << 20)
	dir := r.FormValue("path")
	if dir == "" { dir = "/tmp" }
	file, header, err := r.FormFile("file")
	if err != nil { json.NewEncoder(w).Encode(map[string]string{"status": "error", "msg": err.Error()}); return }
	defer file.Close()
	dst, err := os.Create(filepath.Join(dir, header.Filename))
	if err != nil { json.NewEncoder(w).Encode(map[string]string{"status": "error", "msg": err.Error()}); return }
	defer dst.Close()
	io.Copy(dst, file)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func filesDeleteHandler(w http.ResponseWriter, r *http.Request) {
	path := r.FormValue("path")
	if path == "" { http.Error(w, "no path", 400); return }
	if err := os.RemoveAll(path); err != nil { json.NewEncoder(w).Encode(map[string]string{"status": "error"}); return }
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func filesMkdirHandler(w http.ResponseWriter, r *http.Request) {
	path := r.FormValue("path")
	if path == "" { http.Error(w, "no path", 400); return }
	if err := os.MkdirAll(path, 0755); err != nil { json.NewEncoder(w).Encode(map[string]string{"status": "error"}); return }
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// ---------- App Store ----------
type App struct { Name string `json:"name"` Desc string `json:"desc"` Image string `json:"image"` Port string `json:"port"` Category string `json:"category"` }

func appsHandler(w http.ResponseWriter, r *http.Request) {
	apps := []App{
		{"FileBrowser", "文件管理", "filebrowser/filebrowser", "8080", "工具"},
		{"AriaNG", "Aria2 WebUI", "p3terx/ariang", "8082", "下载"},
		{"Aria2-Pro", "下载引擎", "p3terx/aria2-pro", "6800", "下载"},
		{"qBittorrent", "BT下载", "linuxserver/qbittorrent", "8085", "下载"},
		{"Jellyfin", "媒体服务器", "jellyfin/jellyfin", "8096", "媒体"},
		{"AList", "网盘聚合", "xhofe/alist", "5244", "工具"},
		{"Nginx", "Web服务器", "nginx:alpine", "8081", "Web"},
		{"MySQL", "数据库", "mysql:8", "3306", "数据库"},
		{"Redis", "缓存", "redis:alpine", "6379", "数据库"},
		{"Portainer", "Docker管理", "portainer/portainer-ce", "9000", "工具"},
		{"WordPress", "博客系统", "wordpress:latest", "8083", "Web"},
		{"HomeAssistant", "智能家居", "ghcr.io/home-assistant/home-assistant", "8123", "智能"},
	}
	json.NewEncoder(w).Encode(apps)
}

func appsInstallHandler(w http.ResponseWriter, r *http.Request) {
	image := r.FormValue("image")
	name := r.FormValue("name")
	port := r.FormValue("port")
	if image == "" || name == "" { http.Error(w, "missing params", 400); return }
	cmdStr := fmt.Sprintf("docker run -d --name=%s --restart=always -p %s:%s %s", name, port, port, image)
	cmd := exec.Command("sh", "-c", cmdStr)
	if out, err := cmd.CombinedOutput(); err != nil {
		json.NewEncoder(w).Encode(map[string]string{"status": "error", "msg": string(out)})
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// ---------- Helpers ----------
func readString(p string) string { d, _ := os.ReadFile(p); return strings.TrimSpace(string(d)) }

func readOS() string {
	d, _ := os.ReadFile("/etc/os-release")
	for _, line := range strings.Split(string(d), "\n") {
		if strings.HasPrefix(line, "PRETTY_NAME=") { return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), "\"") }
	}
	return "Linux"
}

func readUptime() string {
	d, _ := os.ReadFile("/proc/uptime")
	var sec float64
	fmt.Sscanf(string(d), "%f", &sec)
	dys := int(sec) / 86400; hrs := (int(sec) % 86400) / 3600; min := (int(sec) % 3600) / 60
	return fmt.Sprintf("%dd %dh %dm", dys, hrs, min)
}

func readCPUCores() int {
	d, _ := os.ReadFile("/proc/cpuinfo")
	return strings.Count(string(d), "processor\t:")
}

func readCPUPercent() float64 {
	d1, _ := os.ReadFile("/proc/stat")
	var i1, t1 uint64
	fmt.Sscanf(string(d1), "cpu  %d %d %d %d", &t1, &t1, &t1, &i1)
	t1 += i1
	time.Sleep(500 * time.Millisecond)
	d2, _ := os.ReadFile("/proc/stat")
	var i2, t2 uint64
	fmt.Sscanf(string(d2), "cpu  %d %d %d %d", &t2, &t2, &t2, &i2)
	t2 += i2
	dt, di := t2-t1, i2-i1
	if dt == 0 { return 0 }
	return float64(dt-di) / float64(dt) * 100
}

func readLoad(i int) float64 {
	d, _ := os.ReadFile("/proc/loadavg")
	var l [3]float64
	fmt.Sscanf(string(d), "%f %f %f", &l[0], &l[1], &l[2])
	return l[i]
}

func parseMemInfo() (map[string]uint64, error) {
	d, err := os.ReadFile("/proc/meminfo")
	if err != nil { return nil, err }
	m := make(map[string]uint64)
	for _, line := range strings.Split(string(d), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 2 { continue }
		key := strings.TrimSuffix(parts[0], ":")
		val, _ := strconv.ParseUint(parts[1], 10, 64)
		m[key] = val * 1024
	}
	return m, nil
}

func readNetStats(iface string) (rx, tx uint64, err error) {
	rxS, _ := os.ReadFile("/sys/class/net/" + iface + "/statistics/rx_bytes")
	txS, _ := os.ReadFile("/sys/class/net/" + iface + "/statistics/tx_bytes")
	rx, _ = strconv.ParseUint(strings.TrimSpace(string(rxS)), 10, 64)
	tx, _ = strconv.ParseUint(strings.TrimSpace(string(txS)), 10, 64)
	return
}
