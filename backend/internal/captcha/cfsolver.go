package captcha

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/worryzyy/upstream-hub/internal/storage"
)

func init() {
	Register(storage.CaptchaCapSolverLocal, func(c Config) Provider { return newCFSolver(c) })
}

// cfsolver 把 cf-solver 便携包（Linux x64，自带 Node 22 + Go transport）包成一个本地 Turnstile provider。
//
// 与其它 provider 的差异：
//   - APIKey 当"启动开关签名"用：非空即认为配置完整；空值一律拒绝，避免误跑。
//   - Endpoint 是 cf-solver 包根目录（含 run.sh / transport / runtime），可在 Extra 里
//     用 {"package_root": "..."} 覆盖，便于同一进程挂多份包。
//   - Extra 支持的键：
//     package_root  覆盖包目录
//     timeout_sec   单次求解超时，默认 60，下限 20，上限 300
//     node_bin      覆盖 runtime/bin/node（调试用）
//     extra_args    []string，追加到 run.sh 之后的原样参数
//
// 求解走非 URL 模式：-mode solve -origin-solve <origin> -sitekey <siteKey>，
// 成功时 stdout 单行 JSON 里取 turnstile_token；退出码 0/1/2 语义见其 README。
//
// 并发控制：cf-solver 单次会拉起 Node + Go 双进程，吃 CPU 与内存，这里做进程级信号量，
// 默认并发 1，可在 Extra 里用 concurrency 调整。
type cfsolver struct {
	cfg  Config
	root string
	opts cfsolverOptions
}

type cfsolverOptions struct {
	PackageRoot string   `json:"package_root"`
	NodeBin     string   `json:"node_bin"`
	TimeoutSec  int      `json:"timeout_sec"`
	Concurrency int      `json:"concurrency"`
	ExtraArgs   []string `json:"extra_args"`
}

const (
	cfsolverDefaultTimeout = 60 * time.Second
	cfsolverMinTimeout     = 20 * time.Second
	cfsolverMaxTimeout     = 300 * time.Second
	// 包目录查找顺序：Extra.package_root > Endpoint > 环境变量 > 默认约定位置。
	cfsolverRootEnv = "UPSTREAMHUB_CFSOLVER_ROOT"
	cfsolverRootDef = "/app/cf-solver"
)

var (
	cfsolverSlots sync.Map // root -> chan struct{}
	cfsolverMu    sync.Mutex
)

