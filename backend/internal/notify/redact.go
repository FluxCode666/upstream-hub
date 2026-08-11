package notify

import "encoding/json"

// RedactedSentinel 是配置预览里用来占位"已配置但隐藏"的哨兵值。
//
// 后端回显配置预览时把敏感字段替换成它；前端编辑表单原样回填；
// 保存时 MergeConfig 见到该值就保留数据库里已有的真实值，从而支持
// "只改名 / 只改一个字段"而不覆盖其余密钥。
const RedactedSentinel = "__REDACTED__"

// secretKeys 每种通知渠道配置里的敏感字段名。回显时这些字段若非空则替换成哨兵。
//
// 注意：webhook_url / bot_token 的 url 形态本身就携带鉴权能力，但属于管理员自己
// 配置的端点，回显可见便于编辑；这里只屏蔽真正的"纯密钥"字段。
var secretKeys = map[string]map[string]bool{
	"telegram": {"bot_token": true},
	"email":    {"password": true},
	"dingtalk": {"secret": true},
	"feishu":   {"app_secret": true, "secret": true},
	// webhook / wecom / bark：无独立密钥字段，端点 URL 由管理员自管，不屏蔽。
}

// RedactConfig 把已解密的通知渠道配置 JSON 脱敏成可回显给前端的 map：
//   - 保留结构（所有 key）和非敏感值（mode / url / id / host / port …）
//   - 命中 secretKeys 的非空字符串值替换成 RedactedSentinel
//
// 解析失败或 raw 为空时返回 nil，调用方据此省略 config_preview 字段，
// 前端回退到空表单（编辑时留空 = 保留原配置的既有兜底语义仍然成立）。
func RedactConfig(channelType string, raw string) map[string]any {
	if raw == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	secrets := secretKeys[channelType]
	for k, v := range m {
		s, ok := v.(string)
		if !ok || s == "" {
			continue
		}
		if secrets[k] {
			m[k] = RedactedSentinel
		}
	}
	return m
}

// MergeConfig 把前端提交的 incoming 配置与数据库已有的 existing 配置合并，
// 处理 RedactedSentinel：incoming 里值为哨兵的字段用 existing 的真实值替换。
//
// 语义：
//   - incoming 里某字段 == RedactedSentinel → 取 existing 同名字段（保留密钥）
//   - incoming 里某字段为其它值（含空串）→ 用 incoming 的值
//   - existing 里有但 incoming 里没有的字段 → 丢弃（前端 buildConfigByType 已覆盖该类型全部字段）
//
// 这样"只改名 / 只改 chat_id"等场景不会把 app_secret 等未改动的密钥冲成空。
// 解析失败时原样返回 incoming，避免阻断保存（交由后续 Notifier 构造校验兜底）。
func MergeConfig(incoming, existing string) (string, error) {
	var in map[string]any
	if err := json.Unmarshal([]byte(incoming), &in); err != nil {
		// incoming 不是合法 JSON：原样返回，让后续 Build/校验报错。
		return incoming, nil
	}
	var ex map[string]any
	if existing != "" {
		_ = json.Unmarshal([]byte(existing), &ex) // 解析失败按空 map 处理
	}
	for k, v := range in {
		if s, ok := v.(string); ok && s == RedactedSentinel {
			if ev, ok := ex[k]; ok {
				in[k] = ev
			} else {
				// existing 没有该字段：哨兵无意义，删掉该 key 避免存入字面哨兵。
				delete(in, k)
			}
		}
	}
	out, err := json.Marshal(in)
	if err != nil {
		return incoming, err
	}
	return string(out), nil
}
