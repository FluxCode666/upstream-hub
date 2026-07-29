// Package channel 提供渠道领域服务：把存储层的加密字段解开成 connector.Channel，
// 处理登录会话的复用与刷新、手动测试登录、手动刷新余额 / 倍率等。
package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/worryzyy/upstream-hub/internal/captcha"
	"github.com/worryzyy/upstream-hub/internal/connector"
	"github.com/worryzyy/upstream-hub/internal/crypto"
	"github.com/worryzyy/upstream-hub/internal/progress"
	"github.com/worryzyy/upstream-hub/internal/storage"
)

// SessionRefreshThreshold 距离过期还有多久就提前刷新登录。
const SessionRefreshThreshold = 5 * time.Minute

// tokenSessionTTL token 模式下"假装"给 AuthSession 的有效期。
// token 由用户提供，我们没法续期，这里设一年只是为了避免 SessionRefreshThreshold 把它判过期。
// 真正失效检测靠 connector.CheckAuth + 上游 401/403。
const tokenSessionTTL = 365 * 24 * time.Hour

// ErrInvalidRechargeURL 标识充值链接未通过输入校验，API 层应映射为 400。
var ErrInvalidRechargeURL = errors.New("充值链接无效")

// Service 渠道领域服务。
type Service struct {
	Channels     *storage.Channels
	AuthSessions *storage.AuthSessions
	Captchas     *storage.Captchas
	MonitorLogs  *storage.MonitorLogs
	Cipher       *crypto.Cipher
}

func NewService(
	channels *storage.Channels,
	authSessions *storage.AuthSessions,
	captchas *storage.Captchas,
	monitorLogs *storage.MonitorLogs,
	cipher *crypto.Cipher,
) *Service {
	return &Service{
		Channels:     channels,
		AuthSessions: authSessions,
		Captchas:     captchas,
		MonitorLogs:  monitorLogs,
		Cipher:       cipher,
	}
}

// NewAPITokenCredential token 模式下 NewAPI 的凭据 JSON 结构。
//
// Cookie：浏览器 DevTools 里拷出来的整条 Cookie 头
// UserID：上游账号 ID（NewAPI 个人设置页可见，作为 New-Api-User 请求头必填）
type NewAPITokenCredential struct {
	Cookie string `json:"cookie"`
	UserID string `json:"user_id"`
}

// Sub2APITokenCredential token 模式下 Sub2API 的凭据。
type Sub2APITokenCredential struct {
	AccessToken string `json:"access_token"`
}

// CreateInput 新建渠道使用的明文输入。
//
// CredentialMode 决定字段语义：
//   - password: Password 必填；Username 为登录账号
//   - token:    TokenCredential 必填（已序列化为 JSON 字符串）；Username 仅作展示备注
type CreateInput struct {
	Name             string
	Type             storage.ChannelType
	SiteURL          string
	RechargeURL      string
	Username         string
	Password         string
	CredentialMode   storage.CredentialMode
	TokenCredential  string // JSON：password 模式时为空
	TurnstileEnabled bool
	CaptchaConfigID  *uint
	BalanceThreshold float64
	MonitorEnabled   bool
}

func (s *Service) Create(in CreateInput) (*storage.Channel, error) {
	rechargeURL, err := normalizeRechargeURL(in.RechargeURL)
	if err != nil {
		return nil, err
	}
	mode := in.CredentialMode
	if mode == "" {
		mode = storage.CredentialModePassword
	}
	rawCred, err := selectRawCredential(mode, in.Password, in.TokenCredential)
	if err != nil {
		return nil, err
	}
	if err := validateCredential(in.Type, mode, rawCred); err != nil {
		return nil, err
	}

	enc, err := s.Cipher.Encrypt(rawCred)
	if err != nil {
		return nil, fmt.Errorf("encrypt credential: %w", err)
	}
	c := &storage.Channel{
		Name:             in.Name,
		Type:             in.Type,
		SiteURL:          in.SiteURL,
		RechargeURL:      rechargeURL,
		Username:         in.Username,
		PasswordCipher:   enc,
		CredentialMode:   mode,
		TurnstileEnabled: in.TurnstileEnabled && mode == storage.CredentialModePassword, // token 模式不需要打码
		CaptchaConfigID:  in.CaptchaConfigID,
		BalanceThreshold: in.BalanceThreshold,
		MonitorEnabled:   in.MonitorEnabled,
	}
	if mode == storage.CredentialModeToken {
		// token 模式不依赖打码 provider
		c.CaptchaConfigID = nil
	}
	if err := s.Channels.Create(c); err != nil {
		return nil, err
	}
	return c, nil
}

