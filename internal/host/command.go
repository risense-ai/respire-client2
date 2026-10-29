package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type argument struct {
	key, flag, kind string
	required        bool
	choices         []string
	max             float64
}

type commandSpec struct {
	words       []string
	args        []argument
	method      string
	host, write bool
}

func pos(key string) argument       { return argument{key: key, kind: "string", required: true} }
func opt(key, flag string) argument { return argument{key: key, flag: flag, kind: "string"} }
func requiredOpt(key, flag string) argument {
	return argument{key: key, flag: flag, kind: "string", required: true}
}
func flag(key, name string) argument { return argument{key: key, flag: name, kind: "flag"} }
func integer(key, name string, max float64) argument {
	return argument{key: key, flag: name, kind: "int", max: max}
}
func choice(key, name string, choices ...string) argument {
	return argument{key: key, flag: name, kind: "string", choices: choices}
}

func (spec commandSpec) build(raw json.RawMessage) ([]string, error) {
	input := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, err
		}
		if input == nil {
			return nil, errors.New("参数必须是 JSON 对象")
		}
	}
	var options, positions []string
	for _, p := range spec.args {
		value, exists := input[p.key]
		delete(input, p.key)
		if !exists || value == nil {
			if p.required {
				return nil, fmt.Errorf("缺少 %s", p.key)
			}
			continue
		}
		var text string
		switch p.kind {
		case "string", "json":
			var ok bool
			text, ok = value.(string)
			if !ok || strings.ContainsRune(text, 0) {
				return nil, fmt.Errorf("%s 必须是有效字符串", p.key)
			}
			if p.required && strings.TrimSpace(text) == "" {
				return nil, fmt.Errorf("%s 不能为空", p.key)
			}
			if p.kind == "json" && !json.Valid([]byte(text)) {
				return nil, fmt.Errorf("%s 不是有效 JSON", p.key)
			}
			if len(p.choices) > 0 {
				found := false
				for _, c := range p.choices {
					found = found || text == c
				}
				if !found {
					return nil, fmt.Errorf("%s 选项无效", p.key)
				}
			}
		case "int", "ratio":
			n, ok := value.(float64)
			if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > p.max || p.kind == "int" && n != math.Trunc(n) {
				return nil, fmt.Errorf("%s 超出有效范围", p.key)
			}
			text = strconv.FormatFloat(n, 'f', -1, 64)
		case "flag", "bool":
			b, ok := value.(bool)
			if !ok {
				return nil, fmt.Errorf("%s 必须是布尔值", p.key)
			}
			if p.kind == "flag" {
				if b {
					options = append(options, p.flag)
				}
				continue
			}
			text = strconv.FormatBool(b)
		default:
			return nil, errors.New("无效的操作参数定义")
		}
		if p.flag == "" {
			positions = append(positions, text)
		} else {
			options = append(options, p.flag+"="+text)
		}
	}
	for key := range input {
		return nil, fmt.Errorf("未知参数 %s", key)
	}
	args := append(append([]string{}, spec.words...), options...)
	if len(positions) > 0 {
		if spec.method == "" {
			args = append(args, "--")
		}
		args = append(args, positions...)
	}
	return args, nil
}

func operationSpec(name string) (commandSpec, bool) {
	for _, group := range []map[string]commandSpec{memoryCommands, runtimeCommands, settingsCommands} {
		if spec, ok := group[name]; ok {
			return spec, true
		}
	}
	return commandSpec{}, false
}
