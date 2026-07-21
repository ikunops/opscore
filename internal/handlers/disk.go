package handlers

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
)

// DirEntry 描述挂载点下的一个顶层条目(目录或文件)。
type DirEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Size  uint64 `json:"size"`
	IsDir bool   `json:"isDir"`
}

// DiskChildrenResp 是某个磁盘挂载点下"可下钻"的返回结构。
type DiskChildrenResp struct {
	Root        string     `json:"root"`
	Total       uint64     `json:"total"`
	Used        uint64     `json:"used"`
	UsedPercent float64    `json:"usedPercent"`
	Children    []DirEntry `json:"children"`
	Partial     bool       `json:"partial"` // 是否因超时/权限未扫全
}

// DiskChildren 处理 GET /api/core/disk/children?path=<挂载点>
// 返回该挂载点的总容量 + 顶层子目录/文件大小,供前端点击下钻。
func DiskChildren(w http.ResponseWriter, r *http.Request) {
	root := r.URL.Query().Get("path")
	if root == "" {
		WriteJSON(w, map[string]any{"error": "missing path"})
		return
	}
	// Windows 盘符形如 `C:` 会被 Go 解释为"该盘的当前工作目录",
	// 需补成 `C:\` 才能读到盘根(否则下钻内容其实是程序 CWD)。
	if len(root) == 2 && root[1] == ':' {
		root = root + `\`
	}

	// 禁止下钻虚拟文件系统：这些路径不是真实磁盘，扫出来会得到天文数字。
	if isVirtualMount(root) {
		WriteJSON(w, map[string]any{"error": "虚拟文件系统不可下钻", "root": root})
		return
	}

	dc := DiskChildrenResp{Root: root}
	if u, err := disk.Usage(root); err == nil {
		dc.Total = u.Total
		dc.Used = u.Used
		dc.UsedPercent = u.UsedPercent
	}

	// 用上下文超时管控遍历,避免大目录(如 Windows 的 C:\Windows)卡死请求。
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	entries, err := os.ReadDir(root)
	if err != nil {
		WriteJSON(w, map[string]any{"error": err.Error(), "root": root})
		return
	}

	// 排除虚拟/特殊文件系统,避免把/proc、/sys等当成真实磁盘占用
	virtualDirs := map[string]bool{
		"proc": true, "sys": true, "dev": true, "run": true,
		"snap": true, "boot": true, "mnt": true, "media": true,
	}

	// 对每个子目录单独调用 du -s 获取大小（比 filepath.WalkDir 快）
	// 文件大小直接用 os.Stat，无需 du
	var collected []DirEntry
	for _, e := range entries {
		if e.IsDir() && virtualDirs[e.Name()] {
			continue
		}
		p := filepath.Join(root, e.Name())
		if e.IsDir() {
			size, err := duSize(ctx, p)
			if err != nil {
				dc.Partial = true
				continue
			}
			collected = append(collected, DirEntry{Name: e.Name(), Path: p, Size: size, IsDir: true})
		} else if fi, ferr := e.Info(); ferr == nil {
			collected = append(collected, DirEntry{Name: e.Name(), Path: p, Size: uint64(fi.Size()), IsDir: false})
		}
	}

	sort.Slice(collected, func(i, j int) bool { return collected[i].Size > collected[j].Size })
	if len(collected) > 60 {
		collected = collected[:60]
	}
	dc.Children = collected
	WriteJSON(w, dc)
}

// duSize 调用 du -s 获取单个目录的总大小（字节），比 WalkDir 快。
// 若 du 因权限/环境失败，回退到 filepath.WalkDir（兼容非 root 用户）。
func duSize(ctx context.Context, dir string) (uint64, error) {
	cmd := exec.CommandContext(ctx, "du", "-s", dir)
	out, err := cmd.Output()
	if err == nil {
		parts := strings.Fields(string(out))
		if len(parts) >= 2 {
			return parseDuSize(parts[0]), nil
		}
	}
	// du 失败（权限不足、命令不存在等），回退到 Go 遍历
	return dirSizeWalk(ctx, dir)
}

// dirSizeWalk 用 filepath.WalkDir 计算目录总大小（回退方案）。
func dirSizeWalk(ctx context.Context, root string) (uint64, error) {
	var total uint64
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // 跳过无权访问的子树
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			return nil
		}
		if fi, e := d.Info(); e == nil {
			total += uint64(fi.Size())
		}
		return nil
	})
	return total, nil
}

// parseDuSize 解析 du 输出的文件大小（支持 K/M/G/T 等单位，默认 1K 块）。
func parseDuSize(s string) uint64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	mult := uint64(1024) // du 默认单位是 1K 块
	if last := s[len(s)-1]; last < '0' || last > '9' {
		switch strings.ToLower(string(last)) {
		case "k":
			mult = 1024
		case "m":
			mult = 1024 * 1024
		case "g":
			mult = 1024 * 1024 * 1024
		case "t":
			mult = 1024 * 1024 * 1024 * 1024
		default:
			return 0
		}
		s = s[:len(s)-1]
	}
	var size uint64
	for _, c := range s {
		if c >= '0' && c <= '9' {
			size = size*10 + uint64(c-'0')
		}
	}
	return size * mult // du 默认 1K 块，mult 已含 1024，直接得字节
}

// isVirtualMount 判断给定路径是否属于 Linux 虚拟/伪文件系统，不应被下钻扫描。
func isVirtualMount(path string) bool {
	// 统一小写并处理 Windows 路径分隔符，便于前缀匹配。
	p := strings.ToLower(filepath.ToSlash(path))
	virtualRoots := []string{
		"/proc", "/sys", "/dev", "/run", "/boot/efi",
	}
	for _, root := range virtualRoots {
		if p == root || strings.HasPrefix(p, root+"/") {
			return true
		}
	}
	return false
}
