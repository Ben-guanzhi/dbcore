package dbcore_test

// 跨引擎的 SSLMode 契约一致性：同一个 `require` 在 MySQL / PostgreSQL /
// MSSQL 上必须是同一个语义——**加密但不验证证书**（verify-* 才验证）。
//
// 这不是风格问题：用户从 PG 切到 MySQL 时若 require 突然开始验证证书，会撞上
// 一个他没选的错误；反过来若从 MySQL 切到 PG 以为行为一致、实际失去了证书
// 校验，则是安全问题。两边都要钉住。

import (
	"strings"
	"testing"

	"github.com/Ben-guanzhi/dbcore"
	"github.com/Ben-guanzhi/dbcore/dm"
	"github.com/Ben-guanzhi/dbcore/mssql"
	"github.com/Ben-guanzhi/dbcore/mysql"
	"github.com/Ben-guanzhi/dbcore/oracle"
	"github.com/Ben-guanzhi/dbcore/postgres"
	"github.com/Ben-guanzhi/dbcore/redis"
)

func tlsDSN(t *testing.T, kind dbcore.DatabaseKind, mode string) string {
	t.Helper()
	spec := dbcore.ConnSpec{Kind: kind, Host: "db.example", Port: 5432, User: "u", Database: "d", SSLMode: mode}
	var dsn string
	switch kind {
	case dbcore.KindMySQL:
		dsn = mysql.DSN(spec)
	case dbcore.KindPostgres:
		dsn = postgres.DSN(spec)
	case dbcore.KindMSSQL:
		dsn = mssql.DSN(spec)
	case dbcore.KindOracle:
		dsn = oracle.DSN(spec)
	case dbcore.KindDM:
		dsn = dm.DSN(spec)
	case dbcore.KindRedis:
		dsn = redis.DSN(spec)
	default:
		t.Fatalf("unsupported kind %v", kind)
	}
	return dsn
}

func TestRequireEncryptsWithoutVerifyingAcrossEngines(t *testing.T) {
	my := tlsDSN(t, dbcore.KindMySQL, "require")
	if !strings.Contains(my, "skip-verify") {
		t.Errorf("mysql require 应映射到 skip-verify（加密不验证），实得: %s", my)
	}
	if strings.Contains(my, "tls=true") {
		t.Errorf("mysql require 不应验证证书（那是 verify-full 的职责）: %s", my)
	}

	pg := tlsDSN(t, dbcore.KindPostgres, "require")
	if !strings.Contains(pg, "sslmode=require") {
		t.Errorf("postgres require 应映射到 sslmode=require，实得: %s", pg)
	}
	if strings.Contains(pg, "verify") {
		t.Errorf("postgres require 不应带 verify: %s", pg)
	}

	ms := tlsDSN(t, dbcore.KindMSSQL, "require")
	if !strings.Contains(strings.ToLower(ms), "encrypt=true") {
		t.Errorf("mssql require 应加密，实得: %s", ms)
	}
	if !strings.Contains(ms, "TrustServerCertificate=true") {
		t.Errorf("mssql require 应容忍自签名证书，实得: %s", ms)
	}
}

func TestVerifyFullIsStrictAcrossEngines(t *testing.T) {
	my := tlsDSN(t, dbcore.KindMySQL, "verify-full")
	if !strings.Contains(my, "tls=true") {
		t.Errorf("mysql verify-full 必须验证证书，实得: %s", my)
	}
	pg := tlsDSN(t, dbcore.KindPostgres, "verify-full")
	if !strings.Contains(pg, "sslmode=verify-full") {
		t.Errorf("postgres verify-full 必须全验证，实得: %s", pg)
	}
	ms := tlsDSN(t, dbcore.KindMSSQL, "verify-full")
	if strings.Contains(ms, "TrustServerCertificate=true") {
		t.Errorf("mssql verify-full 不能容忍自签名证书: %s", ms)
	}
}
