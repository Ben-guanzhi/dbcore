package dbcore

// 数据生成器的内核侧测试：确定性、类型覆盖、DDL 转义。

import (
	"strings"
	"testing"
)

func sampleCols() []ColumnInfo {
	return []ColumnInfo{
		{Name: "id", Type: "int", IsPrimaryKey: true},
		{Name: "email", Type: "varchar(120)", Nullable: true},
		{Name: "salary", Type: "decimal(10,2)"},
		{Name: "active", Type: "tinyint(1)"},
		{Name: "created_at", Type: "timestamp"},
		{Name: "external_id", Type: "uuid"},
		{Name: "payload", Type: "blob"},
		{Name: "note", Type: "text"},
	}
}

// 确定性：同一 seed 产出完全一致的值序列（测试可复现的关键性质）。
func TestGenerateRowsIsDeterministic(t *testing.T) {
	cols := sampleCols()
	a := GenerateRows(cols, GenOptions{Rows: 5, Seed: 42})
	b := GenerateRows(cols, GenOptions{Rows: 5, Seed: 42})
	if len(a) != len(b) || len(a) != 5 {
		t.Fatalf("行数不对: %d vs %d", len(a), len(b))
	}
	for i := range a {
		for j := range a[i] {
			if a[i][j] != b[i][j] {
				t.Fatalf("行 %d 列 %d 不一致: %q vs %q", i, j, a[i][j], b[i][j])
			}
		}
	}
}

// 类型覆盖：每列的值形状正确（数字裸值、文本带引号、email 形状、UUID 形状）。
func TestGenerateRowsCoversTypes(t *testing.T) {
	rows := GenerateRows(sampleCols(), GenOptions{Rows: 1, Seed: 7})
	if len(rows) != 1 {
		t.Fatal("行数")
	}
	row := rows[0]
	// SkipPrimaryKey 未开：8 列全在。
	if len(row) != 8 {
		t.Fatalf("列数 = %d, want 8", len(row))
	}
	if strings.HasPrefix(row[0], "'") {
		t.Errorf("整数不应带引号: %s", row[0])
	}
	if !strings.Contains(row[1], "@") {
		t.Errorf("email 列名应生成 email 形状: %s", row[1])
	}
	if strings.Contains(row[2], "'") {
		t.Errorf("小数不应带引号: %s", row[2])
	}
	if row[3] != "TRUE" && row[3] != "FALSE" {
		t.Errorf("布尔值不对: %s", row[3])
	}
	if !strings.Contains(row[4], "-") {
		t.Errorf("时间戳应为日期形状: %s", row[4])
	}
	if !strings.Contains(row[5], "-") || len(row[5]) < 30 {
		t.Errorf("UUID 形状不对: %s", row[5])
	}
	if !strings.HasPrefix(row[6], "'") {
		t.Errorf("blob 应为带引号十六进制: %s", row[6])
	}
	if !strings.HasPrefix(row[7], "'") {
		t.Errorf("text 应为带引号文本: %s", row[7])
	}
}

// SkipPrimaryKey：主键列不出现在 INSERT 里（自增由服务器填）。
func TestGenerateInsertsSkipsPrimaryKey(t *testing.T) {
	cols := sampleCols()
	stmts := GenerateInserts(cols, GenOptions{Rows: 2, Seed: 3, Dialect: "mysql", Table: "t1", SkipPrimaryKey: true})
	if len(stmts) != 2 {
		t.Fatalf("语句数 = %d", len(stmts))
	}
	s := stmts[0]
	if !strings.HasPrefix(s, "INSERT INTO `t1` (") {
		t.Errorf("MySQL 方言的表名/反引号不对: %s", s)
	}
	if strings.Contains(s, "`id`") {
		t.Errorf("主键列被跳过却出现在语句里: %s", s)
	}
	if !strings.Contains(s, "`email`") {
		t.Errorf("email 列应在列清单里: %s", s)
	}
	if !strings.HasSuffix(s, ");") {
		t.Errorf("语句应以 ); 结束: %s", s)
	}
}

// 值里的引号被转义（生成文本不破坏语句结构）。
func TestGenerateValuesEscapeQuotes(t *testing.T) {
	// note 列名走了通用词分支，不会带引号；直接验证转义 helper 的调用路径：
	// 造一个值里可能含引号的形状——email 域名固定无引号，改验 INSERT 整体
	// 不含裸 ' 的数量错误（引号必成对）。
	stmts := GenerateInserts(sampleCols(), GenOptions{Rows: 10, Seed: 11, Dialect: "postgres", Table: "gen"})
	for i, s := range stmts {
		if !strings.HasPrefix(s, `INSERT INTO "gen" (`) {
			t.Fatalf("PG 方言引用不对: %s", s)
		}
		if n := strings.Count(s, "'"); n%2 != 0 {
			t.Fatalf("语句 %d 的单引号数量为奇数——转义破了结构: %s", i, s)
		}
	}
}
