package metrics

import (
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
)

// Snapshot 是一个时间点的全量系统指标快照。
// 后端用一个后台 goroutine 每 2 秒刷新一次,前端轮询读取,避免每次请求都阻塞采集。
type Snapshot struct {
	Timestamp int64      `json:"timestamp"`
	Host      HostInfo   `json:"host"`
	CPU       CPUInfo    `json:"cpu"`
	Memory    MemoryInfo `json:"memory"`
	Load      *load.AvgStat `json:"load,omitempty"`
	Disks     []DiskInfo `json:"disks"`
	Net       NetIO      `json:"net"`
}

type HostInfo struct {
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Platform string `json:"platform"`
	Uptime   uint64 `json:"uptime"`
}

type CPUInfo struct {
	Percent float64   `json:"percent"`
	PerCore []float64 `json:"perCore"`
	Cores   int       `json:"cores"`
	Model   string    `json:"model"`
}

type MemoryInfo struct {
	Total       uint64  `json:"total"`
	Used        uint64  `json:"used"`
	UsedPercent float64 `json:"usedPercent"`
	Free        uint64  `json:"free"`
	SwapTotal   uint64  `json:"swapTotal"`
	SwapUsed    uint64  `json:"swapUsed"`
	SwapPercent float64 `json:"swapPercent"`
}

type DiskInfo struct {
	Mountpoint  string  `json:"mountpoint"`
	Total       uint64  `json:"total"`
	Used        uint64  `json:"used"`
	UsedPercent float64 `json:"usedPercent"`
	Fstype      string  `json:"fstype"`
}

type NetIO struct {
	ByNic []NicIO `json:"byNic"`
}

type NicIO struct {
	Name    string `json:"name"`
	RxRate  uint64 `json:"rxRate"`
	TxRate  uint64 `json:"txRate"`
	RxTotal uint64 `json:"rxTotal"`
	TxTotal uint64 `json:"txTotal"`
}

var (
	mu      sync.RWMutex
	current Snapshot
	prevNet map[string]net.IOCountersStat
)

// Start 启动后台采集循环(非阻塞)。
func Start() {
	// 预热带量,使第一次 cpu.Percent(0,...) 有基准
	cpu.Percent(200*time.Millisecond, false)
	go loop()
}

func loop() {
	for {
		tick()
		time.Sleep(2 * time.Second)
	}
}

func tick() {
	s := Snapshot{Timestamp: time.Now().Unix()}

	if h, err := host.Info(); err == nil {
		s.Host = HostInfo{Hostname: h.Hostname, OS: h.OS, Platform: h.Platform, Uptime: h.Uptime}
	}
	if c, err := cpu.Percent(time.Second, false); err == nil && len(c) > 0 {
		s.CPU.Percent = c[0]
	}
	if pc, err := cpu.Percent(0, true); err == nil {
		s.CPU.PerCore = pc
	}
	if n, err := cpu.Counts(true); err == nil {
		s.CPU.Cores = n
	}
	if info, err := cpu.Info(); err == nil && len(info) > 0 {
		s.CPU.Model = info[0].ModelName
	}
	if v, err := mem.VirtualMemory(); err == nil {
		s.Memory = MemoryInfo{Total: v.Total, Used: v.Used, UsedPercent: v.UsedPercent, Free: v.Free}
	}
	if sw, err := mem.SwapMemory(); err == nil {
		s.Memory.SwapTotal = sw.Total
		s.Memory.SwapUsed = sw.Used
		s.Memory.SwapPercent = sw.UsedPercent
	}
	if la, err := load.Avg(); err == nil {
		s.Load = la
	}

	if parts, err := disk.Partitions(false); err == nil {
		for _, p := range parts {
			if shouldSkipDisk(p) {
				continue
			}
			if u, err := disk.Usage(p.Mountpoint); err == nil && u.Total > 0 && u.UsedPercent < 1000 {
				s.Disks = append(s.Disks, DiskInfo{
					Mountpoint:  p.Mountpoint,
					Total:       u.Total,
					Used:        u.Used,
					UsedPercent: u.UsedPercent,
					Fstype:      p.Fstype,
				})
			}
		}
	}

	if counters, err := net.IOCounters(true); err == nil {
		prev := prevNet
		cur := map[string]net.IOCountersStat{}
		for _, c := range counters {
			cur[c.Name] = c
			nic := NicIO{Name: c.Name, RxTotal: c.BytesRecv, TxTotal: c.BytesSent}
			if p, ok := prev[c.Name]; ok {
				nic.RxRate = subtract(c.BytesRecv, p.BytesRecv)
				nic.TxRate = subtract(c.BytesSent, p.BytesSent)
			}
			s.Net.ByNic = append(s.Net.ByNic, nic)
		}
		prevNet = cur
	}

	mu.Lock()
	current = s
	mu.Unlock()
}

func subtract(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return 0
}

// shouldSkipDisk 判断一个挂载点是否属于虚拟/伪文件系统，不应纳入磁盘统计。
// 同时按文件系统类型(Fstype)和挂载路径(Mountpoint)双重过滤，避免 /proc、/sys
// 等虚拟 FS 混入磁盘列表，也避免 docker 容器化环境里 overlay、aufs 等被误报。
func shouldSkipDisk(p disk.PartitionStat) bool {
	fstype := strings.ToLower(p.Fstype)
	switch fstype {
	case "", "proc", "procfs", "procfs2", "subfs", "sysfs", "sysfs2",
		"tmpfs", "devtmpfs", "devpts", "ramfs", "overlay", "aufs",
		"squashfs", "cgroup", "cgroup2", "securityfs", "debugfs",
		"pstore", "bpf", "fusectl", "hugetlbfs", "mqueue", "configfs",
		"tracefs", "rpc_pipefs", "nfsd", "fuse.gvfsd-fuse", "binfmt_misc",
		"efivarfs":
		return true
	}
	mp := strings.ToLower(p.Mountpoint)
	virtualRoots := []string{
		"/proc", "/sys", "/dev", "/run", "/boot/efi",
		"/var/lib/docker/", "/var/lib/containers/",
	}
	for _, root := range virtualRoots {
		if mp == root || strings.HasPrefix(mp, root+"/") {
			return true
		}
	}
	return false
}

// Get 返回最新的指标快照。
func Get() Snapshot {
	mu.RLock()
	defer mu.RUnlock()
	return current
}
