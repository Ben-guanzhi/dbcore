// Package usability 是跨客户端可用性指标的**唯一实现点**（S-01）。
//
// 背景（SIMS 213 / Sullivan）：可用性不是"看起来好"，而是可测的——每条任务
// 尝试都要能回答三个问题：成功了吗、花了多久、用户是否回退了。此前 7 个客户端
// 全都没有这类数据，只能回答"多少人在用"。
//
// 设计约束：
//   - **默认关闭**：只有 POLYDB_USABILITY=1/true 时才落盘；关闭时 Record 是 no-op，
//     不产生文件、不占内存。
//   - **只落本地**：NDJSON 追加写，上限 1 MiB（契约 storage.max_bytes），到顶即停，
//     不做上传、不做联网。
//   - **契约化**：事件形状由工作区根 `spec/usability-metrics.json` 声明，
//     LoadContract/ValidateEvent 供测试用；客户端只提供 task/outcome/字段值。
//   - **并发安全**：UI 线程与后台任务都会调 Record。
package usability

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 契约缺省值（与 spec/usability-metrics.json 的 storage 段一致）。
const (
	EnvVar          = "POLYDB_USABILITY"
	DefaultFile     = "usability.ndjson"
	DefaultMaxBytes = int64(1 << 20)
	// maxSamples 限制每个 task 在内存里保留的耗时样本数（只影响 p50/p95 精度）。
	maxSamples = 4096
)

// Event 是落盘的一行。字段名即契约 required_fields。
type Event struct {
	TS         string            `json:"ts"`
	Client     string            `json:"client"`
	Task       string            `json:"task"`
	Outcome    string            `json:"outcome"`
	DurationMs float64           `json:"duration_ms"`
	Fields     map[string]string `json:"fields,omitempty"`
}

// Recorder 收集事件并（在开启时）追加写入 NDJSON。零值不可用，用 Open。
type Recorder struct {
	mu       sync.Mutex
	client   string
	path     string
	file     *os.File
	w        *bufio.Writer
	maxBytes int64
	written  int64
	capped   bool
	err      error

	stats   map[string]*taskStat
	samples map[string][]time.Duration
}

type taskStat struct {
	N, OK, Error, Cancelled, Reverted int
	LastAt                            time.Time
}

// TaskStats 是单个 task 的聚合。P50/P95 从内存样本算（样本上限 maxSamples）。
type TaskStats struct {
	N, OK, Error, Cancelled, Reverted int
	P50, P95                          time.Duration
	LastAt                            time.Time
}

// Stats 是整体聚合：三指标（成功/耗时/回退）都从这里读。
type Stats struct {
	Client  string
	Path    string
	Writing bool
	Capped  bool
	Err     error
	Tasks   map[string]TaskStats
}

// Open 打开（或按环境变量关闭）一个 Recorder。path 为空时用用户配置目录下的
// DefaultFile。Open 从不返回 nil，也从不因 IO 失败而让调用方崩溃——失败记在
// Stats().Err 里，Record 继续做内存统计（若已开启）。
func Open(client, path string) *Recorder {
	r := &Recorder{
		client:   client,
		maxBytes: DefaultMaxBytes,
		stats:    make(map[string]*taskStat),
		samples:  make(map[string][]time.Duration),
	}
	if !Enabled() {
		return r // 关闭态：无文件、无统计、Record 是 no-op
	}
	if path == "" {
		path = DefaultPath()
	}
	r.path = path
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.err = err
		return r
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		r.err = err
		return r
	}
	if st, statErr := f.Stat(); statErr == nil {
		r.written = st.Size()
	}
	r.file = f
	r.w = bufio.NewWriter(f)
	return r
}

// Enabled 报告是否通过环境变量开启（1/true，大小写不敏感）。
func Enabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(EnvVar)))
	return v == "1" || v == "true"
}

// DefaultPath 返回默认落盘路径：<userConfig>/polydb/usability.ndjson。
func DefaultPath() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return DefaultFile
	}
	return filepath.Join(dir, "polydb", DefaultFile)
}

// Path 返回落盘路径（关闭时为空）。
func (r *Recorder) Path() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.path
}

