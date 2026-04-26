package main

import (
	"fmt"
	"log"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func openMySQLWithRetry(cfg AppConfig) (*gorm.DB, error) {
	retries := cfg.MySQLConnectRetries
	if retries < 1 {
		retries = 1
	}
	interval := time.Duration(cfg.MySQLConnectInterval) * time.Second
	if interval <= 0 {
		interval = 2 * time.Second
	}

	var lastErr error
	for attempt := 1; attempt <= retries; attempt++ {
		db, err := gorm.Open(mysql.Open(cfg.MySQLDSN), &gorm.Config{})
		if err == nil {
			logKV("info", "mysql_connected", map[string]any{"attempt": attempt})
			return db, nil
		}

		lastErr = err
		log.Printf("MySQL not ready yet, retrying: attempt=%d/%d err=%v", attempt, retries, err)
		if attempt < retries {
			time.Sleep(interval)
		}
	}

	return nil, fmt.Errorf("mysql connect failed after %d attempts: %w", retries, lastErr)
}
