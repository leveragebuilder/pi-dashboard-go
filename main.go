package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type processInfo struct {
	PID     int     `json:"pid"`
	Command string  `json:"command"`
	CPU     float64 `json:"cpu"`
	MemPct  float64 `json:"mem_pct"`
	RSSMB   float64 `json:"rss_mb"`
}

type memoryStats struct {
	TotalMB int `json:"total_mb"`
	UsedMB  int `json:"used_mb"`
	FreeMB  int `json:"free_mb"`
	UsedPct int `json:"used_pct"`
	SwapMB  int `json:"swap_mb"`
}

type storageStats struct {
	TotalGB float64 `json:"total_gb"`
	UsedGB  float64 `json:"used_gb"`
	FreeGB  float64 `json:"free_gb"`
	UsedPct int     `json:"used_pct"`
}

type statsResponse struct {
	Hostname     string        `json:"hostname"`
	TemperatureC float64       `json:"temperature_c"`
	Uptime       string        `json:"uptime"`
	Memory       memoryStats   `json:"memory"`
	Storage      storageStats  `json:"storage"`
	TopRAM       []processInfo `json:"top_ram"`
	TopCPU       []processInfo `json:"top_cpu"`
	Timestamp    string        `json:"timestamp"`
}

type killRequest struct {
	PID int `json:"pid"`
}

var pageTemplate = template.Must(template.New("page").Parse(pageHTML))

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/api/stats", handleStats)
	mux.HandleFunc("/api/kill", handleKill)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	addr := getenv("PI_DASHBOARD_ADDR", ":8088")
	log.Printf("starting pi-dashboard-go on %s", addr)
	log.Fatal(http.ListenAndServe(addr, logRequest(mux)))
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTemplate.Execute(w, map[string]any{
		"RefreshSeconds": getenv("PI_DASHBOARD_REFRESH_SECONDS", "5"),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := collectStats()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, stats)
}

func handleKill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req killRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if req.PID <= 1 {
		http.Error(w, "invalid pid", http.StatusBadRequest)
		return
	}
	if err := syscall.Kill(req.PID, syscall.SIGTERM); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "pid": req.PID})
}

func collectStats() (statsResponse, error) {
	hostname, _ := os.Hostname()
	tempC, _ := readTemperature()
	uptime, _ := readUptime()
	mem, err := readMemory()
	if err != nil {
		return statsResponse{}, err
	}
	storage, err := readStorage("/")
	if err != nil {
		return statsResponse{}, err
	}
	topRAM, err := topProcesses("rss")
	if err != nil {
		return statsResponse{}, err
	}
	topCPU, err := topProcesses("%cpu")
	if err != nil {
		return statsResponse{}, err
	}
	return statsResponse{
		Hostname:     hostname,
		TemperatureC: tempC,
		Uptime:       uptime,
		Memory:       mem,
		Storage:      storage,
		TopRAM:       topRAM,
		TopCPU:       topCPU,
		Timestamp:    time.Now().Format("15:04:05"),
	}, nil
}

func readTemperature() (float64, error) {
	data, err := os.ReadFile("/sys/class/thermal/thermal_zone0/temp")
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
	if err != nil {
		return 0, err
	}
	return value / 1000.0, nil
}

func readUptime() (string, error) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return "", fmt.Errorf("invalid uptime data")
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return "", err
	}
	return formatDuration(time.Duration(seconds) * time.Second), nil
}

func readMemory() (memoryStats, error) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return memoryStats{}, err
	}
	defer file.Close()

	values := map[string]int{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		parts := strings.Fields(scanner.Text())
		if len(parts) < 2 {
			continue
		}
		key := strings.TrimSuffix(parts[0], ":")
		val, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		values[key] = val
	}
	if err := scanner.Err(); err != nil {
		return memoryStats{}, err
	}

	totalMB := values["MemTotal"] / 1024
	availMB := values["MemAvailable"] / 1024
	usedMB := totalMB - availMB
	usedPct := 0
	if totalMB > 0 {
		usedPct = int(float64(usedMB) / float64(totalMB) * 100)
	}
	return memoryStats{
		TotalMB: totalMB,
		UsedMB:  usedMB,
		FreeMB:  availMB,
		UsedPct: usedPct,
		SwapMB:  values["SwapTotal"] / 1024,
	}, nil
}

func readStorage(path string) (storageStats, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return storageStats{}, err
	}
	total := float64(stat.Blocks) * float64(stat.Bsize)
	free := float64(stat.Bavail) * float64(stat.Bsize)
	used := total - free
	usedPct := 0
	if total > 0 {
		usedPct = int((used / total) * 100)
	}
	return storageStats{
		TotalGB: round1(total / 1024 / 1024 / 1024),
		UsedGB:  round1(used / 1024 / 1024 / 1024),
		FreeGB:  round1(free / 1024 / 1024 / 1024),
		UsedPct: usedPct,
	}, nil
}

