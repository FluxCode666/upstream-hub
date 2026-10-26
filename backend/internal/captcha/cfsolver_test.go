package captcha

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCFSolver 造一个最小可执行包结构：run.sh 吐一行 JSON，transport/runtime 只要求存在。
func fakeCFSolver(t *testing.T, script string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "runtime", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "transport"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "runtime", "bin", "node"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "transport", "go-transport"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\n" + script + "\n"
	if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestCFSolverSuccess(t *testing.T) {
	root := fakeCFSolver(t, `printf '%s\n' '{"status":"ok","turnstile_token":"TOKEN-ABC"}'`)
	p := newCFSolver(Config{APIKey: "enabled", Endpoint: root})

	tok, err := p.SolveTurnstile(context.Background(), "0xSITEKEY", "https://example.com/login?x=1")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if tok != "TOKEN-ABC" {
		t.Fatalf("token = %q", tok)
	}
}

func TestCFSolverOriginOnlyArg(t *testing.T) {
	// 断言交给 cf-solver 的必须是 origin，不带 path/query。
	root := fakeCFSolver(t, `for a in "$@"; do echo "$a" >> "$0.args"; done
printf '%s\n' '{"turnstile_token":"T"}'`)
	p := newCFSolver(Config{APIKey: "enabled", Endpoint: root})
	if _, err := p.SolveTurnstile(context.Background(), "k", "https://example.com/very/deep?q=1"); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "run.sh.args"))
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Fields(string(raw))
	var seenOrigin string
	for i, a := range args {
		if a == "-origin-solve" && i+1 < len(args) {
			seenOrigin = args[i+1]
		}
	}
	if seenOrigin != "https://example.com" {
		t.Fatalf("origin arg = %q, args=%v", seenOrigin, args)
	}
}

func TestCFSolverExitNonZero(t *testing.T) {
	root := fakeCFSolver(t, `echo '{"error":"solve timeout"}' 1>&2; exit 1`)
	p := newCFSolver(Config{APIKey: "enabled", Endpoint: root})
	_, err := p.SolveTurnstile(context.Background(), "k", "https://example.com")
	if err == nil || !strings.Contains(err.Error(), "exit=1") {
		t.Fatalf("err = %v", err)
	}
}

func TestCFSolverEmptyKeyRejected(t *testing.T) {
	root := fakeCFSolver(t, `printf '%s\n' '{"turnstile_token":"T"}'`)
	p := newCFSolver(Config{Endpoint: root})
	if _, err := p.SolveTurnstile(context.Background(), "k", "https://example.com"); err == nil {
		t.Fatal("want error for empty api key")
	}
}

func TestCFSolverMissingPackage(t *testing.T) {
	p := newCFSolver(Config{APIKey: "enabled", Endpoint: filepath.Join(t.TempDir(), "nope")})
	_, err := p.SolveTurnstile(context.Background(), "k", "https://example.com")
	if err == nil || !strings.Contains(err.Error(), "找不到") {
		t.Fatalf("err = %v", err)
	}
}

func TestCFSolverBadURL(t *testing.T) {
	root := fakeCFSolver(t, `printf '%s\n' '{"turnstile_token":"T"}'`)
	p := newCFSolver(Config{APIKey: "enabled", Endpoint: root})
	if _, err := p.SolveTurnstile(context.Background(), "k", "not-a-url"); err == nil {
		t.Fatal("want error for bad page url")
	}
}

func TestParseCFSolverTokenShapes(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{`{"turnstile_token":"A"}`, "A", true},
		{"log line\n{\"turnstile_token\":\"B\"}\n", "B", true},
		{`{"solution":{"token":"C"}}`, "C", true},
		{`{"token":"D"}`, "D", true},
		{`{"cf_clearance":"x"}`, "", false},
		{`not json`, "", false},
	}
	for _, c := range cases {
		got, err := parseCFSolverToken(c.in)
		if c.ok && (err != nil || got != c.want) {
			t.Fatalf("in=%q got=%q err=%v", c.in, got, err)
		}
		if !c.ok && err == nil {
			t.Fatalf("in=%q expected error", c.in)
		}
	}
}

func TestOriginOf(t *testing.T) {
	u, err := originOf("https://abc.example.com:8443/a/b?c=d")
	if err != nil || u != "https://abc.example.com:8443" {
		t.Fatalf("got %q err %v", u, err)
	}
	if _, err := originOf("/relative"); err == nil {
		t.Fatal("want error")
	}
}

func TestCFSolverExtraBounds(t *testing.T) {
	p := newCFSolver(Config{APIKey: "x", Extra: `{"timeout_sec":5,"concurrency":3,"package_root":"/tmp/x"}`})
	if p.timeout().Seconds() != 20 {
		t.Fatalf("timeout clamp low = %v", p.timeout())
	}
	if p.root != "/tmp/x" {
		t.Fatalf("root = %q", p.root)
	}
	// Extra 非法 JSON 不应 panic，退回默认包目录
	p2 := newCFSolver(Config{APIKey: "x", Extra: "{bad"})
	if p2.root == "" {
		t.Fatal("root empty")
	}
}
