package database

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	_ "github.com/go-sql-driver/mysql"

	"ops-version-backend/database/migrations"

	"ops-version-backend/logx"
)

// Open 连接 MySQL（库需预先创建），然后自动跑 migration 建表。
func Open(dsn string) (*sql.DB, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse DSN: %w", err)
	}
	if cfg.DBName == "" {
		return nil, fmt.Errorf("DSN missing dbname")
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	// 2026-07-31 故障（CMDB-012）里，10 条连接被挂死的查询占满后，后续每一个查库
	// 请求都卡在「等空闲连接」上，整站瘫痪。根治手段是 DSN 的 readTimeout（卡死的
	// 查询会超时释放连接，见 config.buildDSN），连接池只是把耐受度抬高一档：
	// 采集协程 + HTTP 请求 + cron 三路并发，10 条在正常负载下也偏紧。
	// 上限 25 是按「这台 MySQL 由 5 个系统共用、max_connections 默认 151」留的余量。
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err := db.Ping(); err != nil {
		if strings.Contains(err.Error(), "Unknown database") {
			_ = db.Close()
			return nil, fmt.Errorf("database %q does not exist; create it first", cfg.DBName)
		}
		return nil, err
	}
	logx.Line("db", fmt.Sprintf("MySQL connected (db=%s)", cfg.DBName))

	if err := migrations.Run(db); err != nil {
		return nil, fmt.Errorf("run migrations: %w", err)
	}
	return db, nil
}
