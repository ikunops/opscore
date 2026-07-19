package handlers

import (
	"encoding/json"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/shirou/gopsutil/v4/process"
)

// ServiceInfo 统一描述一个"服务"条目。
// 在 Linux 上来自 systemd 单元;在其它平台降级为进程列表(demo 占位)。
type ServiceInfo struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Status      string  `json:"status"`     // active / inactive / running / sleep ...
	SubStatus   string  `json:"subStatus"`  // systemd 子状态
	Description string  `json:"description"`
	UnitFile    string  `json:"unitFile"`   // 单元文件位置
	LogHint     string  `json:"logHint"`    // 查看日志的命令
	IsProcess   bool    `json:"isProcess"`  // true = 进程降级列表
	PID         int32   `json:"pid,omitempty"`
	CPUPercent  float64 `json:"cpuPercent,omitempty"`
	MemPercent  float32 `json:"memPercent,omitempty"`
	// 常见服务识别(按单元名/进程名,属事实来源,安全标注)
	Recognized string `json:"recognized,omitempty"`
	Category   string `json:"category,omitempty"`
	Icon       string `json:"icon,omitempty"`
}

// ServicesList 返回服务列表与运行平台信息。
func ServicesList(w http.ResponseWriter, r *http.Request) {
	if runtime.GOOS == "linux" {
		WriteJSON(w, map[string]any{"os": "linux", "managed": true, "services": listSystemd()})
		return
	}
	WriteJSON(w, map[string]any{
		"os":       runtime.GOOS,
		"managed":  false,
		"services": listProcesses(),
		"note":     "当前非 Linux,服务启停不可用;以下为进程列表(demo 降级展示)",
	})
}

// listSystemd 解析 `systemctl list-units --type=service`,把 Linux 命令变成结构化数据。
func listSystemd() []ServiceInfo {
	out, err := exec.Command("systemctl", "list-units", "--type=service", "--no-legend", "--no-pager").Output()
	if err != nil {
		return nil
	}
	var res []ServiceInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		unit := fields[0]
		active := fields[2]
		sub := fields[3]
		desc := strings.Join(fields[4:], " ")
		si := ServiceInfo{ID: unit, Name: unit, Status: active, SubStatus: sub, Description: desc}
		if fp, e := exec.Command("systemctl", "show", "-p", "FragmentPath", "--value", unit).Output(); e == nil {
			si.UnitFile = strings.TrimSpace(string(fp))
		}
		si.LogHint = "journalctl -u " + unit
		if meta, ok := recognizeProc(unit); ok {
			si.Recognized = meta.Label
			si.Category = meta.Category
			si.Icon = meta.Icon
		}
		res = append(res, si)
	}
	return res
}

// listProcesses 非 Linux 平台的降级:用 gopsutil 列进程,让 UI 有真实数据可看。
func listProcesses() []ServiceInfo {
	procs, err := process.Processes()
	if err != nil {
		return nil
	}
	var res []ServiceInfo
	for _, p := range procs {
		name, _ := p.Name()
		status, _ := p.Status()
		cpuP, _ := p.CPUPercent()
		memP, _ := p.MemoryPercent()
		res = append(res, ServiceInfo{
			ID:          strconv.Itoa(int(p.Pid)),
			Name:        name,
			Status:      strings.ToLower(strings.Join(status, ",")),
			Description: "进程(demo 降级)",
			IsProcess:   true,
			PID:         p.Pid,
			CPUPercent:  cpuP,
			MemPercent:  memP,
		})
		if meta, ok := recognizeProc(name); ok {
			res[len(res)-1].Recognized = meta.Label
			res[len(res)-1].Category = meta.Category
			res[len(res)-1].Icon = meta.Icon
		}
	}
	// 按 CPU 占用降序,取前 50,避免列表过长
	top := res
	if len(top) > 50 {
		top = top[:50]
	}
	return top
}

// ServiceAction 对指定单元执行 start/stop/restart —— 把运维命令变成可视化按钮。
func ServiceAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID     string `json:"id"`
		Action string `json:"action"` // start | stop | restart
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body"})
		return
	}
	if runtime.GOOS != "linux" {
		WriteJSON(w, map[string]any{"ok": false, "error": "服务启停仅支持 Linux / systemd(当前 " + runtime.GOOS + ")"})
		return
	}
	switch body.Action {
	case "start", "stop", "restart":
	default:
		WriteJSON(w, map[string]any{"ok": false, "error": "action 必须是 start/stop/restart"})
		return
	}
	cmd := exec.Command("systemctl", body.Action, body.ID)
	out, err := cmd.CombinedOutput()
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": strings.TrimSpace(string(out))})
		return
	}
	WriteJSON(w, map[string]any{"ok": true, "action": body.Action, "id": body.ID})
}
