package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func NetConfigHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		ifaces := runCapture("ip", "addr", "show")
		routes := runCapture("ip", "route", "show")
		dns := runCapture("resolvectl", "status")
		if strings.HasPrefix(dns, "(resolvectl not found)") || !strings.Contains(dns, "DNS") {
			dns = runCapture("cat", "/etc/resolv.conf")
		}
		nm := runCapture("nmcli", "-t", "dev", "status")
		WriteJSON(w, map[string]any{
			"interfaces": ifaces,
			"routes":     routes,
			"dns":        dns,
			"nm":         nm,
			"permission": permLabel(),
		})
		return
	}

	if !isRoot() {
		WriteJSON(w, map[string]any{"error": "需要 root 权限", "permission": "user"})
		return
	}

	var body struct {
		Action string `json:"action"`
		Device string `json:"device"`
		IP     string `json:"ip"`
		Mask   int    `json:"mask"`
		DNS    string `json:"dns"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteJSON(w, map[string]any{"error": "请求格式错误", "permission": "root"})
		return
	}

	var cmd *exec.Cmd
	switch body.Action {
	case "set-ip":
		if body.Device == "" || body.IP == "" {
			WriteJSON(w, map[string]any{"error": "缺少 device 或 ip", "permission": "root"})
			return
		}
		cidr := body.IP
		if body.Mask > 0 {
			cidr += "/" + strconv.Itoa(body.Mask)
		}
		if hasBin("nmcli") {
			cmd = exec.Command("nmcli", "connection", "modify", body.Device, "ipv4.addresses", cidr, "ipv4.method", "manual")
			cmd.Run()
			cmd = exec.Command("nmcli", "connection", "up", body.Device)
		} else {
			cmd = exec.Command("ip", "addr", "add", cidr, "dev", body.Device)
		}

	case "set-dns":
		if body.DNS == "" || body.Device == "" {
			WriteJSON(w, map[string]any{"error": "缺少 dns 或 device", "permission": "root"})
			return
		}
		if hasBin("nmcli") {
			cmd = exec.Command("nmcli", "connection", "modify", body.Device, "ipv4.dns", body.DNS)
			cmd.Run()
			cmd = exec.Command("nmcli", "connection", "up", body.Device)
		} else {
			f, err := os.Create("/etc/resolv.conf")
			if err != nil {
				WriteJSON(w, map[string]any{"error": "写 resolv.conf 失败: " + err.Error(), "permission": "root"})
				return
			}
			defer f.Close()
			f.WriteString("# managed by opscore\n")
			for _, ns := range strings.Fields(body.DNS) {
				f.WriteString("nameserver " + ns + "\n")
			}
			WriteJSON(w, map[string]any{"ok": true, "note": "直接写入 /etc/resolv.conf，可能被 systemd-resolved 覆盖", "permission": "root"})
			return
		}

	case "restart":
		if body.Device == "" {
			WriteJSON(w, map[string]any{"error": "缺少 device", "permission": "root"})
			return
		}
		exec.Command("ip", "link", "set", "dev", body.Device, "down").Run()
		exec.Command("ip", "link", "set", "dev", body.Device, "up").Run()
		WriteJSON(w, map[string]any{"ok": true, "note": "网卡已重启，如果无法连接请手动恢复", "permission": "root"})
		return

	case "dhcp":
		if body.Device == "" {
			WriteJSON(w, map[string]any{"error": "缺少 device", "permission": "root"})
			return
		}
		if hasBin("nmcli") {
			cmd = exec.Command("nmcli", "connection", "modify", body.Device, "ipv4.method", "auto")
			cmd.Run()
			cmd = exec.Command("nmcli", "connection", "up", body.Device)
		} else {
			cmd = exec.Command("dhclient", "-v", body.Device)
		}

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

func hasBin(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
