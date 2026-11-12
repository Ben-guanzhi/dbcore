package main

// Pulsar console: topics and their subscription backlog over the admin REST v2 API.
//
// Pulsar's hierarchy is tenant → namespace → topic, and the thing an operator actually
// watches is a subscription's BACKLOG (how many messages are waiting). So Browse on a
// topic lists its subscriptions with their backlog — that is the console's real read —
// while the object tree lists the topics themselves.
//
// The admin API's topic paths are long because a topic is addressed by its full
// "persistent://tenant/namespace/topic" form, which the REST path spells as
// /admin/v2/persistent/{tenant}/{namespace}/{topic}.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/plugindriver"
)

type pulsarProvider struct{}

func (pulsarProvider) Kind() string { return "pulsar" }
func (pulsarProvider) Name() string { return "Pulsar 消息队列" }

func (pulsarProvider) Open(_ context.Context, p plugindriver.ConnectParams) (Session, error) {
	hp := hostPort{Host: p.Host, Port: p.Port, Scheme: p.Options["scheme"], Base: p.Options["baseUrl"]}
	// The admin service is conventionally 8080; a broker's standalone is 8080 too.
	if hp.Port == "" {
		hp.Port = "8080"
	}
	if hp.Host == "" && hp.Base == "" {
		return nil, fmt.Errorf("需要主机地址")
	}
	c := newHTTPConsole(hp)
	c.token = p.Options["token"]
	return &pulsarSession{c: c}, nil
}

type pulsarSession struct {
	c *httpConsole
}

