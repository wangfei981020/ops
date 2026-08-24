// Package config 只做一件事：把环境变量读成配置，并在启动时就否决不合法的组合。
//
// 宁可起不来也不要带着坏配置跑 —— 加密密钥用了默认值这种事，
// 等到第一次存凭据才发现就晚了（已经用弱密钥加密过的数据得重来）。
package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

type Config struct {
	Port int
	// MetricsPort 健康检查与指标。与业务端口分开，见 main.go 的说明
	MetricsPort int

	// 数据库连接拆成独立字段，DSN 由 DSN() 拼。
	//
	// 拆开不只是为了好填 —— 更重要的是**密码里可以有任意字符**：
	// 手写 DSN 时 user:pass@tcp(...) 这种格式里，密码含 @ # : / 会让解析出错，
	// 而报错信息指向「DSN 格式不对」，没人会联想到是密码里的一个字符。
	// 用 mysql.Config 构造则由驱动自己处理转义。
	MySQLHost     string
	MySQLPort     int
	MySQLUser     string
	MySQLPassword string
	MySQLDatabase string

	// EncryptKey 加密组织凭据（Rancher/Kite 的密码与 token）。
	// 🔴 没有默认值：一旦给了默认值，多半就有人用默认值上生产，
	//    那等于凭据是明文存的。
	EncryptKey string

	// JWTSecret 会话签名。同上，不给默认值。
	JWTSecret string

	// CollectInterval 定时采集周期。我方 Kite 走内网可以密一点，
	// 对方走公网别太频繁 —— 采集本身对目标集群 apiserver 是有负载的。
	CollectInterval time.Duration

	// LogLevel debug | info | warn | error。
	//
	// 接新数据源时开 debug 能看到每次请求的 URL、状态码、匹配到的 ns、解析出的 key；
	// 跑顺了改回 info。**改这个不需要重新构建镜像**，改 Secret 重启即可。
	LogLevel string

	// SuperUser 首次启动时创建的本地超管。
	// SSO 接上之前这是唯一入口；SSO 接上后也**必须保留**——
	// SSO 挂了的时候，本地账号是唯一的逃生通道。
	SuperUser     string
	SuperPassword string
}

func Load() (*Config, error) {
	c := &Config{
		Port:            envInt("PORT", 8080),
		MetricsPort:     envInt("METRICS_PORT", 8088),
		MySQLHost:       os.Getenv("MYSQL_HOST"),
		MySQLPort:       envInt("MYSQL_PORT", 3306),
		MySQLUser:       os.Getenv("MYSQL_USER"),
		MySQLPassword:   os.Getenv("MYSQL_PASSWORD"),
		MySQLDatabase:   envStr("MYSQL_DATABASE", "ops_version"),
		EncryptKey:      os.Getenv("ENCRYPT_KEY"),
		JWTSecret:       os.Getenv("JWT_SECRET"),
		CollectInterval: time.Duration(envInt("COLLECT_INTERVAL_MIN", 30)) * time.Minute,
		LogLevel:        envStr("LOG_LEVEL", "info"),
		SuperUser:       envStr("SUPER_USER", "admin"),
		SuperPassword:   os.Getenv("SUPER_PASSWORD"),
	}

	var missing []string
	if strings.TrimSpace(c.MySQLHost) == "" {
		missing = append(missing, "MYSQL_HOST")
	}
	if strings.TrimSpace(c.MySQLUser) == "" {
		missing = append(missing, "MYSQL_USER")
	}
	if c.MySQLPassword == "" {
		missing = append(missing, "MYSQL_PASSWORD")
	}
	if len(c.EncryptKey) < 16 {
		missing = append(missing, "ENCRYPT_KEY（至少 16 字符，用于加密组织凭据）")
	}
	if len(c.JWTSecret) < 16 {
		missing = append(missing, "JWT_SECRET（至少 16 字符）")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("缺少必需配置: %s", strings.Join(missing, ", "))
	}

	// 采集周期兜底。设成 1 分钟去打对方公网 Rancher 是在给对方制造负载，
	// 而版本这种东西本来也不会分钟级变化。
	if c.CollectInterval < 5*time.Minute {
		c.CollectInterval = 5 * time.Minute
	}
	return c, nil
}