// UpdateInput 编辑渠道的可选字段。Password / TokenCredential 为空表示不修改凭据。
type UpdateInput struct {
	Name             *string
	SiteURL          *string
	RechargeURL      *string
	Username         *string
	Password         *string
	CredentialMode   *storage.CredentialMode
	TokenCredential  *string // JSON
	TurnstileEnabled *bool
	CaptchaConfigID  *uint
	BalanceThreshold *float64
	MonitorEnabled   *bool
}

func (s *Service) Update(id uint, in UpdateInput) (*storage.Channel, error) {
	c, err := s.Channels.FindByID(id)
	if err != nil {
		return nil, err
	}
	if in.Name != nil {
		c.Name = *in.Name
	}
	if in.SiteURL != nil {
		c.SiteURL = *in.SiteURL
	}
	if in.RechargeURL != nil {
		rechargeURL, err := normalizeRechargeURL(*in.RechargeURL)
		if err != nil {
			return nil, err
		}
		c.RechargeURL = rechargeURL
	}
	if in.Username != nil {
		c.Username = *in.Username
	}

	// 决定本次更新后的最终凭据模式。
	finalMode := c.CredentialMode
	if in.CredentialMode != nil && *in.CredentialMode != "" {
		finalMode = *in.CredentialMode
	}
	if finalMode == "" {
		finalMode = storage.CredentialModePassword
	}

	// 是否切换了模式 → 强制重写凭据并清空 session
	modeChanged := finalMode != c.CredentialMode

	var rawCred string
	switch finalMode {
	case storage.CredentialModePassword:
		if in.Password != nil && *in.Password != "" {
			rawCred = *in.Password
		} else if modeChanged {
			return nil, errors.New("切换到账号密码模式时必须填写密码")
		}
	case storage.CredentialModeToken:
		if in.TokenCredential != nil && *in.TokenCredential != "" {
			rawCred = *in.TokenCredential
		} else if modeChanged {
			return nil, errors.New("切换到 token 模式时必须填写凭据")
		}
	default:
		return nil, fmt.Errorf("unknown credential mode: %s", finalMode)
	}

	if rawCred != "" {
		if err := validateCredential(c.Type, finalMode, rawCred); err != nil {
			return nil, err
		}
		enc, err := s.Cipher.Encrypt(rawCred)
		if err != nil {
			return nil, fmt.Errorf("encrypt credential: %w", err)
		}
		c.PasswordCipher = enc
		c.CredentialMode = finalMode
		// 凭据或模式变了，强制下次重新构造 session
		_ = s.AuthSessions.Delete(c.ID)
	} else if modeChanged {
		// 理论上面已挡住，这里兜底
		return nil, errors.New("凭据模式变更必须同时提供新凭据")
	}

	if in.TurnstileEnabled != nil {
		c.TurnstileEnabled = *in.TurnstileEnabled && finalMode == storage.CredentialModePassword
	}
	if in.CaptchaConfigID != nil {
		if finalMode == storage.CredentialModePassword {
			c.CaptchaConfigID = in.CaptchaConfigID
		} else {
			c.CaptchaConfigID = nil
		}
	} else if finalMode == storage.CredentialModeToken {
		// token 模式强制清空打码绑定
		c.CaptchaConfigID = nil
	}
	if in.BalanceThreshold != nil {
		c.BalanceThreshold = *in.BalanceThreshold
	}
	if in.MonitorEnabled != nil {
		c.MonitorEnabled = *in.MonitorEnabled
	}
	if err := s.Channels.Update(c); err != nil {
		return nil, err
	}
	return c, nil
}

// normalizeRechargeURL 统一充值链接的空值和安全协议语义。
// 该链接只会作为通知内容展示，不会由服务端主动访问。
func normalizeRechargeURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	if len(value) > 2048 {
		return "", fmt.Errorf("%w：不能超过 2048 个字符", ErrInvalidRechargeURL)
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("%w：必须是有效的 http/https 地址", ErrInvalidRechargeURL)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("%w：不能包含用户名或密码", ErrInvalidRechargeURL)
	}
	return value, nil
}

