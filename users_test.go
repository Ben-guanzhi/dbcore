package dbcore

// UserAdmin 能力的内核侧测试：能力断言 + 方言 DDL 生成的引号正确性。

import (
	"context"
	"strings"
	"testing"
)

// uaDriver 模拟实现 UserAdmin 的驱动。
type uaDriver struct{ Driver }

func (d uaDriver) ListUsers(context.Context) ([]ServerUser, error) { return nil, nil }
func (d uaDriver) CreateUserDDL(user, host, password string) string {
	return "CREATE"
}
func (d uaDriver) DropUserDDL(user, host string) string { return "DROP" }
func (d uaDriver) GrantDatabaseDDL(user, host, db string) string {
	return "GRANT"
}

func TestAsUserAdminAssertsCapability(t *testing.T) {
	if _, ok := AsUserAdmin(uaDriver{}); !ok {
		t.Error("实现了 UserAdmin 的驱动断言失败")
	}
	if _, ok := AsUserAdmin(struct{ Driver }{}); ok {
		t.Error("未实现的驱动不该断言成功")
	}
}

// 单引号字面量转义（密码里的引号不能破坏语句结构）。
func TestQuoteSingleLiteralEscapesQuotes(t *testing.T) {
	got := QuoteSingleLiteral("p'w")
	if got != "'p''w'" {
		t.Errorf("QuoteSingleLiteral = %q, want 'p''w'", got)
	}
	// 注入串不会逃出字面量。
	if !strings.HasPrefix(QuoteSingleLiteral("x'); DROP TABLE users;--"), "'x'')") {
		t.Error("注入串没有被转义")
	}
}

// 反引号/双引号标识符转义。
func TestIdentifierQuoting(t *testing.T) {
	if got := QuoteBacktickIdent("db`x"); got != "`db``x`" {
		t.Errorf("QuoteBacktickIdent = %q", got)
	}
	if got := QuoteDoubleIdent("db\"x"); got != "\"db\"\"x\"" {
		t.Errorf("QuoteDoubleIdent = %q", got)
	}
}
