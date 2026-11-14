// Package oracledsn holds the Oracle DSN spelling, free of the go-ora driver.
//
// 波 100 对齐（dbstudio 先例）：go-ora 单个驱动 ~13MB，而 DSN 拼装是纯字符串
// 运算。把它从 dbcore/oracle 拆到这里，客户端就能在不链接 go-ora 的前提下
// 拼出 oracle:// 连接串（把驱动留给可选插件）；dbcore/oracle 再引本包复用，
// 消除重复。
package oracledsn

import (
	"net/url"
	"strings"

	"github.com/dbcore/dbcore"
)

// DSN renders spec as an oracle:// URL understood by go-ora. The database part
// is the service name (or SID), i.e. "oracle://user:pass@host:1521/ORCLPDB1".
func DSN(spec dbcore.ConnSpec) string {
	u := url.URL{
		Scheme: "oracle",
		User:   url.UserPassword(spec.User, spec.Password),
		Host:   spec.Addr(),
	}
	service := spec.Database
	if service == "" {
		service = spec.Schema
	}
	if service != "" {
		u.Path = "/" + service
	}
	q := url.Values{}
	for k, v := range spec.Params {
		q.Set(k, v)
	}
	if strings.EqualFold(spec.SSLMode, "require") || strings.EqualFold(spec.SSLMode, "verify-full") {
		// go-ora enables TLS via the SSL parameter.
		q.Set("SSL", "true")
	}
	u.RawQuery = q.Encode()
	return u.String()
}