// selectRawCredential 在 Create 时根据 mode 决定要落库的明文凭据字符串。
func selectRawCredential(mode storage.CredentialMode, password, tokenCredential string) (string, error) {
	switch mode {
	case storage.CredentialModePassword:
		if password == "" {
			return "", errors.New("账号密码模式下密码不能为空")
		}
		return password, nil
	case storage.CredentialModeToken:
		if tokenCredential == "" {
			return "", errors.New("token 模式下必须提供凭据")
		}
		return tokenCredential, nil
	default:
		return "", fmt.Errorf("unknown credential mode: %s", mode)
	}
}

// validateCredential 在保存前对凭据做语法 / 必填字段校验，能尽早把无效输入挡在 connector 外。
//
// 注意：这里只做语法层校验，不做"凭据是否真的有效"的网络验证——
// 那个交给后续 TestLogin / 第一次同步去发现。
func validateCredential(channelType storage.ChannelType, mode storage.CredentialMode, raw string) error {
	if mode != storage.CredentialModeToken {
		return nil
	}
	switch channelType {
	case storage.ChannelTypeNewAPI:
		var cred NewAPITokenCredential
		if err := json.Unmarshal([]byte(raw), &cred); err != nil {
			return fmt.Errorf("解析 NewAPI 凭据 JSON 失败：%w", err)
		}
		if strings.TrimSpace(cred.Cookie) == "" {
			return errors.New("NewAPI token 模式需要 Cookie")
		}
		if strings.TrimSpace(cred.UserID) == "" {
			return errors.New("NewAPI token 模式需要 User ID（在 NewAPI 个人设置页查看）")
		}
	case storage.ChannelTypeSub2API:
		var cred Sub2APITokenCredential
		if err := json.Unmarshal([]byte(raw), &cred); err != nil {
			return fmt.Errorf("解析 Sub2API 凭据 JSON 失败：%w", err)
		}
		if strings.TrimSpace(cred.AccessToken) == "" {
			return errors.New("Sub2API token 模式需要 access_token")
		}
	default:
		return fmt.Errorf("unknown channel type: %s", channelType)
	}
	return nil
}

func (s *Service) Delete(id uint) error {
	_ = s.AuthSessions.Delete(id)
	return s.Channels.Delete(id)
}

// Resolve 把存储层的加密渠道解密成 connector 可用的 Channel。
//
// 注意：这一步**不**求解 Turnstile —— 打码只在真正要登录时做（见 prepareTurnstile），
// 复用现有 session 的路径无需任何打码消耗。
//
// token 模式下 connector.Channel.Password 留空——connector 永远不会读到它。
func (s *Service) Resolve(ctx context.Context, c *storage.Channel) (*connector.Channel, error) {
	_ = ctx
	raw, err := s.Cipher.Decrypt(c.PasswordCipher)
	if err != nil {
		return nil, fmt.Errorf("decrypt credential: %w", err)
	}
	resolved := &connector.Channel{
		ID:               c.ID,
		Name:             c.Name,
		Type:             connector.ChannelType(c.Type),
		SiteURL:          c.SiteURL,
		Username:         c.Username,
		TurnstileEnabled: c.TurnstileEnabled,
	}
	if c.CredentialMode == storage.CredentialModeToken {
		// token 模式：raw 是 JSON，Password 留空避免被 connector 误用
		resolved.Password = ""
	} else {
		resolved.Password = raw
	}
	return resolved, nil
}

// buildSessionFromToken 在 token 模式下，把用户提供的凭据 JSON 解析成 AuthSession。
// 不发任何 HTTP 请求——失效检测留给 connector.CheckAuth + 后续 GetBalance / GetRates。
func (s *Service) buildSessionFromToken(c *storage.Channel) (*connector.AuthSession, error) {
	raw, err := s.Cipher.Decrypt(c.PasswordCipher)
	if err != nil {
		return nil, fmt.Errorf("decrypt credential: %w", err)
	}
	switch c.Type {
	case storage.ChannelTypeNewAPI:
		var cred NewAPITokenCredential
		if err := json.Unmarshal([]byte(raw), &cred); err != nil {
			return nil, fmt.Errorf("parse newapi token credential: %w", err)
		}
		return &connector.AuthSession{
			UserID:    cred.UserID,
			Cookie:    cred.Cookie,
			ExpiresAt: time.Now().Add(tokenSessionTTL),
		}, nil
	case storage.ChannelTypeSub2API:
		var cred Sub2APITokenCredential
		if err := json.Unmarshal([]byte(raw), &cred); err != nil {
			return nil, fmt.Errorf("parse sub2api token credential: %w", err)
		}
		return &connector.AuthSession{
			AccessToken: cred.AccessToken,
			ExpiresAt:   time.Now().Add(tokenSessionTTL),
		}, nil
	default:
		return nil, fmt.Errorf("unknown channel type: %s", c.Type)
	}
}

