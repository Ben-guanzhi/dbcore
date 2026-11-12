package main

// Nacos console: configuration management over the v1 Open API.
//
// Nacos stores configs as (group, dataId, content). The object tree lists them flat as
// "group/dataId" — group rarely contains a slash and dataId often does, so the FIRST
// slash is the separator, and a config's own slashes stay intact in its dataId.
//
// Two console actions:
//
//	query-config    read one config's content (the everyday operation)
//	publish-config  write one (DANGER — it changes remote state, so the host marks it
//	                and asks for confirmation, exactly like a destructive SQL statement)

import (
	"context"
	"fmt"
	"net/url"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/plugindriver"
)

const nacosListPath = "/nacos/v1/cs/configs"

type nacosProvider struct{}

func (nacosProvider) Kind() string { return "nacos" }
func (nacosProvider) Name() string { return "Nacos 配置中心" }

func init() { registerProvider(nacosProvider{}) }

func (nacosProvider) Open(_ context.Context, p plugindriver.ConnectParams) (Session, error) {
	hp := hostPort{Host: p.Host, Port: p.Port, Scheme: p.Options["scheme"], Base: p.Options["baseUrl"]}
	hp.User, hp.Pass, hp.Token = p.User, p.Password, p.Options["token"]
	if hp.Host == "" && hp.Base == "" {
		return nil, fmt.Errorf("需要主机地址")
	}
	c := newHTTPConsole(hp)
	c.token = hp.Token
	return &nacosSession{c: c}, nil
}

type nacosSession struct {
	c *httpConsole
}

