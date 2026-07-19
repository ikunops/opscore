package handlers

import "strings"

// SvcMeta 是对一个"已知服务"的展示元数据。
type SvcMeta struct {
	Label    string `json:"label"`
	Category string `json:"category"`
	Icon     string `json:"icon"`
}

// knownByProc 按"进程名 / systemd 单元名"识别常见服务。
// 关键:进程名或单元名本身就是事实来源(systemd 单元、/proc 进程表),
// 不像端口那样可以被任意进程占用,因此可以安全地直接标注。
var knownByProc = map[string]SvcMeta{
	"mysql":         {Label: "MySQL", Category: "数据库", Icon: "🐬"},
	"mysqld":        {Label: "MySQL", Category: "数据库", Icon: "🐬"},
	"mariadbd":      {Label: "MariaDB", Category: "数据库", Icon: "🐬"},
	"mariadb":       {Label: "MariaDB", Category: "数据库", Icon: "🐬"},
	"postgres":      {Label: "PostgreSQL", Category: "数据库", Icon: "🐘"},
	"postmaster":    {Label: "PostgreSQL", Category: "数据库", Icon: "🐘"},
	"redis":         {Label: "Redis", Category: "缓存", Icon: "🔴"},
	"redis-server":  {Label: "Redis", Category: "缓存", Icon: "🔴"},
	"mongod":        {Label: "MongoDB", Category: "数据库", Icon: "🍃"},
	"nginx":         {Label: "Nginx", Category: "Web 服务器", Icon: "🌐"},
	"apache2":       {Label: "Apache", Category: "Web 服务器", Icon: "🌐"},
	"httpd":         {Label: "Apache", Category: "Web 服务器", Icon: "🌐"},
	"sshd":          {Label: "OpenSSH", Category: "远程访问", Icon: "🔑"},
	"dockerd":       {Label: "Docker", Category: "容器", Icon: "🐳"},
	"containerd":    {Label: "containerd", Category: "容器", Icon: "🐳"},
	"kubelet":       {Label: "Kubelet", Category: "容器编排", Icon: "☸️"},
	"rabbitmq":      {Label: "RabbitMQ", Category: "消息队列", Icon: "🐰"},
	"elasticsearch": {Label: "Elasticsearch", Category: "搜索", Icon: "🔍"},
	"memcached":     {Label: "Memcached", Category: "缓存", Icon: "🟠"},
	"influxd":       {Label: "InfluxDB", Category: "数据库", Icon: "📈"},
	"etcd":          {Label: "etcd", Category: "协调服务", Icon: "🗄️"},
	"consul":        {Label: "Consul", Category: "协调服务", Icon: "🗂️"},
	"prometheus":    {Label: "Prometheus", Category: "监控", Icon: "🔥"},
	"grafana":       {Label: "Grafana", Category: "监控", Icon: "📊"},
	"alertmanager":  {Label: "Alertmanager", Category: "监控", Icon: "🔔"},
	"php-fpm":       {Label: "PHP-FPM", Category: "Web", Icon: "🐘"},
}

// knownByPort 仅作"常见端口"提示,绝不作为身份结论。
// 真正的身份必须以占用端口的进程(PID→进程名)为准,二者一致才标"已确认"。
var knownByPort = map[uint16]SvcMeta{
	22:    {Label: "SSH", Category: "远程访问", Icon: "🔑"},
	53:    {Label: "DNS", Category: "网络", Icon: "🌍"},
	25:    {Label: "SMTP", Category: "邮件", Icon: "✉️"},
	80:    {Label: "HTTP", Category: "Web", Icon: "🌐"},
	443:   {Label: "HTTPS", Category: "Web", Icon: "🔒"},
	3306:  {Label: "MySQL", Category: "数据库", Icon: "🐬"},
	5432:  {Label: "PostgreSQL", Category: "数据库", Icon: "🐘"},
	6379:  {Label: "Redis", Category: "缓存", Icon: "🔴"},
	27017: {Label: "MongoDB", Category: "数据库", Icon: "🍃"},
	8080:  {Label: "HTTP-Alt", Category: "Web", Icon: "🌐"},
	8443:  {Label: "HTTPS-Alt", Category: "Web", Icon: "🔒"},
	9000:  {Label: "PHP-FPM", Category: "Web", Icon: "🐘"},
	9090:  {Label: "Prometheus", Category: "监控", Icon: "🔥"},
	9092:  {Label: "Kafka", Category: "消息队列", Icon: "📨"},
	9093:  {Label: "Alertmanager", Category: "监控", Icon: "🔔"},
	9200:  {Label: "Elasticsearch", Category: "搜索", Icon: "🔍"},
	11211: {Label: "Memcached", Category: "缓存", Icon: "🟠"},
	5672:  {Label: "RabbitMQ", Category: "消息队列", Icon: "🐰"},
	2181:  {Label: "ZooKeeper", Category: "协调服务", Icon: "🐘"},
	2379:  {Label: "etcd", Category: "协调服务", Icon: "🗄️"},
	8500:  {Label: "Consul", Category: "协调服务", Icon: "🗂️"},
}

// recognizeProc 根据进程名 / 单元名识别常见服务。
// 依次尝试:精确 → 前缀 → 子串(最后兜底)。返回识别到的元数据与是否命中。
func recognizeProc(raw string) (SvcMeta, bool) {
	n := strings.ToLower(raw)
	n = strings.TrimSuffix(n, ".service")
	n = strings.TrimSuffix(n, ".socket")
	if m, ok := knownByProc[n]; ok {
		return m, true
	}
	for k, m := range knownByProc {
		if strings.HasPrefix(n, k) {
			return m, true
		}
	}
	for k, m := range knownByProc {
		if strings.Contains(n, k) {
			return m, true
		}
	}
	return SvcMeta{}, false
}

// recognizePort 仅返回端口对应的"常见服务"提示,不表示身份已确认。
func recognizePort(port uint16) (SvcMeta, bool) {
	m, ok := knownByPort[port]
	return m, ok
}

// categoryOfPort 返回端口对应的"常见服务"分类(用于与进程识别结果交叉验证)。
func categoryOfPort(port uint16) string {
	if m, ok := knownByPort[port]; ok {
		return m.Category
	}
	return ""
}