// pulsarAdmin decodes an admin response. The admin API returns bare JSON (a list or an
// object) and signals failure with a non-2xx plus a {"reason": "..."} body.
func (s *pulsarSession) admin(ctx context.Context, path string, out any) error {
	body, status, err := s.c.get(ctx, path, nil)
	if err != nil {
		return err
	}
	if status >= 400 {
		return fmt.Errorf("Pulsar 管理 API: HTTP %d: %s", status, truncate(string(body), 200))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

// Objects walks tenant → namespace → topic. Bounded: a cluster with many namespaces
// would otherwise issue one request per namespace on every tree refresh, so the walk
// stops at maxPulsarTopics.
func (s *pulsarSession) Objects(ctx context.Context) ([]plugindriver.ConnObject, error) {
	var tenants []string
	if err := s.admin(ctx, "/admin/v2/tenants", &tenants); err != nil {
		return nil, fmt.Errorf("列出租户: %w", err)
	}
	var out []plugindriver.ConnObject
	for _, tenant := range tenants {
		var namespaces []string
		if err := s.admin(ctx, "/admin/v2/namespaces/"+tenant, &namespaces); err != nil {
			continue // a tenant we cannot read should not hide the others
		}
		for _, ns := range namespaces {
			topics, err := s.topicsOfNamespace(ctx, ns)
			if err != nil {
				continue
			}
			for _, t := range topics {
				out = append(out, plugindriver.ConnObject{Name: t, Kind: "topic"})
				if len(out) >= maxPulsarTopics {
					return out, nil
				}
			}
		}
	}
	return out, nil
}

// topicsOfNamespace lists the persistent and non-persistent topics of one namespace.
func (s *pulsarSession) topicsOfNamespace(ctx context.Context, namespace string) ([]string, error) {
	var out []string
	for _, domain := range []string{"persistent", "non-persistent"} {
		var topics []string
		if err := s.admin(ctx, "/admin/v2/"+domain+"/"+namespace, &topics); err != nil {
			continue
		}
		out = append(out, topics...)
	}
	return out, nil
}

// topicPath converts a full topic ("persistent://tenant/ns/name") into its admin REST
// path ("/admin/v2/persistent/tenant/ns/name").
func topicPath(topic string) string {
	t := strings.TrimPrefix(topic, "persistent://")
	t = strings.TrimPrefix(t, "non-persistent://")
	domain := "persistent"
	if strings.Contains(topic, "non-persistent://") {
		domain = "non-persistent"
	}
	return "/admin/v2/" + domain + "/" + t
}

func (s *pulsarSession) Columns(_ context.Context, _ string) ([]plugindriver.ConnColumn, error) {
	return []plugindriver.ConnColumn{
		{Name: "subscription", Type: "TEXT"},
		{Name: "backlog", Type: "BIGINT"},
		{Name: "blocked", Type: "BOOL"},
	}, nil
}

// Browse lists a topic's subscriptions with their backlog — the operator's daily view.
func (s *pulsarSession) Browse(ctx context.Context, topic string, _ int) (*plugindriver.ConnQueryResult, error) {
	var subs []string
	path := topicPath(topic) + "/subscriptions"
	if err := s.admin(ctx, path, &subs); err != nil {
		return nil, fmt.Errorf("列出订阅: %w", err)
	}
	cols := []plugindriver.ConnColumn{
		{Name: "subscription", Type: "TEXT"},
		{Name: "backlog", Type: "BIGINT"},
	}
	var rows [][]plugindriver.CellPayload
	for _, sub := range subs {
		backlog := s.backlogOf(ctx, topic, sub)
		rows = append(rows, []plugindriver.CellPayload{textCell(sub), intCell(backlog)})
	}
	return result(cols, rows), nil
}

func (s *pulsarSession) Actions() []plugindriver.ConnAction {
	return []plugindriver.ConnAction{
		{
			ID:    "topic-stats",
			Title: "查看主题统计",
			Group: "主题",
			Params: []plugindriver.ConnActionParam{
				{Name: "topic", Label: "主题（完整名）", Required: true},
			},
		},
		{
			ID:    "list-namespaces",
			Title: "查看租户命名空间",
			Group: "主题",
			Params: []plugindriver.ConnActionParam{
				{Name: "tenant", Label: "租户", Required: true},
			},
		},
	}
}

func (s *pulsarSession) RunAction(ctx context.Context, action string, args map[string]any) (*plugindriver.ConnQueryResult, error) {
	switch action {
	case "topic-stats":
		topic := argString(args, "topic")
		if topic == "" {
			return nil, &dbcore.Error{Code: "ERR_INVALID_ARG", Message: "topic 不能为空"}
		}
		body, status, err := s.c.get(ctx, topicPath(topic)+"/stats", nil)
		if err != nil {
			return nil, err
		}
		if status >= 400 {
			return nil, fmt.Errorf("主题统计: HTTP %d: %s", status, truncate(string(body), 200))
		}
		// The stats document is a nested blob; show it as pretty JSON in one cell so
		// the operator can read the whole thing rather than a flattened guess.
		var pretty json.RawMessage
		if err := json.Unmarshal(body, &pretty); err == nil {
			if indented, err := json.MarshalIndent(pretty, "", "  "); err == nil {
				return result([]plugindriver.ConnColumn{{Name: "stats", Type: "TEXT"}},
					[][]plugindriver.CellPayload{{textCell(string(indented))}}), nil
			}
		}
		return result([]plugindriver.ConnColumn{{Name: "stats", Type: "TEXT"}},
			[][]plugindriver.CellPayload{{textCell(string(body))}}), nil
	case "list-namespaces":
		tenant := argString(args, "tenant")
		if tenant == "" {
			return nil, &dbcore.Error{Code: "ERR_INVALID_ARG", Message: "tenant 不能为空"}
		}
		var namespaces []string
		if err := s.admin(ctx, "/admin/v2/namespaces/"+tenant, &namespaces); err != nil {
			return nil, err
		}
		cols := []plugindriver.ConnColumn{{Name: "namespace", Type: "TEXT"}}
		var rows [][]plugindriver.CellPayload
		for _, ns := range namespaces {
			rows = append(rows, []plugindriver.CellPayload{textCell(ns)})
		}
		return result(cols, rows), nil
	default:
		return nil, &dbcore.Error{Code: dbcore.ErrCodeNotSupported, Message: "未知动作 " + action}
	}
}

// backlogOf reads one subscription's backlog; a failure to read one subscription is not
// fatal to the list, so it degrades to 0 rather than aborting the whole browse.
func (s *pulsarSession) backlogOf(ctx context.Context, topic, sub string) int64 {
	body, status, err := s.c.get(ctx, topicPath(topic)+"/subscription/"+url.PathEscape(sub)+"/backlog", nil)
	if err != nil || status >= 400 {
		return 0
	}
	var n int64
	_ = json.Unmarshal(body, &n)
	return n
}

func (s *pulsarSession) Close() error { return nil }

const maxPulsarTopics = 2000

func init() { registerProvider(pulsarProvider{}) }
