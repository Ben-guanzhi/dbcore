package usability

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// contractPath 指向工作区根的共享契约（dbcore 的上一级）。
func contractPath() string {
	return filepath.Join("..", "spec", "usability-metrics.json")
}

func TestDisabledIsNoOp(t *testing.T) {
	t.Setenv(EnvVar, "")
	path := filepath.Join(t.TempDir(), "u.ndjson")
	r := Open("test", path)
	defer r.Close()
	r.Record("query", "ok", time.Millisecond, map[string]string{"statement_type": "SELECT"})
	if st := r.Stats(); st.Writing || st.Tasks["query"].N != 0 {
		t.Fatalf("关闭态不应采集，得到 %+v", st)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("关闭态不应创建文件，err=%v", err)
	}
}

// 落盘事件必须与共享契约同形——这是 S-01 的"契约"部分：换客户端只换 task 字段，
// 形状不变。契约文件在 dbcore 的上一级（工作区根 spec/）。
func TestEventsConformToContract(t *testing.T) {
	t.Setenv(EnvVar, "1")
	path := filepath.Join(t.TempDir(), "u.ndjson")
	r := Open("test", path)

	r.Record("connect", "ok", 12*time.Millisecond, map[string]string{"engine": "postgres", "ok": "true"})
	r.Record("query", "error", 30*time.Millisecond, map[string]string{"statement_type": "SELECT", "rows": "0", "truncated": "false"})
	r.Record("rollback", "reverted", 5*time.Millisecond, map[string]string{"table": "orders"})
	r.Close()

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	contract, err := LoadContract(contractPath())
	if err != nil {
		t.Fatalf("读契约: %v", err)
	}

	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		var ev map[string]any
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("第 %d 行不是 JSON: %v (%s)", n, err, sc.Text())
		}
		if err := contract.ValidateEvent(ev); err != nil {
			t.Errorf("第 %d 行违反契约: %v (%s)", n, err, sc.Text())
		}
		if _, ok := ev["duration_ms"].(float64); !ok {
			t.Errorf("第 %d 行 duration_ms 不是数字: %T", n, ev["duration_ms"])
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if n != 3 {
		t.Fatalf("写了 %d 行，期望 3", n)
	}
}

func TestStatsAggregateOutcomesAndPercentiles(t *testing.T) {
	t.Setenv(EnvVar, "1")
	r := Open("test", filepath.Join(t.TempDir(), "u.ndjson"))
	defer r.Close()

	for i, d := range []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond, 40 * time.Millisecond} {
		outcome := "ok"
		if i == 3 {
			outcome = "error"
		}
		r.Record("query", outcome, d, map[string]string{"statement_type": "SELECT"})
	}
	r.Record("rollback", "reverted", time.Millisecond, map[string]string{"table": "t"})

	st := r.Stats()
	q := st.Tasks["query"]
	if q.N != 4 || q.OK != 3 || q.Error != 1 {
		t.Fatalf("query 统计不对: %+v", q)
	}
	if q.P50 == 0 || q.P95 == 0 || q.P95 < q.P50 {
		t.Fatalf("p50/p95 不合理: p50=%v p95=%v", q.P50, q.P95)
	}
	if rb := st.Tasks["rollback"]; rb.N != 1 || rb.Reverted != 1 {
		t.Fatalf("rollback 应记 reverted: %+v", rb)
	}
}

func TestCapStopsWriting(t *testing.T) {
	t.Setenv(EnvVar, "1")
	path := filepath.Join(t.TempDir(), "u.ndjson")
	r := Open("test", path)
	defer r.Close()
	// 同包测试可直接收窄上限：约 3 行就会封顶。
	r.mu.Lock()
	r.maxBytes = 400
	r.mu.Unlock()

	for i := 0; i < 50; i++ {
		r.Record("query", "ok", time.Millisecond, map[string]string{"statement_type": "SELECT", "rows": "1", "truncated": "false"})
	}
	if !r.Stats().Capped {
		t.Fatal("超过 max_bytes 后应置 capped")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Size() > 1024 {
		t.Fatalf("封顶后文件仍在增长：%d 字节", fi.Size())
	}
}
