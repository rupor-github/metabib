package db

import (
	"fmt"
	"time"

	mysql "github.com/go-sql-driver/mysql"

	"metabib/config"
)

func DSN(cfg config.DatabaseConfig, withDatabase bool) (string, error) {
	if cfg.DSN != "" {
		mc, err := mysql.ParseDSN(cfg.DSN)
		if err != nil {
			return "", fmt.Errorf("parse database DSN: %w", err)
		}
		applyDriverDefaults(mc)
		if !withDatabase {
			mc.DBName = ""
		} else if mc.DBName == "" {
			mc.DBName = cfg.Name
		}
		return mc.FormatDSN(), nil
	}

	mc := mysql.NewConfig()
	mc.User = cfg.User
	mc.Passwd = cfg.Password
	mc.Net = cfg.Protocol
	if cfg.Protocol == "unix" {
		mc.Addr = cfg.Host
	} else {
		mc.Addr = fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	}
	if withDatabase {
		mc.DBName = cfg.Name
	}
	applyDriverDefaults(mc)
	return mc.FormatDSN(), nil
}

func applyDriverDefaults(mc *mysql.Config) {
	mc.ParseTime = true
	mc.MultiStatements = true
	mc.AllowNativePasswords = true
	mc.CheckConnLiveness = true
	if mc.Loc == nil {
		mc.Loc = time.Local
	}
	if mc.Timeout == 0 {
		mc.Timeout = 30 * time.Second
	}
	if mc.ReadTimeout == 0 {
		mc.ReadTimeout = 30 * time.Second
	}
	if mc.WriteTimeout == 0 {
		mc.WriteTimeout = 30 * time.Second
	}
	if mc.Collation == "" {
		mc.Collation = "utf8mb4_unicode_ci"
	}
	if mc.Params == nil {
		mc.Params = make(map[string]string)
	}
	if _, ok := mc.Params["charset"]; !ok {
		mc.Params["charset"] = "utf8mb4"
	}
	delete(mc.Params, "collation")
}

func dsnDatabaseName(dsn string) (string, error) {
	if dsn == "" {
		return "", nil
	}
	mc, err := mysql.ParseDSN(dsn)
	if err != nil {
		return "", fmt.Errorf("parse database DSN: %w", err)
	}
	return mc.DBName, nil
}
