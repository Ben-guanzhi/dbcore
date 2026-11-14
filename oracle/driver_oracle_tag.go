//go:build oracle

package oracle

import (
	_ "github.com/sijms/go-ora/v2" // registers the "oracle" driver with database/sql
)

// 波 100 对齐（dbstudio 先例）：go-ora 单个驱动 ~13MB，故 Oracle 的 database/sql
// 驱动注册是构建可选项。方言（OracleDialect）、DSN 拼装（oracledsn）与
// NewFromDB（吃外部 *sql.DB）不依赖本文件——只有 Open/OpenSpec（经
// database/sql 自拨）需要 go-ora 已注册。客户端默认构建引 oracle 包拼 DSN、
// 造方言，driver 名 "oracle" 留给插件宿主注册。