// prepareTurnstile 在调用 conn.Login 之前求解 Turnstile token。
// 没启用 turnstile 或者上游 site 公开接口说"未开启 Turnstile"时是空操作。
func (s *Service) prepareTurnstile(
	ctx context.Context,
	c *storage.Channel,
	resolved *connector.Channel,
	conn connector.Connector,
) error {
	if !c.TurnstileEnabled || c.CaptchaConfigID == nil {
		return nil
	}
	progress.Start(ctx, progress.StageCaptcha, "求解 Turnstile…")
	siteKey, err := conn.GetTurnstileSiteKey(ctx, resolved)
	if err != nil {
		progress.Fail(ctx, progress.StageCaptcha, err.Error())
		return fmt.Errorf("fetch turnstile site key: %w", err)
	}
	if siteKey == "" {
		progress.OK(ctx, progress.StageCaptcha, "上游未开启 Turnstile，跳过")
		return nil
	}
	token, err := s.solveCaptcha(ctx, *c.CaptchaConfigID, siteKey, c.SiteURL)
	if err != nil {
		progress.Fail(ctx, progress.StageCaptcha, err.Error())
		return fmt.Errorf("solve captcha: %w", err)
	}
	resolved.TurnstileToken = token
	progress.OK(ctx, progress.StageCaptcha, "打码完成")
	return nil
}

func (s *Service) solveCaptcha(ctx context.Context, captchaID uint, siteKey, pageURL string) (string, error) {
	cfg, err := s.Captchas.FindByID(captchaID)
	if err != nil {
		return "", err
	}
	if !cfg.Enabled {
		return "", errors.New("captcha config disabled")
	}
	apiKey, err := s.Cipher.Decrypt(cfg.APIKeyCipher)
	if err != nil {
		return "", err
	}
	provider, err := captcha.Build(cfg, apiKey)
	if err != nil {
		return "", err
	}
	return provider.SolveTurnstile(ctx, siteKey, pageURL)
}

// EnsureSession 优先复用未过期的 session，否则重新登录并加密回写。
//
// token 模式：
//   - 跳过 AuthSessions 表与 Login 调用
//   - 每次构造一个临时 AuthSession（基于用户提供的凭据）返回
//   - CheckAuth 用来发现 token 是否还有效；失效会在 last_error 显示
func (s *Service) EnsureSession(
	ctx context.Context,
	c *storage.Channel,
	resolved *connector.Channel,
	conn connector.Connector,
) (*connector.AuthSession, error) {
	if c.CredentialMode == storage.CredentialModeToken {
		progress.Start(ctx, progress.StageSession, "使用用户提供的 token…")
		session, err := s.buildSessionFromToken(c)
		if err != nil {
			progress.Fail(ctx, progress.StageSession, err.Error())
			_ = s.Channels.SetLastError(c.ID, err.Error())
			return nil, err
		}
		// 走一次 CheckAuth 确认 token 仍有效。失败立即标 last_error，调用方往上抛错。
		if err := conn.CheckAuth(ctx, resolved, session); err != nil {
			msg := "token 已失效，请重新粘贴凭据：" + err.Error()
			progress.Fail(ctx, progress.StageSession, msg)
			_ = s.Channels.SetLastError(c.ID, msg)
			return nil, errors.New(msg)
		}
		_ = s.Channels.SetLastError(c.ID, "")
		progress.OK(ctx, progress.StageSession, "token 有效，跳过登录")
		return session, nil
	}

	saved, err := s.AuthSessions.FindByChannel(c.ID)
	if err != nil {
		return nil, err
	}
	if saved != nil && saved.ExpiresAt != nil && time.Until(*saved.ExpiresAt) > SessionRefreshThreshold {
		session, err := s.decryptSession(saved)
		if err != nil {
			return nil, err
		}
		// 轻量校验现有 session，不通过则继续走重新登录。
		progress.Start(ctx, progress.StageSession, "校验已有会话…")
		if err := conn.CheckAuth(ctx, resolved, session); err == nil {
			progress.OK(ctx, progress.StageSession, "复用现有会话")
			return session, nil
		}
		progress.OK(ctx, progress.StageSession, "会话已失效，重新登录")
	}
	return s.login(ctx, c, resolved, conn)
}

