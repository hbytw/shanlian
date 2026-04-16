package repository

import (
	"database/sql"
	"fmt"
	"log"

	_ "github.com/lib/pq"
)

// PostgresDB PostgreSQL 数据库连接
type PostgresDB struct {
	*sql.DB
}

// NewPostgresDB 创建数据库连接
func NewPostgresDB(dsn string) (*PostgresDB, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库连接失败：%w", err)
	}

	// 设置连接池
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)

	// 验证连接
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("数据库 Ping 失败：%w", err)
	}

	log.Println("数据库连接池已建立")
	return &PostgresDB{DB: db}, nil
}

// Close 关闭数据库连接
func (db *PostgresDB) Close() error {
	if db.DB != nil {
		return db.DB.Close()
	}
	return nil
}