// Record 记一次任务尝试。d 是用户可感知的耗时（从动作发起到结果可见）。
// fields 的值一律用字符串（契约只声明字段名，不约束内部结构）。
func (r *Recorder) Record(task, outcome string, d time.Duration, fields map[string]string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.w == nil { // 关闭态或打开失败：不采集
		return
	}
	if r.capped {
		return
	}

	st := r.stats[task]
	if st == nil {
		st = &taskStat{}
		r.stats[task] = st
	}
	st.N++
	st.LastAt = time.Now()
	switch outcome {
	case "ok":
		st.OK++
	case "error":
		st.Error++
	case "cancelled":
		st.Cancelled++
	case "reverted":
		st.Reverted++
	}
	if len(r.samples[task]) < maxSamples {
		r.samples[task] = append(r.samples[task], d)
	}

	ev := Event{
		TS:         time.Now().Format(time.RFC3339Nano),
		Client:     r.client,
		Task:       task,
		Outcome:    outcome,
		DurationMs: float64(d.Microseconds()) / 1000.0,
		Fields:     fields,
	}
	line, err := json.Marshal(ev)
	if err != nil {
		r.err = err
		return
	}
	if r.written+int64(len(line))+1 > r.maxBytes {
		// 到顶即停：只记一次"已封顶"，之后不再写（避免无限增长掩盖真实问题）。
		r.capped = true
		_, _ = r.w.WriteString(`{"task":"_cap","outcome":"ok","client":"` + r.client + `"}` + "\n")
		_ = r.w.Flush()
		return
	}
	if _, err := r.w.Write(line); err != nil {
		r.err = err
		return
	}
	if err := r.w.WriteByte('\n'); err != nil {
		r.err = err
		return
	}
	if err := r.w.Flush(); err != nil {
		r.err = err
		return
	}
	r.written += int64(len(line)) + 1
}

// Stats 返回聚合。任务级：N/OK/Error/Cancelled/Reverted + p50/p95。
func (r *Recorder) Stats() Stats {
	if r == nil {
		return Stats{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := Stats{
		Client:  r.client,
		Path:    r.path,
		Writing: r.w != nil,
		Capped:  r.capped,
		Err:     r.err,
		Tasks:   make(map[string]TaskStats, len(r.stats)),
	}
	for task, st := range r.stats {
		ts := TaskStats{
			N: st.N, OK: st.OK, Error: st.Error,
			Cancelled: st.Cancelled, Reverted: st.Reverted,
			LastAt: st.LastAt,
		}
		if d := r.samples[task]; len(d) > 0 {
			sorted := append([]time.Duration(nil), d...)
			sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
			ts.P50 = sorted[len(sorted)*50/100]
			ts.P95 = sorted[min(len(sorted)-1, len(sorted)*95/100)]
		}
		out.Tasks[task] = ts
	}
	return out
}

// Close 冲刷并关闭文件；关闭态是 no-op。
func (r *Recorder) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.w != nil {
		_ = r.w.Flush()
		r.w = nil
	}
	if r.file != nil {
		err := r.file.Close()
		r.file = nil
		return err
	}
	return nil
}

// ─── 契约（供测试与诊断；客户端不依赖它）──────────────────────────────

// Contract 是 spec/usability-metrics.json 的反序列化形态。
type Contract struct {
	Version        int      `json:"version"`
	RequiredFields []string `json:"required_fields"`
	Outcomes       []string `json:"outcomes"`
	Tasks          map[string]struct {
		Description string   `json:"description"`
		Fields      []string `json:"fields"`
	} `json:"tasks"`
	Storage struct {
		Format   string `json:"format"`
		MaxBytes int64  `json:"max_bytes"`
	} `json:"storage"`
}

// LoadContract 读契约文件。
func LoadContract(path string) (*Contract, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Contract
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	if len(c.RequiredFields) == 0 || len(c.Tasks) == 0 {
		return nil, errors.New("usability contract: required_fields/tasks 为空")
	}
	return &c, nil
}

// ValidateEvent 校验一条事件是否满足契约（required_fields 齐全、outcome 合法、
// task 已知）。测试用它钉住"落盘事件与契约同形"。
func (c *Contract) ValidateEvent(e map[string]any) error {
	for _, f := range c.RequiredFields {
		if _, ok := e[f]; !ok {
			return fmt.Errorf("缺必填字段 %q", f)
		}
	}
	outcome, _ := e["outcome"].(string)
	okOutcome := false
	for _, o := range c.Outcomes {
		if o == outcome {
			okOutcome = true
			break
		}
	}
	if !okOutcome {
		return fmt.Errorf("未知 outcome %q（契约：%v）", outcome, c.Outcomes)
	}
	task, _ := e["task"].(string)
	if _, ok := c.Tasks[task]; !ok {
		return fmt.Errorf("未知 task %q（契约声明了 %d 个）", task, len(c.Tasks))
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
