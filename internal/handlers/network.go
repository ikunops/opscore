package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

// Network 返回网络接口、流量统计与监听端口。
func Network(w http.ResponseWriter, r *http.Request) {
	type Iface struct {
		Name  string   `json:"name"`
		MTU   int      `json:"mtu"`
		Flags []string `json:"flags"`
		Addrs []string `json:"addrs"`
	}
	var ifaces []Iface
	if il, err := net.Interfaces(); err == nil {
		for _, i := range il {
			var addrs []string
			for _, a := range i.Addrs {
				addrs = append(addrs, a.Addr)
			}
			ifaces = append(ifaces, Iface{Name: i.Name, MTU: i.MTU, Flags: i.Flags, Addrs: addrs})
		}
	}

	type Listen struct {
		Protocol string `json:"protocol"`
		Local    string `json:"local"`
		Port     int    `json:"port"`
		PID      int32  `json:"pid"`
		// 身份以"实际占用进程"为准,端口常见服务仅作提示
		Process  string `json:"process"`  // 真实占用进程名(事实来源)
		Service  string `json:"service"`  // 由进程名识别出的服务(已确认身份)
		Category string `json:"category"` // 服务分类
		Icon     string `json:"icon"`     // 服务图标
		KnownAs  string `json:"knownAs"`  // 该端口"常见服务"提示(仅供参考)
		Verified bool   `json:"verified"` // 端口提示与进程身份一致 → 已确认
	}
	var listens []Listen
	if conns, err := net.Connections("all"); err == nil {
		for _, c := range conns {
			if !strings.EqualFold(c.Status, "listen") {
				continue
			}
			port := int(c.Laddr.Port)
			local := c.Laddr.IP + ":" + strconv.Itoa(port)
			// gopsutil v4 中 Type 是套接字类型常量(1=TCP,2=UDP)
			protocol := "TCP"
			if c.Type == 2 {
				protocol = "UDP"
			}
			li := Listen{Protocol: protocol, Local: local, Port: port, PID: c.Pid}

			// 端口常见服务提示(仅供参考,绝不作结论)
			if hint, ok := recognizePort(uint16(port)); ok {
				li.KnownAs = hint.Label
			}

			// 关键:用 PID 反查真实进程名作为身份依据,再确认是否与端口提示相符
			if c.Pid > 0 {
				if p, perr := process.NewProcess(c.Pid); perr == nil {
					if nm, nerr := p.Name(); nerr == nil {
						li.Process = nm
						if meta, ok := recognizeProc(nm); ok {
							li.Service = meta.Label
							li.Category = meta.Category
							li.Icon = meta.Icon
							// 只有"端口提示"与"进程识别出的服务"一致,才标已确认
							if li.KnownAs != "" && (meta.Label == li.KnownAs || meta.Category == categoryOfPort(uint16(port))) {
								li.Verified = true
							}
						}
					}
				}
			}
			listens = append(listens, li)
		}
	}

	WriteJSON(w, map[string]any{"interfaces": ifaces, "listeners": listens})
}
