package model

// 方言层：标识符引用、字面量、类型映射。
//
// 类型是方言中性的（建模工具的通用拼写）：
//
//	int   → 整数（MySQL INT / PG INTEGER / MSSQL INT）
//	long  → 长整数（BIGINT）
//	text  → 变长文本（VARCHAR(255) / TEXT / NVARCHAR(MAX)）
//	decimal → 定点数（DECIMAL(18,4)）
//	bool  → 布尔（TINYINT(1) / BOOLEAN / BIT）
//	datetime → 时间戳（DATETIME / TIMESTAMP / DATETIME2）
//	uuid  → GUID（CHAR(36) / UUID / UNIQUEIDENTIFIER）
//	blob  → 二进制（BLOB / BYTEA / VARBINARY(MAX)）

import (
	"strings"
)

// quoter 是方言的引用器。
type quoter struct {
	ident   func(string) string
	literal func(string) string
}

func dialectQuoteFor(dialect string) quoter {
	switch strings.ToLower(dialect) {
	case "mysql":
		return quoter{
			ident:   func(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" },
			literal: func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" },
		}
	case "sqlserver", "mssql":
		return quoter{
			ident:   func(s string) string { return "[" + strings.ReplaceAll(s, "]", "]]") + "]" },
			literal: func(s string) string { return "N'" + strings.ReplaceAll(s, "'", "''") + "'" },
		}
	default: // postgres / sqlite / oracle 的 ANSI 公共部分
		return quoter{
			ident:   func(s string) string { return "\"" + strings.ReplaceAll(s, "\"", "\"\"") + "\"" },
			literal: func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" },
		}
	}
}

// typeMapper 是方言的类型映射。
type typeMapper map[string]string

func typeMapFor(dialect string) typeMapper {
	switch strings.ToLower(dialect) {
	case "mysql":
		return typeMapper{
			"int": "INT", "long": "BIGINT", "text": "VARCHAR(255)",
			"decimal": "DECIMAL(18,4)", "bool": "TINYINT(1)",
			"datetime": "DATETIME", "uuid": "CHAR(36)", "blob": "BLOB",
		}
	case "sqlserver", "mssql":
		return typeMapper{
			"int": "INT", "long": "BIGINT", "text": "NVARCHAR(255)",
			"decimal": "DECIMAL(18,4)", "bool": "BIT",
			"datetime": "DATETIME2", "uuid": "UNIQUEIDENTIFIER", "blob": "VARBINARY(MAX)",
		}
	default: // postgres
		return typeMapper{
			"int": "INTEGER", "long": "BIGINT", "text": "TEXT",
			"decimal": "NUMERIC(18,4)", "bool": "BOOLEAN",
			"datetime": "TIMESTAMP", "uuid": "UUID", "blob": "BYTEA",
		}
	}
}

// mapType 映射一个方言中性类型；未知拼写原样透传（用户直写方言类型）。
func (tm typeMapper) mapType(t string) string {
	if v, ok := tm[strings.ToLower(strings.TrimSpace(t))]; ok {
		return v
	}
	return t
}
