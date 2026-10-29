package host

import (
	"encoding/json"
	"errors"
	"strings"
)

// Nonsecret routing fields are allowlisted by commandSpec; credentials never become argv.
func authParams(args []string, secret string) (map[string]string, error) {
	params := map[string]string{}
	if secret != "" {
		if len(secret) > 49152 {
			return nil, errors.New("认证材料超过大小限制")
		}
		if err := json.Unmarshal([]byte(secret), &params); err != nil {
			return nil, errors.New("认证材料格式无效")
		}
		if params == nil {
			return nil, errors.New("认证材料必须是对象")
		}
	}
	for key, value := range params {
		switch key {
		case "password", "super", "secret", "kdf_salt", "wrapped_urk", "urk_nonce", "totp":
		default:
			return nil, errors.New("未知认证材料字段")
		}
		if len(value) > 16384 || strings.ContainsRune(value, 0) {
			return nil, errors.New("认证材料字段格式无效")
		}
	}
	for _, arg := range args {
		key, value, ok := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		if !ok || (key != "user" && key != "addr") {
			return nil, errors.New("认证路由参数无效")
		}
		if len(value) > 16384 || strings.ContainsRune(value, 0) {
			return nil, errors.New("认证路由字段超出限制")
		}
		params[key] = value
	}
	return params, nil
}