func (s *Service) login(
	ctx context.Context,
	c *storage.Channel,
	resolved *connector.Channel,
	conn connector.Connector,
) (*connector.AuthSession, error) {
	if err := s.prepareTurnstile(ctx, c, resolved, conn); err != nil {
		return nil, err
	}
	progress.Start(ctx, progress.StageLogin, "登录上游…")
	started := time.Now()
	session, err := conn.Login(ctx, resolved)
	finished := time.Now()
	_ = s.MonitorLogs.Append(&storage.MonitorLog{
		ChannelID:    c.ID,
		Job:          storage.MonitorJobLogin,
		Success:      err == nil,
		ErrorMessage: errString(err),
		StartedAt:    started,
		FinishedAt:   finished,
	})
	if err != nil {
		progress.Fail(ctx, progress.StageLogin, err.Error())
		_ = s.Channels.SetLastError(c.ID, err.Error())
		return nil, err
	}
	if err := s.persistSession(c.ID, session); err != nil {
		progress.Fail(ctx, progress.StageLogin, err.Error())
		return nil, err
	}
	_ = s.Channels.SetLastError(c.ID, "")
	progress.OK(ctx, progress.StageLogin, "登录成功")
	return session, nil
}

func (s *Service) persistSession(channelID uint, session *connector.AuthSession) error {
	acc, err := s.Cipher.Encrypt(session.AccessToken)
	if err != nil {
		return fmt.Errorf("encrypt access token: %w", err)
	}
	cookie, err := s.Cipher.Encrypt(session.Cookie)
	if err != nil {
		return fmt.Errorf("encrypt cookie: %w", err)
	}
	csrf, err := s.Cipher.Encrypt(session.CSRFToken)
	if err != nil {
		return fmt.Errorf("encrypt csrf: %w", err)
	}
	now := time.Now()
	expires := session.ExpiresAt
	return s.AuthSessions.Upsert(&storage.AuthSession{
		ChannelID:         channelID,
		UserID:            session.UserID,
		AccessTokenCipher: acc,
		CookieCipher:      cookie,
		CSRFTokenCipher:   csrf,
		ExpiresAt:         &expires,
		LastLoginAt:       &now,
	})
}

func (s *Service) decryptSession(saved *storage.AuthSession) (*connector.AuthSession, error) {
	acc, err := s.Cipher.Decrypt(saved.AccessTokenCipher)
	if err != nil {
		return nil, fmt.Errorf("decrypt access token: %w", err)
	}
	cookie, err := s.Cipher.Decrypt(saved.CookieCipher)
	if err != nil {
		return nil, fmt.Errorf("decrypt cookie: %w", err)
	}
	csrf, err := s.Cipher.Decrypt(saved.CSRFTokenCipher)
	if err != nil {
		return nil, fmt.Errorf("decrypt csrf: %w", err)
	}
	expires := time.Time{}
	if saved.ExpiresAt != nil {
		expires = *saved.ExpiresAt
	}
	return &connector.AuthSession{
		UserID:      saved.UserID,
		AccessToken: acc,
		Cookie:      cookie,
		CSRFToken:   csrf,
		ExpiresAt:   expires,
	}, nil
}

// TestLogin 手动测试登录：
//   - password 模式：复用 login() 的完整流程（打码 → 登录 → 持久化）
//   - token 模式：直接走 EnsureSession，等同于检查 CheckAuth 是否通过
func (s *Service) TestLogin(ctx context.Context, channelID uint) error {
	c, err := s.Channels.FindByID(channelID)
	if err != nil {
		return err
	}
	resolved, err := s.Resolve(ctx, c)
	if err != nil {
		return err
	}
	conn, err := connector.For(connector.ChannelType(c.Type))
	if err != nil {
		return err
	}
	if c.CredentialMode == storage.CredentialModeToken {
		_, err = s.EnsureSession(ctx, c, resolved, conn)
		return err
	}
	_, err = s.login(ctx, c, resolved, conn)
	return err
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