func topProcesses(sortKey string) ([]processInfo, error) {
	cmd := exec.Command("ps", "-eo", "pid,comm,%cpu,%mem,rss", "--sort=-"+sortKey)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	var processes []processInfo
	for i, line := range lines {
		if i == 0 || len(processes) >= 10 {
			continue
		}
		fields := strings.Fields(string(line))
		if len(fields) < 5 {
			continue
		}
		pid, _ := strconv.Atoi(fields[0])
		cpu, _ := strconv.ParseFloat(fields[2], 64)
		memPct, _ := strconv.ParseFloat(fields[3], 64)
		rssKB, _ := strconv.ParseFloat(fields[4], 64)
		processes = append(processes, processInfo{
			PID:     pid,
			Command: fields[1],
			CPU:     cpu,
			MemPct:  memPct,
			RSSMB:   round1(rssKB / 1024.0),
		})
	}
	return processes, nil
}

func formatDuration(d time.Duration) string {
	seconds := int(d.Seconds())
	days := seconds / 86400
	hours := (seconds % 86400) / 3600
	mins := (seconds % 3600) / 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	}
	return fmt.Sprintf("%dh %dm", hours, mins)
}

func round1(v float64) float64 {
	return mathRound(v*10) / 10
}

func mathRound(v float64) float64 {
	if v < 0 {
		return float64(int(v - 0.5))
	}
	return float64(int(v + 0.5))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func logRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

const pageHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Pi Dashboard</title>
  <style>
    :root{color-scheme:dark;--bg:#0f1115;--panel:#171a21;--muted:#9aa4b2;--text:#edf2f7;--line:#2a3140;--green:#21c37b;--red:#ff5d73;--blue:#4da3ff;--amber:#ffcc66}
    *{box-sizing:border-box} body{margin:0;font-family:ui-sans-serif,system-ui,-apple-system,sans-serif;background:var(--bg);color:var(--text)}
    .wrap{max-width:1200px;margin:0 auto;padding:20px}
    h1{margin:0 0 6px;font-size:28px}.sub{color:var(--muted);margin-bottom:18px}
    .grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(240px,1fr));gap:16px;margin-bottom:16px}
    .card{background:var(--panel);border:1px solid var(--line);border-radius:16px;padding:18px;box-shadow:0 6px 20px rgba(0,0,0,.22)}
    .label{font-size:13px;letter-spacing:.08em;text-transform:uppercase;color:var(--muted);margin-bottom:10px}
    .value{font-size:34px;font-weight:700}
    .meta{margin-top:8px;color:var(--muted);font-size:14px}
    .bar{height:12px;background:#232a35;border-radius:999px;overflow:hidden;margin-top:12px}
    .fill{height:100%;background:linear-gradient(90deg,var(--green),#45d38f)}
    .fill.warn{background:linear-gradient(90deg,var(--amber),var(--red))}
    table{width:100%;border-collapse:collapse}
    th,td{padding:10px 8px;border-bottom:1px solid var(--line);text-align:left;font-size:14px}
    th{color:var(--muted);font-weight:600}
    button{background:transparent;color:var(--red);border:1px solid rgba(255,93,115,.45);border-radius:10px;padding:6px 10px;cursor:pointer}
    button:hover{background:rgba(255,93,115,.1)}
    .row2{display:grid;grid-template-columns:1fr 1fr;gap:16px}
    .stamp{margin-top:18px;color:var(--muted);font-size:13px}
    .ok{color:var(--green)} .warnText{color:var(--amber)}
    @media (max-width: 900px){.row2{grid-template-columns:1fr}}
  </style>
</head>
<body>
  <div class="wrap">
    <h1>Pi Dashboard</h1>
    <div class="sub">Lightweight local monitor. Auto-refresh every {{.RefreshSeconds}}s.</div>
    <div class="grid">
      <div class="card">
        <div class="label">Temperature</div>
        <div class="value" id="temperature">--.-°C</div>
        <div class="meta" id="temperatureState">Loading...</div>
      </div>
      <div class="card">
        <div class="label">Uptime</div>
        <div class="value" id="uptime">--</div>
        <div class="meta" id="hostname">Loading...</div>
      </div>
      <div class="card">
        <div class="label">RAM Usage</div>
        <div class="value" id="ramUsed">-- MB</div>
        <div class="meta" id="ramMeta">Loading...</div>
        <div class="bar"><div id="ramBar" class="fill" style="width:0%"></div></div>
      </div>
      <div class="card">
        <div class="label">Storage</div>
        <div class="value" id="storageUsed">-- GB</div>
        <div class="meta" id="storageMeta">Loading...</div>
        <div class="bar"><div id="storageBar" class="fill" style="width:0%"></div></div>
      </div>
    </div>
    <div class="row2">
      <div class="card">
        <div class="label">Top RAM Processes</div>
        <table>
          <thead><tr><th>PID</th><th>Command</th><th>%MEM</th><th>RSS MB</th><th></th></tr></thead>
          <tbody id="topRam"><tr><td colspan="5">Loading...</td></tr></tbody>
        </table>
      </div>
      <div class="card">
        <div class="label">Top CPU Processes</div>
        <table>
          <thead><tr><th>PID</th><th>Command</th><th>%CPU</th><th>%MEM</th><th></th></tr></thead>
          <tbody id="topCpu"><tr><td colspan="5">Loading...</td></tr></tbody>
        </table>
      </div>
    </div>
    <div class="stamp" id="updatedAt">Updated --</div>
  </div>
  <script>
    async function loadStats() {
      const res = await fetch('/api/stats');
      if (!res.ok) throw new Error('stats failed');
      const stats = await res.json();
      render(stats);
    }

    function render(stats) {
      document.getElementById('temperature').textContent = stats.temperature_c.toFixed(1) + '°C';
      document.getElementById('temperatureState').innerHTML = stats.temperature_c >= 70 ? '<span class="warnText">Hot</span>' : '<span class="ok">Normal</span>';
      document.getElementById('uptime').textContent = stats.uptime;
      document.getElementById('hostname').textContent = stats.hostname;

      document.getElementById('ramUsed').textContent = stats.memory.used_mb + ' MB';
      document.getElementById('ramMeta').textContent = stats.memory.free_mb + ' MB free of ' + stats.memory.total_mb + ' MB total';
      paintBar('ramBar', stats.memory.used_pct);

      document.getElementById('storageUsed').textContent = stats.storage.used_gb.toFixed(1) + ' GB';
      document.getElementById('storageMeta').textContent = stats.storage.free_gb.toFixed(1) + ' GB free of ' + stats.storage.total_gb.toFixed(1) + ' GB total';
      paintBar('storageBar', stats.storage.used_pct);

      renderTable('topRam', stats.top_ram, row =>
        '<tr>' +
          '<td>' + row.pid + '</td>' +
          '<td>' + escapeHtml(row.command) + '</td>' +
          '<td>' + row.mem_pct.toFixed(1) + '</td>' +
          '<td>' + row.rss_mb.toFixed(1) + '</td>' +
          '<td><button onclick="killProcess(' + row.pid + ', \'' + escapeJs(row.command) + '\')">Kill</button></td>' +
        '</tr>');
      renderTable('topCpu', stats.top_cpu, row =>
        '<tr>' +
          '<td>' + row.pid + '</td>' +
          '<td>' + escapeHtml(row.command) + '</td>' +
          '<td>' + row.cpu.toFixed(1) + '</td>' +
          '<td>' + row.mem_pct.toFixed(1) + '</td>' +
          '<td><button onclick="killProcess(' + row.pid + ', \'' + escapeJs(row.command) + '\')">Kill</button></td>' +
        '</tr>');

      document.getElementById('updatedAt').textContent = 'Updated ' + stats.timestamp;
    }

    function paintBar(id, pct) {
      const node = document.getElementById(id);
      node.style.width = pct + '%';
      node.className = 'fill' + (pct >= 80 ? ' warn' : '');
    }

    function renderTable(id, rows, renderRow) {
      const tbody = document.getElementById(id);
      if (!rows.length) {
        tbody.innerHTML = '<tr><td colspan="5">No processes found</td></tr>';
        return;
      }
      tbody.innerHTML = rows.map(renderRow).join('');
    }

    async function killProcess(pid, command) {
      if (!confirm('Kill PID ' + pid + ' (' + command + ')?')) return;
      const res = await fetch('/api/kill', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({pid})
      });
      if (!res.ok) {
        alert(await res.text());
        return;
      }
      await loadStats();
    }

    function escapeHtml(str) {
      return String(str).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;');
    }

    function escapeJs(str) {
      return String(str).replaceAll('\\', '\\\\').replaceAll("'", "\\'");
    }

    loadStats().catch(err => {
      document.getElementById('updatedAt').textContent = 'Error: ' + err.message;
    });
    setInterval(() => loadStats().catch(() => {}), {{.RefreshSeconds}} * 1000);
  </script>
</body>
</html>`
