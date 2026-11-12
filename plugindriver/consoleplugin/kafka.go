package main

// Kafka console: topics and message consume/produce over segmentio/kafka-go.
//
// Kafka has no HTTP API, so this is the one provider that needs a client library —
// which is exactly why the console plugin is its OWN Go module. Putting kafka-go in the
// dbcore kernel would force every consumer (fynequery, sqltui, polydb) to pull a Kafka
// client it will never use. As a separate module, the dependency stops at the plugin
// binary and the kernel stays lean.
//
// The console surface:
//
//	object tree   topics (flat, each with its partitions)
//	browse        consume a bounded batch from one partition, earliest-first
//	actions       consume-messages (pick partition/offset/count) and
//	              produce-message (DANGER — writes to the broker)
//
// The broker-touching code is behind two small interfaces so the mapping logic (how a
// Kafka message becomes a grid row) is unit-testable WITHOUT a running broker.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/plugindriver"
	"github.com/segmentio/kafka-go"
)

type kafkaProvider struct{}

func (kafkaProvider) Kind() string { return "kafka" }
func (kafkaProvider) Name() string { return "Kafka 消息队列" }

func (kafkaProvider) Open(_ context.Context, p plugindriver.ConnectParams) (Session, error) {
	brokers := kafkaBrokers(p.Host, p.Port)
	if len(brokers) == 0 {
		return nil, fmt.Errorf("需要 broker 地址")
	}
	return &kafkaSession{
		brokers: brokers,
		list:    kafkaTopicLister{brokers: brokers},
		consume: kafkaMessageConsumer{brokers: brokers},
		produce: kafkaMessageProducer{brokers: brokers},
	}, nil
}

// kafkaBrokers splits a host field into broker addresses. A cluster is usually given as
// "broker1,broker2,broker3"; a single host uses the port for all of them.
func kafkaBrokers(host, port string) []string {
	if host == "" {
		return nil
	}
	if port == "" {
		port = "9092"
	}
	var out []string
	for _, h := range strings.Split(host, ",") {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if !strings.Contains(h, ":") {
			h = h + ":" + port
		}
		out = append(out, h)
	}
	return out
}

type kafkaSession struct {
	brokers []string
	list    topicLister
	consume messageConsumer
	produce messageProducer
}

// consoleMessage is the broker-independent shape of one message, so the interfaces and
// the tests do not import kafka-go.
type consoleMessage struct {
	Partition int
	Offset    int64
	Key       string
	Value     string
	Time      time.Time
}

type topicLister interface {
	ListTopics(ctx context.Context) ([]string, error)
	Partitions(ctx context.Context, topic string) ([]int, error)
}

type consumeOpts struct {
	Partition   int
	Offset      int64
	MaxMessages int
	Timeout     time.Duration
}

type messageConsumer interface {
	Consume(ctx context.Context, topic string, o consumeOpts) ([]consoleMessage, error)
}

type messageProducer interface {
	Produce(ctx context.Context, topic, key, value string) error
}

func (s *kafkaSession) Objects(ctx context.Context) ([]plugindriver.ConnObject, error) {
	topics, err := s.list.ListTopics(ctx)
	if err != nil {
		return nil, fmt.Errorf("列出主题: %w", err)
	}
	out := make([]plugindriver.ConnObject, 0, len(topics))
	for _, t := range topics {
		// Internal topics (__consumer_offsets and friends) are noise in a console tree,
		// but hiding them entirely would confuse an operator looking for one. Mark them
		// as views so the tree can render them differently rather than dropping them.
		kind := "topic"
		if strings.HasPrefix(t, "__") {
			kind = "view"
		}
		out = append(out, plugindriver.ConnObject{Name: t, Kind: kind})
	}
	return out, nil
}

func (s *kafkaSession) Columns(ctx context.Context, topic string) ([]plugindriver.ConnColumn, error) {
	parts, err := s.list.Partitions(ctx, topic)
	if err != nil {
		return nil, err
	}
	cols := []plugindriver.ConnColumn{{Name: "partition", Type: "INT"}}
	for _, p := range parts {
		cols = append(cols, plugindriver.ConnColumn{Name: "partition_" + strconv.Itoa(p), Type: "TEXT"})
	}
	return cols, nil
}