func newCFSolver(c Config) *cfsolver {
	var opts cfsolverOptions
	if strings.TrimSpace(c.Extra) != "" {
		_ = json.Unmarshal([]byte(c.Extra), &opts) // 解析失败退回默认值，不阻断构建
	}
	root := firstNonEmpty(opts.PackageRoot, strings.TrimSpace(c.Endpoint), os.Getenv(cfsolverRootEnv), cfsolverRootDef)
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return &cfsolver{cfg: c, root: root, opts: opts}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (p *cfsolver) timeout() time.Duration {
	t := time.Duration(p.opts.TimeoutSec) * time.Second
	switch {
	case t <= 0:
		return cfsolverDefaultTimeout
	case t < cfsolverMinTimeout:
		return cfsolverMinTimeout
	case t > cfsolverMaxTimeout:
		return cfsolverMaxTimeout
	default:
		return t
	}
}

func (p *cfsolver) runScript() string {
	return filepath.Join(p.root, "run.sh")
}

// validate 在真正开跑前做一次本地体检，错误信息可直接暴露到前端进度里。
func (p *cfsolver) validate() error {
	if p.cfg.APIKey == "" {
		return errors.New("cfsolver: api key 为空，配置视为未启用")
	}
	script := p.runScript()
	st, err := os.Stat(script)
	if err != nil {
		return fmt.Errorf("cfsolver: 找不到 %s（检查 Endpoint / %s）: %w", script, cfsolverRootEnv, err)
	}
	if st.IsDir() {
		return fmt.Errorf("cfsolver: %s 是目录", script)
	}
	if st.Mode()&0o111 == 0 {
		return fmt.Errorf("cfsolver: %s 缺少可执行权限，请 chmod +x", script)
	}
	node := firstNonEmpty(p.opts.NodeBin, filepath.Join(p.root, "runtime", "bin", "node"))
	if _, err := os.Stat(node); err != nil {
		return fmt.Errorf("cfsolver: 包内 node 缺失（%s），包可能未完整解压", node)
	}
	if _, err := os.Stat(filepath.Join(p.root, "transport", "go-transport")); err != nil {
		return fmt.Errorf("cfsolver: transport/go-transport 缺失，包不完整")
	}
	return nil
}

func (p *cfsolver) slot() chan struct{} {
	n := p.opts.Concurrency
	if n <= 0 {
		n = 1
	}
	cfsolverMu.Lock()
	defer cfsolverMu.Unlock()
	if ch, ok := cfsolverSlots.Load(p.root); ok {
		return ch.(chan struct{})
	}
	ch := make(chan struct{}, n)
	cfsolverSlots.Store(p.root, ch)
	return ch
}

func (p *cfsolver) SolveTurnstile(ctx context.Context, siteKey, pageURL string) (string, error) {
	if siteKey == "" {
		return "", errors.New("cfsolver: siteKey is empty")
	}
	if err := p.validate(); err != nil {
		return "", err
	}
	origin, err := originOf(pageURL)
	if err != nil {
		return "", fmt.Errorf("cfsolver: %w", err)
	}

	runCtx, cancel := context.WithTimeout(ctx, p.timeout())
	defer cancel()

	slots := p.slot()
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-runCtx.Done():
		return "", fmt.Errorf("cfsolver: 等待空闲槽位超时: %w", runCtx.Err())
	}

	args := []string{
		"-mode", "solve",
		"-origin-solve", origin,
		"-sitekey", siteKey,
	}
	args = append(args, p.opts.ExtraArgs...)

	cmd := exec.CommandContext(runCtx, p.runScript(), args...) // #nosec G204 -- 包路径来自管理员配置
	cmd.Dir = p.root
	env := os.Environ()
	if p.opts.NodeBin != "" {
		env = append(env, "PATH="+filepath.Dir(p.opts.NodeBin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	if runCtx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("cfsolver: 求解超过 %s", p.timeout())
	}
	token, parseErr := parseCFSolverToken(stdout.String())

	switch {
	case runErr != nil:
		code := -1
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			code = ee.ExitCode()
		}
		// 退出码 1 = 流程未通过但进程正常；此时 stdout 里通常仍带 error 字段。
		if token != "" {
			return token, nil
		}
		return "", fmt.Errorf("cfsolver: exit=%d %s%s", code, tailString(stderr.String(), 400), parseDetail(parseErr, stdout.String()))
	case parseErr != nil:
		return "", fmt.Errorf("cfsolver: 解析输出失败: %w%s", parseErr, parseDetail(nil, stdout.String()))
	default:
		return token, nil
	}
}

// parseCFSolverToken 从 stdout 里找 turnstile_token。
//
// 约定是"单行 JSON"，但真实包里可能有调试行混入，所以按行从后往前找第一个含
// turnstile_token 的合法 JSON 对象；同时兼容几种常见键名。
func parseCFSolverToken(out string) (string, error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || line[0] != '{' {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		for _, k := range []string{"turnstile_token", "token"} {
			if v, ok := m[k].(string); ok && v != "" {
				return v, nil
			}
		}
		// solution.token（与 API 类打码商同构）也认
		if sol, ok := m["solution"].(map[string]any); ok {
			if v, ok := sol["token"].(string); ok && v != "" {
				return v, nil
			}
		}
		return "", errors.New("输出 JSON 中没有 turnstile_token")
	}
	return "", errors.New("stdout 中没有 JSON 输出")
}

func parseDetail(err error, out string) string {
	if err == nil {
		return ""
	}
	body := strings.TrimSpace(out)
	if body != "" {
		return "；stdout=" + tailString(body, 400)
	}
	return "；" + err.Error()
}

func tailString(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// originOf 把 pageURL 收敛成 scheme://host[:port]，cf-solver 的 -origin-solve 只吃 origin。
func originOf(pageURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(pageURL))
	if err != nil {
		return "", fmt.Errorf("parse page url: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("page url 非法: %q", pageURL)
	}
	return u.Scheme + "://" + u.Host, nil
}