// nacosConfigItem is one entry of the list response.
type nacosConfigItem struct {
	DataID  string `json:"dataId"`
	Group   string `json:"group"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

type nacosListResponse struct {
	TotalCount int               `json:"totalCount"`
	PageItems  []nacosConfigItem `json:"pageItems"`
}

func (s *nacosSession) Objects(ctx context.Context) ([]plugindriver.ConnObject, error) {
	q := url.Values{}
	q.Set("search", "blur")
	q.Set("dataId", "")
	q.Set("group", "")
	q.Set("pageNo", "1")
	q.Set("pageSize", "200")
	body, status, err := s.c.get(ctx, nacosListPath, q)
	if err != nil {
		return nil, err
	}
	var resp nacosListResponse
	if err := decodeJSON(body, status, &resp); err != nil {
		return nil, fmt.Errorf("列出配置: %w", err)
	}
	out := make([]plugindriver.ConnObject, 0, len(resp.PageItems))
	for _, it := range resp.PageItems {
		out = append(out, plugindriver.ConnObject{
			Name: it.Group + "/" + it.DataID,
			Kind: "config",
		})
	}
	return out, nil
}

func (s *nacosSession) Columns(_ context.Context, _ string) ([]plugindriver.ConnColumn, error) {
	return []plugindriver.ConnColumn{
		{Name: "group", Type: "TEXT"},
		{Name: "dataId", Type: "TEXT"},
		{Name: "type", Type: "TEXT"},
		{Name: "content", Type: "TEXT"},
	}, nil
}

// Browse reads one config's live content. The object name is "group/dataId".
func (s *nacosSession) Browse(ctx context.Context, object string, limit int) (*plugindriver.ConnQueryResult, error) {
	group, dataID := splitConfigName(object)
	content, err := s.getConfig(ctx, group, dataID)
	if err != nil {
		return nil, err
	}
	cols := []plugindriver.ConnColumn{
		{Name: "group", Type: "TEXT"},
		{Name: "dataId", Type: "TEXT"},
		{Name: "content", Type: "TEXT"},
	}
	return result(cols, [][]plugindriver.CellPayload{{
		textCell(group), textCell(dataID), textCell(content),
	}}), nil
}

func (s *nacosSession) Actions() []plugindriver.ConnAction {
	return []plugindriver.ConnAction{
		{
			ID:    "query-config",
			Title: "查询配置",
			Group: "配置",
			Params: []plugindriver.ConnActionParam{
				{Name: "dataId", Label: "dataId", Required: true},
				{Name: "group", Label: "group", Default: "DEFAULT_GROUP"},
			},
		},
		{
			ID:     "publish-config",
			Title:  "发布配置",
			Group:  "配置",
			Danger: true,
			Params: []plugindriver.ConnActionParam{
				{Name: "dataId", Label: "dataId", Required: true},
				{Name: "group", Label: "group", Default: "DEFAULT_GROUP"},
				{Name: "content", Label: "内容", Kind: "textarea", Required: true},
				{Name: "type", Label: "类型", Default: "json"},
			},
		},
	}
}

func (s *nacosSession) RunAction(ctx context.Context, action string, args map[string]any) (*plugindriver.ConnQueryResult, error) {
	switch action {
	case "query-config":
		group := argString(args, "group")
		dataID := argString(args, "dataId")
		if dataID == "" {
			return nil, &dbcore.Error{Code: "ERR_INVALID_ARG", Message: "dataId 不能为空"}
		}
		content, err := s.getConfig(ctx, group, dataID)
		if err != nil {
			return nil, err
		}
		// Real-service integration finding: the v1 GET /configs?dataId&group endpoint
		// returns the CONTENT ONLY, not the config's metadata. The old result had a
		// "type" column whose value came from args["type"] — a parameter this action
		// does not even declare, so that column was always empty. The list endpoint's
		// pageItems do carry "type", but on a real Nacos it is null for most configs
		// (verified against v2.3.2). Rather than show a column we cannot fill honestly,
		// query-config returns the same three columns as Browse: group / dataId / content.
		cols := []plugindriver.ConnColumn{
			{Name: "group", Type: "TEXT"},
			{Name: "dataId", Type: "TEXT"},
			{Name: "content", Type: "TEXT"},
		}
		return result(cols, [][]plugindriver.CellPayload{{
			textCell(group), textCell(dataID), textCell(content),
		}}), nil
	case "publish-config":
		group := argString(args, "group")
		dataID := argString(args, "dataId")
		content := argString(args, "content")
		if dataID == "" || content == "" {
			return nil, &dbcore.Error{Code: "ERR_INVALID_ARG", Message: "dataId 与 content 不能为空"}
		}
		ok, err := s.publish(ctx, group, dataID, content, argString(args, "type"))
		if err != nil {
			return nil, err
		}
		msg := "发布成功"
		if !ok {
			msg = "发布失败（Nacos 返回 false）"
		}
		return result([]plugindriver.ConnColumn{{Name: "result", Type: "TEXT"}},
			[][]plugindriver.CellPayload{{textCell(msg)}}), nil
	default:
		return nil, &dbcore.Error{Code: dbcore.ErrCodeNotSupported, Message: "未知动作 " + action}
	}
}

func (s *nacosSession) getConfig(ctx context.Context, group, dataID string) (string, error) {
	if dataID == "" {
		return "", &dbcore.Error{Code: "ERR_INVALID_ARG", Message: "dataId 不能为空"}
	}
	q := url.Values{}
	q.Set("dataId", dataID)
	q.Set("group", group)
	body, status, err := s.c.get(ctx, nacosListPath, q)
	if err != nil {
		return "", err
	}
	if status == 404 {
		return "", fmt.Errorf("配置不存在：%s/%s", group, dataID)
	}
	if status >= 400 {
		return "", fmt.Errorf("读取配置: HTTP %d: %s", status, truncate(string(body), 200))
	}
	return string(body), nil
}

func (s *nacosSession) publish(ctx context.Context, group, dataID, content, typ string) (bool, error) {
	if group == "" {
		group = "DEFAULT_GROUP"
	}
	fields := url.Values{}
	fields.Set("dataId", dataID)
	fields.Set("group", group)
	fields.Set("content", content)
	if typ == "" {
		typ = "json"
	}
	fields.Set("type", typ)
	body, status, err := s.c.postForm(ctx, nacosListPath, fields)
	if err != nil {
		return false, err
	}
	if status >= 400 {
		return false, fmt.Errorf("发布配置: HTTP %d: %s", status, truncate(string(body), 200))
	}
	return string(body) == "true", nil
}

func (s *nacosSession) Close() error { return nil }

// splitConfigName splits "group/dataId" on the FIRST slash: group rarely has one and
// dataId commonly does ("com/app/config"), so the config's own slashes must survive.
func splitConfigName(object string) (group, dataID string) {
	for i := 0; i < len(object); i++ {
		if object[i] == '/' {
			return object[:i], object[i+1:]
		}
	}
	return "DEFAULT_GROUP", object
}