func (s *kafkaSession) Browse(ctx context.Context, topic string, limit int) (*plugindriver.ConnQueryResult, error) {
	if limit <= 0 {
		limit = 50
	}
	msgs, err := s.consume.Consume(ctx, topic, consumeOpts{
		Partition:   0,
		Offset:      kafka.FirstOffset,
		MaxMessages: limit,
		Timeout:     5 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("消费消息: %w", err)
	}
	return messageResult(msgs), nil
}

func (s *kafkaSession) Actions() []plugindriver.ConnAction {
	return []plugindriver.ConnAction{
		{
			ID:    "consume-messages",
			Title: "消费消息",
			Group: "消息",
			Params: []plugindriver.ConnActionParam{
				{Name: "topic", Label: "主题", Required: true},
				{Name: "partition", Label: "分区（-1 表示不指定）", Kind: "number", Default: "0"},
				{Name: "offset", Label: "起始偏移（-2 earliest / -1 latest）", Kind: "number", Default: "-2"},
				{Name: "max", Label: "最多条数", Kind: "number", Default: "50"},
				{Name: "timeoutMs", Label: "超时(ms)", Kind: "number", Default: "5000"},
			},
		},
		{
			ID:     "produce-message",
			Title:  "生产消息",
			Group:  "消息",
			Danger: true,
			Params: []plugindriver.ConnActionParam{
				{Name: "topic", Label: "主题", Required: true},
				{Name: "key", Label: "key"},
				{Name: "value", Label: "value", Kind: "textarea", Required: true},
			},
		},
	}
}

func (s *kafkaSession) RunAction(ctx context.Context, action string, args map[string]any) (*plugindriver.ConnQueryResult, error) {
	switch action {
	case "consume-messages":
		topic := argString(args, "topic")
		if topic == "" {
			return nil, &dbcore.Error{Code: "ERR_INVALID_ARG", Message: "topic 不能为空"}
		}
		max := argInt(args, "max", 50)
		timeout := time.Duration(argInt(args, "timeoutMs", 5000)) * time.Millisecond
		opts := consumeOpts{
			Partition:   int(argInt(args, "partition", 0)),
			Offset:      argInt(args, "offset", int64(kafka.FirstOffset)),
			MaxMessages: int(max),
			Timeout:     timeout,
		}
		msgs, err := s.consume.Consume(ctx, topic, opts)
		if err != nil {
			return nil, err
		}
		return messageResult(msgs), nil
	case "produce-message":
		topic := argString(args, "topic")
		value := argString(args, "value")
		if topic == "" || value == "" {
			return nil, &dbcore.Error{Code: "ERR_INVALID_ARG", Message: "topic 与 value 不能为空"}
		}
		if err := s.produce.Produce(ctx, topic, argString(args, "key"), value); err != nil {
			return nil, fmt.Errorf("生产消息: %w", err)
		}
		return result([]plugindriver.ConnColumn{{Name: "result", Type: "TEXT"}},
			[][]plugindriver.CellPayload{{textCell("已发送")}}), nil
	default:
		return nil, &dbcore.Error{Code: dbcore.ErrCodeNotSupported, Message: "未知动作 " + action}
	}
}

func (s *kafkaSession) Close() error { return nil }

// messageResult is the one grid shape both browse and consume return: partition,
// offset, key, value, time.
func messageResult(msgs []consoleMessage) *plugindriver.ConnQueryResult {
	cols := []plugindriver.ConnColumn{
		{Name: "partition", Type: "INT"},
		{Name: "offset", Type: "BIGINT"},
		{Name: "key", Type: "TEXT"},
		{Name: "value", Type: "TEXT"},
		{Name: "time", Type: "TIMESTAMP"},
	}
	rows := make([][]plugindriver.CellPayload, 0, len(msgs))
	for _, m := range msgs {
		rows = append(rows, []plugindriver.CellPayload{
			intCell(int64(m.Partition)),
			intCell(m.Offset),
			textCell(m.Key),
			textCell(m.Value),
			{Kind: "time", Time: m.Time.UTC().Format(time.RFC3339Nano)},
		})
	}
	return result(cols, rows)
}

// ---------- kafka-go-backed implementations ----------

type kafkaTopicLister struct{ brokers []string }

func (k kafkaTopicLister) ListTopics(ctx context.Context) ([]string, error) {
	client := &kafka.Client{Addr: kafka.TCP(k.brokers...), Timeout: 10 * time.Second}
	resp, err := client.Metadata(ctx, &kafka.MetadataRequest{})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(resp.Topics))
	for _, t := range resp.Topics {
		out = append(out, t.Name)
	}
	return out, nil
}

func (k kafkaTopicLister) Partitions(ctx context.Context, topic string) ([]int, error) {
	client := &kafka.Client{Addr: kafka.TCP(k.brokers...), Timeout: 10 * time.Second}
	resp, err := client.Metadata(ctx, &kafka.MetadataRequest{Topics: []string{topic}})
	if err != nil {
		return nil, err
	}
	var out []int
	for _, t := range resp.Topics {
		for _, p := range t.Partitions {
			out = append(out, p.ID)
		}
	}
	return out, nil
}

type kafkaMessageConsumer struct{ brokers []string }

func (k kafkaMessageConsumer) Consume(ctx context.Context, topic string, o consumeOpts) ([]consoleMessage, error) {
	cfg := kafka.ReaderConfig{
		Brokers:  k.brokers,
		Topic:    topic,
		MinBytes: 1,
		MaxBytes: 10e6,
	}
	if o.Partition >= 0 {
		cfg.Partition = o.Partition
	}
	reader := kafka.NewReader(cfg)
	defer reader.Close()
	if o.Partition >= 0 && o.Offset != 0 {
		if err := reader.SetOffset(o.Offset); err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	var out []consoleMessage
	for i := 0; i < o.MaxMessages; i++ {
		m, err := reader.ReadMessage(ctx)
		if err != nil {
			// A timeout after we already have messages is a complete batch, not a
			// failure: a topic with fewer messages than asked for should return them.
			if ctx.Err() != nil && len(out) > 0 {
				break
			}
			if len(out) > 0 {
				break
			}
			return nil, err
		}
		out = append(out, consoleMessage{
			Partition: m.Partition,
			Offset:    m.Offset,
			Key:       string(m.Key),
			Value:     string(m.Value),
			Time:      m.Time,
		})
	}
	return out, nil
}

type kafkaMessageProducer struct{ brokers []string }

func (k kafkaMessageProducer) Produce(ctx context.Context, topic, key, value string) error {
	w := &kafka.Writer{
		Addr:     kafka.TCP(k.brokers...),
		Topic:    topic,
		Balancer: &kafka.LeastBytes{},
	}
	defer w.Close()
	return w.WriteMessages(ctx, kafka.Message{Key: []byte(key), Value: []byte(value)})
}

func init() { registerProvider(kafkaProvider{}) }
