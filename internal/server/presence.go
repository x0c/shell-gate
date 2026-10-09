package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const presenceCheckTimeout = 3 * time.Second

// homePresence 查询「用户是否在家」。仅用于决定要不要发网络唤醒。
type homePresence interface {
	ComfortHome(ctx context.Context) (bool, error)
}

// harborPresenceClient 读 presence 服务 GET /v1/presence 的 comfort_home 字段。
// 对接示例见 docs/design/2026-09-09-wake-suppress-when-home.md；服务选型由部署方自定。
type harborPresenceClient struct {
	url    string
	token  string
	client *http.Client
}

func newHarborPresenceClient(url, token string) *harborPresenceClient {
	return &harborPresenceClient{
		url:   url,
		token: token,
		client: &http.Client{
			Timeout:   presenceCheckTimeout,
			Transport: &http.Transport{Proxy: nil, TLSHandshakeTimeout: presenceCheckTimeout, ResponseHeaderTimeout: presenceCheckTimeout},
			// 禁止跟随重定向：presence 基址必须是我们配置的那一个，不能被拐到别处。
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (c *harborPresenceClient) ComfortHome(ctx context.Context) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return false, err
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("presence HTTP %d", resp.StatusCode)
	}
	var payload struct {
		ComfortHome bool `json:"comfort_home"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false, fmt.Errorf("解析 presence: %w", err)
	}
	return payload.ComfortHome, nil
}
