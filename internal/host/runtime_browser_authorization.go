//go:build linux || windows || (darwin && cgo)

package host

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (m *Manager) runtimeBrowserAuthorization(ctx context.Context, server string) (string, runtimeLoginAuthorization, error) {
	var authorization runtimeLoginAuthorization
	var dashboard string
	switch server {
	case "https://api.rsrs.rs":
		dashboard = "https://dash.rsrs.rs"
	case "https://api-dev.rsrs.rs", "https://api.dev.rsrs.rs":
		dashboard = "https://dash.dev.rsrs.rs"
	default:
		return "", authorization, errors.New("此服务器没有配置可验证的浏览器授权页面")
	}
	if m.OpenBrowser == nil {
		return "", authorization, errors.New("当前客户端无法打开浏览器授权页面")
	}

	client := *m.HTTPClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	post := func(ctx context.Context, path string, form url.Values, reply any) (int, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, server+path, strings.NewReader(form.Encode()))
		if err != nil {
			return 0, err
		}
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, err := client.Do(request)
		if err != nil {
			return 0, err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusBadRequest {
			return 0, fmt.Errorf("浏览器授权服务返回 HTTP %d", response.StatusCode)
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, controlFrameLimit)).Decode(reply); err != nil {
			return 0, errors.New("浏览器授权服务返回了无效响应")
		}
		return response.StatusCode, nil
	}

	const clientID = "respire-cli" // Registered public OAuth client for the device flow.
	var grant struct {
		DeviceCode string        `json:"device_code"`
		UserCode   string        `json:"user_code"`
		URI        string        `json:"verification_uri_complete"`
		Expires    time.Duration `json:"expires_in"`
		Interval   time.Duration `json:"interval"`
	}
	status, err := post(ctx, "/oauth/device/code", url.Values{"client_id": {clientID}, "device_name": {"Respire Client2"}}, &grant)
	if err != nil {
		return "", authorization, err
	}
	_, codeErr := hex.DecodeString(grant.UserCode)
	const maxSeconds = time.Duration(1<<63-1) / time.Second
	if status != http.StatusOK || grant.DeviceCode == "" || len(grant.UserCode) != 12 || codeErr != nil ||
		grant.Expires <= 0 || grant.Expires > maxSeconds || grant.Interval <= 0 || grant.Interval >= grant.Expires {
		return "", authorization, errors.New("服务器没有返回有效的浏览器授权请求")
	}
	verification := dashboard + "/#/authorize?code=" + grant.UserCode
	if grant.URI != verification {
		return "", authorization, errors.New("浏览器授权页面与服务器的预期地址不一致")
	}
	poll, cancel := context.WithTimeout(ctx, grant.Expires*time.Second)
	defer cancel()
	if err := m.OpenBrowser(verification); err != nil {
		return "", authorization, err
	}
	interval := grant.Interval * time.Second
	form := url.Values{"client_id": {clientID}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}, "device_code": {grant.DeviceCode}}
	for {
		timer := time.NewTimer(interval)
		select {
		case <-poll.Done():
			timer.Stop()
			return "", authorization, fmt.Errorf("浏览器授权未完成: %w", poll.Err())
		case <-timer.C:
		}
		var reply struct {
			Error   string  `json:"error"`
			User    string  `json:"user"`
			Token   string  `json:"access_token"`
			Type    string  `json:"token_type"`
			Session *string `json:"session_id"`
		}
		status, err := post(poll, "/oauth/token", form, &reply)
		if err != nil {
			return "", authorization, err
		}
		if status == http.StatusOK && reply.Error == "" && reply.User != "" && reply.Token != "" && strings.EqualFold(reply.Type, "bearer") {
			return reply.User, runtimeLoginAuthorization{Kind: "browser", Token: &reply.Token, Session: reply.Session}, nil
		}
		switch reply.Error {
		case "authorization_pending":
		case "slow_down":
			if interval < grant.Expires*time.Second-5*time.Second {
				interval += 5 * time.Second
			} else {
				interval = grant.Expires * time.Second
			}
		case "access_denied":
			return "", authorization, errors.New("浏览器授权已拒绝")
		case "expired_token", "invalid_grant":
			return "", authorization, errors.New("浏览器授权已过期或已被使用")
		default:
			return "", authorization, errors.New("浏览器授权服务返回了无效结果")
		}
	}
}