// DSN 拼出 go-sql-driver 格式的连接串。
//
// 用 mysql.Config 而不是字符串拼接：密码里的 @ : / # 等字符由驱动负责转义，
// 手拼的话这些字符会让 DSN 解析出错，且报错指向格式而非密码，极难联想。
func (c *Config) DSN() string {
	m := mysql.NewConfig()
	m.User = c.MySQLUser
	m.Passwd = c.MySQLPassword
	m.Net = "tcp"
	m.Addr = net.JoinHostPort(c.MySQLHost, strconv.Itoa(c.MySQLPort))
	m.DBName = c.MySQLDatabase
	// 时间字段直接扫进 time.Time，否则得在每个 Scan 处手动解析
	m.ParseTime = true
	m.Loc = time.Local

	// 🔴 **把会话时区也钉成和应用一样**，否则库里会存在两种时间基准。
	//
	// `m.Loc` 只管**读**（DATETIME 按这个时区解释）。写入却有两条路：
	//
	//	Go 传 time.Time  → 驱动按 m.Loc 转换 → 存的是应用时区的墙钟
	//	SQL 里的 NOW() / DEFAULT CURRENT_TIMESTAMP → 用 **MySQL 服务端时区**
	//
	// 两者不一致时，同一次操作写的两个字段会差整整几个小时，
	// 而读出来都按 m.Loc 解释 —— 于是一半的时间是对的、一半早了 8 小时，
	// 界面上看不出哪个才对（实测过：orgs.sync_at=09:36:34 而
	// org_envs.last_collect_at=01:36:34，秒数相同、差 8 小时，）。
	//
	// ⚠️ 不能逐个把 NOW() 改成 Go 传参：全项目 39 处，改不全必漏，
	//    而 `DEFAULT CURRENT_TIMESTAMP` 那种列默认值**根本改不掉**。
	//    钉会话时区是唯一能覆盖全部写入路径的做法。
	//
	// ⚠️ 也不能写死 '+08:00'：容器 TZ 未必是 CST。取应用当前时区的实际偏移，
	//    这样无论容器设成什么，两边永远一致。
	m.Params = map[string]string{
		"charset":   "utf8mb4",
		"time_zone": "'" + localTZOffset() + "'",
	}
	// 卡死的查询会超时释放连接，而不是把连接池占满 ——
	// 连接池被占满时整站会一起瘫，且症状是「所有接口都卡」，看不出根因在数据库
	m.ReadTimeout = 30 * time.Second
	m.WriteTimeout = 30 * time.Second
	m.Timeout = 10 * time.Second
	return m.FormatDSN()
}

func envStr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// envBool 读布尔环境变量。只认明确的真值，其余（含拼错）一律 false ——
// ⚠️ 这个开关会重置超管密码，拼错时宁可不生效，也不能"看着像开了就开"。
func envBool(k string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(k))) {
	case "":
		return def
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// localTZOffset 取应用当前时区相对 UTC 的偏移，格式 ±HH:MM（MySQL time_zone 要的形状）。
//
// ⚠️ 用**当前时刻**的偏移而不是固定值：有夏令时的地区偏移会变，
// 而进程启动时算一次就够了 —— 连接是长连接，跨夏令时切换的场景
// 远比"两种时间基准并存"罕见，且重启即修正。
func localTZOffset() string {
	_, off := time.Now().Zone()
	sign := "+"
	if off < 0 {
		sign = "-"
		off = -off
	}
	return fmt.Sprintf("%s%02d:%02d", sign, off/3600, (off%3600)/60)
}
