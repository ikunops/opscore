package handlers

import (
	"bufio"
	"errors"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
)

// LogEntry 是一行日志。
type LogEntry struct {
	Line string `json:"line"`
	Num  int    `json:"num"`
}

// LogResponse 日志查看响应。
type LogResponse struct {
	Source   string     `json:"source"`    // "journalctl" | "file"
	Target   string     `json:"target"`    // unit 名或文件路径
	 Lines   []LogEntry `json:"lines"`
	Total    int        `json:"total"`     // 实际返回行数
	Warnings []string   `json:"warnings,omitempty"`
}

// ServiceLogsHandler 返回指定服务的日志。
// GET /api/core/services/logs?source=journalctl&target=<unit>&lines=100
// GET /api/core/services/logs?source=file&path=<logfile>&lines=100
// 有 filter 参数时: 用 journalctl -g / grep 搜全部历史
func ServiceLogsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	source := r.URL.Query().Get("source")
	target := r.URL.Query().Get("target")
	path := r.URL.Query().Get("path")
	linesStr := r.URL.Query().Get("lines")
	filter := r.URL.Query().Get("filter")
	if linesStr == "" {
		linesStr = "100"
	}
	lines, _ := strconv.Atoi(linesStr)
	if lines <= 0 || lines > 500 {
		lines = 100
	}

	var resp LogResponse
	var err error

	switch source {
	case "journalctl", "":
		if target == "" {
			target = r.URL.Query().Get("unit")
		}
		if target == "" {
			WriteJSON(w, LogResponse{Source: "journalctl", Warnings: []string{"缺少 target/unit 参数"}})
			return
		}
		if filter != "" {
			resp, err = readJournalctlGrep(target, filter, lines)
		} else {
			resp, err = readJournalctl(target, lines)
		}
		resp.Source = "journalctl"
		resp.Target = target
	case "file":
		if path == "" {
			WriteJSON(w, LogResponse{Source: "file", Warnings: []string{"缺少 path 参数"}})
			return
		}
		if filter != "" {
			resp, err = readFileGrep(path, filter)
		} else {
			resp, err = readFileLog(path, lines)
		}
		resp.Source = "file"
		resp.Target = path
	default:
		WriteJSON(w, LogResponse{Warnings: []string{"不支持的 source: " + source}})
		return
	}

	if err != nil {
		resp.Warnings = append(resp.Warnings, err.Error())
	}
	WriteJSON(w, resp)
}

// readJournalctl 执行 journalctl -u <unit> -n <lines>。
func readJournalctl(unit string, lines int) (LogResponse, error) {
	cmd := exec.Command("journalctl", "-u", unit, "-n", strconv.Itoa(lines), "--no-pager", "--output=short-iso", "--reverse")
	out, err := cmd.CombinedOutput()
	if err != nil {
		// 权限不足:当前用户无 journal 读权限,给出可读提示而非裸 exit status。
		msg := string(out)
		if strings.Contains(msg, "insufficient permissions") ||
			strings.Contains(msg, "Not permitted") ||
			strings.Contains(msg, "Failed to search") {
			return LogResponse{}, errors.New("当前用户无 journal 读权限:请将运行用户加入 systemd-journal 组,或以 root 运行")
		}
		if strings.Contains(msg, "Couldn't find") || strings.Contains(msg, "No such") {
			return LogResponse{}, errors.New("未找到 unit「" + unit + "」,请确认服务名是否正确")
		}
		return LogResponse{}, errors.New("journalctl 执行失败: " + strings.TrimSpace(msg))
	}
	return parseLines(string(out)), nil
}

// readJournalctlGrep 用 journalctl -g 在全部历史中 grep。
func readJournalctlGrep(unit, filter string, lines int) (LogResponse, error) {
	cmd := exec.Command("journalctl", "-u", unit, "-g", filter, "-n", strconv.Itoa(lines), "--no-pager", "--output=short-iso", "--reverse")
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := string(out)
		if strings.Contains(msg, "insufficient permissions") ||
			strings.Contains(msg, "Not permitted") ||
			strings.Contains(msg, "Failed to search") {
			return LogResponse{}, errors.New("当前用户无 journal 读权限:请将运行用户加入 systemd-journal 组,或以 root 运行")
		}
		if strings.Contains(msg, "Couldn't find") || strings.Contains(msg, "No such") {
			return LogResponse{}, errors.New("未找到 unit「" + unit + "」,请确认服务名是否正确")
		}
		return LogResponse{}, errors.New("journalctl -g 执行失败: " + strings.TrimSpace(msg))
	}
	return parseLines(string(out)), nil
}

// readFileLog 读取日志文件末尾 <lines> 行。
func readFileLog(path string, lines int) (LogResponse, error) {
	if !strings.HasPrefix(path, "/var/log/") {
		return LogResponse{}, errors.New("只允许读取 /var/log/ 下的日志文件")
	}
	cmd := exec.Command("tail", "-n", strconv.Itoa(lines), path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return LogResponse{}, err
	}
	return parseLines(string(out)), nil
}

// readFileGrep 用 grep -i 在全部文件中搜索。
func readFileGrep(path, filter string) (LogResponse, error) {
	if !strings.HasPrefix(path, "/var/log/") {
		return LogResponse{}, errors.New("只允许读取 /var/log/ 下的日志文件")
	}
	cmd := exec.Command("grep", "-i", "--color=never", filter, path)
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		// grep 无匹配时返回 exit 1，不算错误
		return LogResponse{Lines: []LogEntry{}, Total: 0}, nil
	}
	return parseLines(string(out)), nil
}

// parseLines 将原始文本按行解析为 LogEntry 列表。
func parseLines(text string) LogResponse {
	scanner := bufio.NewScanner(strings.NewReader(text))
	var entries []LogEntry
	num := 0
	for scanner.Scan() {
		num++
		entries = append(entries, LogEntry{Line: scanner.Text(), Num: num})
	}
	return LogResponse{Lines: entries, Total: len(entries)}
}
