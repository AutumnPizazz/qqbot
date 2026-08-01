package admin

import (
	"net/http/httptest"
	"testing"

	"qqbot/internal/state"
)

// newTestServerWithProxy 构造带可选可信代理的 Server。
func newTestServerWithProxy(t *testing.T, proxies []string) *Server {
	t.Helper()
	svc, err := state.Open(t.TempDir(), state.NewTestMasterKey())
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{DataDir: t.TempDir(), Service: svc, Keys: state.NewTestMasterKey(), TrustedProxies: proxies})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTrustedProxyClientIP(t *testing.T) {
	// 无信任代理：一律取 RemoteAddr，忽略 X-Forwarded-For
	s := newTestServerWithProxy(t, nil)
	r := httptest.NewRequest("GET", "http://x/", nil)
	r.RemoteAddr = "1.2.3.4:5678"
	r.Header.Set("X-Forwarded-For", "9.9.9.9")
	if got := s.clientIP(r); got != "1.2.3.4" {
		t.Fatalf("无信任代理时应取 RemoteAddr，实际 %s", got)
	}

	// 配置信任代理后：来自代理的请求取 X-Forwarded-For
	s2 := newTestServerWithProxy(t, []string{"10.0.0.2"})
	r2 := httptest.NewRequest("GET", "http://x/", nil)
	r2.RemoteAddr = "10.0.0.2:1234"
	r2.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.2")
	if got := s2.clientIP(r2); got != "203.0.113.7" {
		t.Fatalf("应取 X-Forwarded-For 首个 IP，实际 %s", got)
	}

	// 非代理来源（即使带 XFF）不信任
	r3 := httptest.NewRequest("GET", "http://x/", nil)
	r3.RemoteAddr = "203.0.113.99:9999"
	r3.Header.Set("X-Forwarded-For", "6.6.6.6")
	if got := s2.clientIP(r3); got != "203.0.113.99" {
		t.Fatalf("非代理来源应取 RemoteAddr，实际 %s", got)
	}

	// 信任代理但无 XFF 头：回退 RemoteAddr
	r4 := httptest.NewRequest("GET", "http://x/", nil)
	r4.RemoteAddr = "10.0.0.2:1234"
	if got := s2.clientIP(r4); got != "10.0.0.2" {
		t.Fatalf("无 XFF 时应回退 RemoteAddr，实际 %s", got)
	}

	// CIDR 网段匹配
	s3 := newTestServerWithProxy(t, []string{"172.16.0.0/12"})
	r5 := httptest.NewRequest("GET", "http://x/", nil)
	r5.RemoteAddr = "172.18.0.5:1234"
	r5.Header.Set("X-Forwarded-For", "198.51.100.9")
	if got := s3.clientIP(r5); got != "198.51.100.9" {
		t.Fatalf("CIDR 信任应取 XFF，实际 %s", got)
	}
	// CIDR 之外来源不信任
	r6 := httptest.NewRequest("GET", "http://x/", nil)
	r6.RemoteAddr = "10.99.0.5:1234"
	r6.Header.Set("X-Forwarded-For", "6.6.6.6")
	if got := s3.clientIP(r6); got != "10.99.0.5" {
		t.Fatalf("CIDR 外来源应取 RemoteAddr，实际 %s", got)
	}
}
