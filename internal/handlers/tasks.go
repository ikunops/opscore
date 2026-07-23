package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

func CrontabHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		user := r.URL.Query().Get("user")
		if user == "" {
			user = "root"
		}
		if !isRoot() {
			u := os.Getenv("USER")
			if user != u {
				WriteJSON(w, map[string]any{"error": "非 root 只能查看自己的 crontab", "permission": "user"})
				return
			}
			user = u
		}
		cmd := exec.Command("crontab", "-l", "-u", user)
		out, _ := cmd.CombinedOutput()
		WriteJSON(w, map[string]any{"content": string(out), "permission": permLabel()})

	case "POST":
		if !isRoot() {
			WriteJSON(w, map[string]any{"error": "需要 root 权限修改 crontab", "permission": "user"})
			return
		}
		var body struct {
			User    string `json:"user"`
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			WriteJSON(w, map[string]any{"error": "请求格式错误", "permission": "root"})
			return
		}
		if body.User == "" {
			body.User = "root"
		}
		cmd := exec.Command("crontab", "-u", body.User, "-")
		cmd.Stdin = strings.NewReader(body.Content)
		out, err := cmd.CombinedOutput()
		resp := map[string]any{"permission": "root"}
		if err != nil {
			resp["error"] = err.Error()
			resp["output"] = string(out)
		} else {
			resp["ok"] = true
		}
		WriteJSON(w, resp)

	default:
		http.Error(w, "method not allowed", 405)
	}
}

func DisksHandler(w http.ResponseWriter, r *http.Request) {
	lsblk := runCapture("lsblk", "-o", "NAME,SIZE,TYPE,FSTYPE,MOUNTPOINT,MODEL")
	mounts := runCapture("mount")
	df := runCapture("df", "-h")
	WriteJSON(w, map[string]any{
		"lsblk":      lsblk,
		"mounts":     mounts,
		"df":         df,
		"permission": permLabel(),
	})
}

func DiskActionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !isRoot() {
		WriteJSON(w, map[string]any{"error": "需要 root 权限", "permission": "user"})
		return
	}
	var body struct {
		Action     string `json:"action"`
		Device     string `json:"device"`
		Mountpoint string `json:"mountpoint"`
		Fstype     string `json:"fstype"`
		Options    string `json:"options"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteJSON(w, map[string]any{"error": "请求格式错误", "permission": "root"})
		return
	}

	var cmd *exec.Cmd
	switch body.Action {
	case "mount":
		args := []string{body.Device, body.Mountpoint}
		if body.Fstype != "" {
			args = append([]string{"-t", body.Fstype}, args...)
		}
		if body.Options != "" {
			args = append([]string{"-o", body.Options}, args...)
		}
		cmd = exec.Command("mount", args...)
	case "umount":
		target := body.Mountpoint
		if target == "" {
			target = body.Device
		}
		cmd = exec.Command("umount", target)
	case "smart":
		dev := body.Device
		if dev == "" {
			WriteJSON(w, map[string]any{"error": "缺少 device", "permission": "root"})
			return
		}
		if !strings.HasPrefix(dev, "/dev/") {
			dev = "/dev/" + dev
		}
		if _, err := os.Stat(dev); os.IsNotExist(err) {
			WriteJSON(w, map[string]any{"error": "设备不存在 " + dev, "permission": "root"})
			return
		}
		out := runCapture("smartctl", "-a", dev)
		WriteJSON(w, map[string]any{"output": out, "permission": "root"})
		return
	default:
		WriteJSON(w, map[string]any{"error": "未知操作: " + body.Action, "permission": "root"})
		return
	}

	out, err := cmd.CombinedOutput()
	resp := map[string]any{"permission": "root"}
	if err != nil {
		resp["error"] = err.Error()
		resp["output"] = string(out)
	} else {
		resp["ok"] = true
		resp["output"] = string(out)
	}
	WriteJSON(w, resp)
}

func runCapture(name string, args ...string) string {
	path, err := exec.LookPath(name)
	if err != nil {
		return "(" + name + " not found)"
	}
	cmd := exec.Command(path, args...)
	out, _ := cmd.CombinedOutput()
	return string(out)
}
