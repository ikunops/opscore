package handlers

import (
	"context"
	"net/http"
	"os"
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
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	entries, err := os.ReadDir(root)
	if err != nil {
		WriteJSON(w, map[string]any{"error": err.Error(), "root": root})
		return
	}

	var collected []DirEntry
	for _, e := range entries {
		p := filepath.Join(root, e.Name())
		if e.IsDir() {
			size, ok := dirSize(ctx, p)
			if !ok {
				dc.Partial = true
				continue // 超时或被拒,跳过该目录
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

// dirSize 计算目录总大小,支持 ctx 取消(超时/遍历过深时立即停)。
// 返回 (size, ok):ok=false 表示中途被取消或权限不足导致未扫全。
func dirSize(ctx context.Context, root string) (uint64, bool) {
	var total uint64
	done := true
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // 跳过无权访问的子树
		}
		if ctx.Err() != nil {
			done = false
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
	return total, done
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
