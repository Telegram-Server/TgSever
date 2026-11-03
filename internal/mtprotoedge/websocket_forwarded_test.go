package mtprotoedge

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"testing"
	"time"
)

func TestAdmissionTicketRekeyMovesPerIPCount(t *testing.T) {
	a := newAdmissionController(10, 2, 1)
	proxy := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 40000}
	client := &net.TCPAddr{IP: net.ParseIP("203.0.113.7")}
	full := &net.TCPAddr{IP: net.ParseIP("203.0.113.8")}

	ticket, ok := a.acquire(proxy)
	if !ok {
		t.Fatal("proxy connection rejected")
	}
	if !ticket.rekey(client) {
		t.Fatal("rekey to a fresh client IP rejected")
	}
	assertByIP(t, a, map[string]int{"203.0.113.7": 1})

	// 目标 IP 已满：不改动，调用方拒绝连接。
	for i := 0; i < 2; i++ {
		if _, ok := a.acquire(full); !ok {
			t.Fatalf("seed connection %d for full IP rejected", i)
		}
	}
	if ticket.rekey(full) {
		t.Fatal("rekey onto an IP at its per-IP cap succeeded")
	}
	assertByIP(t, a, map[string]int{"203.0.113.7": 1, "203.0.113.8": 2})

	ticket.release()
	ticket.release()
	assertByIP(t, a, map[string]int{"203.0.113.8": 2})
	if ticket.rekey(client) {
		t.Fatal("rekey after release succeeded")
	}
	a.mu.Lock()
	connections := a.connections
	a.mu.Unlock()
	if connections != 2 {
		t.Fatalf("connections = %d, want 2", connections)
	}
}

func assertByIP(t *testing.T, a *admissionController, want map[string]int) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.byIP) != len(want) {
		t.Fatalf("byIP = %v, want %v", a.byIP, want)
	}
	for ip, n := range want {
		if a.byIP[ip] != n {
			t.Fatalf("byIP = %v, want %v", a.byIP, want)
		}
	}
}

func TestForwardedClientAddr(t *testing.T) {
	loopback := []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("::1/128")}
	tests := []struct {
		name    string
		trusted []netip.Prefix
		peer    string
		xff     []string
		want    string
	}{
		{name: "no trusted proxies configured", peer: "127.0.0.1:5000", xff: []string{"203.0.113.7"}},
		{name: "untrusted peer cannot spoof", trusted: loopback, peer: "198.51.100.1:5000", xff: []string{"203.0.113.7"}},
		{name: "trusted peer without header", trusted: loopback, peer: "127.0.0.1:5000"},
		{name: "trusted peer forwards client", trusted: loopback, peer: "127.0.0.1:5000", xff: []string{"203.0.113.7"}, want: "203.0.113.7"},
		{name: "rightmost entry wins over a client-supplied one", trusted: loopback, peer: "127.0.0.1:5000", xff: []string{"6.6.6.6, 203.0.113.7"}, want: "203.0.113.7"},
		{name: "trusted hops are skipped", trusted: loopback, peer: "127.0.0.1:5000", xff: []string{"203.0.113.7, 127.0.0.1"}, want: "203.0.113.7"},
		{name: "repeated headers are one chain", trusted: loopback, peer: "127.0.0.1:5000", xff: []string{"6.6.6.6", "203.0.113.7"}, want: "203.0.113.7"},
		{name: "ipv6 proxy and client", trusted: loopback, peer: "[::1]:5000", xff: []string{"2001:db8::7"}, want: "2001:db8::7"},
		{name: "mapped ipv4 is unmapped", trusted: loopback, peer: "127.0.0.1:5000", xff: []string{"::ffff:203.0.113.7"}, want: "203.0.113.7"},
		{name: "garbage entry is not believed", trusted: loopback, peer: "127.0.0.1:5000", xff: []string{"203.0.113.7, not-an-ip"}},
		{name: "only trusted hops", trusted: loopback, peer: "127.0.0.1:5000", xff: []string{"127.0.0.1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := http.NewRequest(http.MethodGet, "http://dmweb.cc/apiws", nil)
			if err != nil {
				t.Fatal(err)
			}
			r.RemoteAddr = tt.peer
			for _, v := range tt.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			got, ok := forwardedClientAddr(r, tt.trusted)
			if tt.want == "" {
				if ok {
					t.Fatalf("forwardedClientAddr = %v, want not believed", got)
				}
				return
			}
			if !ok {
				t.Fatalf("forwardedClientAddr not believed, want %s", tt.want)
			}
			host, _, err := net.SplitHostPort(got.String())
			if err != nil || host != tt.want {
				t.Fatalf("forwardedClientAddr = %v, want %s", got, tt.want)
			}
		})
	}
}

// 接线：经 准入 → 同端口分流 → HTTP 升级 的真实连接，受信反代转发的真实 IP 要落到
// hijack 出来的连接上（gotd 升级处理器取的就是它），单 IP 计数也跟着挪过去；
// 真实 IP 已满时升级被拒。
func TestWebSocketUpgradeFromTrustedProxyUsesForwardedClient(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	admission := newAdmissionController(100, 2, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mux := newSamePortMux(admission.wrapListener(ln), 5*time.Second)
	hijacked := make(chan string, 4)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			hijacked <- "hijack error: " + err.Error()
			return
		}
		hijacked <- conn.RemoteAddr().String()
		_, _ = conn.Write([]byte("HTTP/1.1 204 No Content\r\n\r\n"))
		// 保持连接打开，让准入计数在断言期间仍然有效。
		go func() { <-ctx.Done(); _ = conn.Close() }()
	})
	httpServer := &http.Server{
		Handler:           websocketRouteHandler(inner, nil, []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}),
		ReadHeaderTimeout: 5 * time.Second,
		ConnContext:       websocketConnContext,
	}
	serveErr := make(chan error, 2)
	go func() { serveErr <- mux.Serve(ctx) }()
	go func() {
		err := httpServer.Serve(mux.HTTP())
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			err = nil
		}
		serveErr <- err
	}()
	defer func() {
		cancel()
		_ = mux.Close()
		_ = httpServer.Close()
		for i := 0; i < 2; i++ {
			if err := <-serveErr; err != nil {
				t.Errorf("serve: %v", err)
			}
		}
	}()

	upgrade := func(forwardedFor string) int {
		t.Helper()
		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		fmt.Fprintf(conn, "GET /apiws HTTP/1.1\r\nHost: dmweb.cc\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nX-Forwarded-For: %s\r\n\r\n", forwardedFor)
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatalf("read upgrade response: %v", err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	for i := 0; i < 2; i++ {
		if code := upgrade("203.0.113.7"); code != http.StatusNoContent {
			t.Fatalf("upgrade %d status = %d, want 204", i, code)
		}
		if got := <-hijacked; got != "203.0.113.7:0" {
			t.Fatalf("hijacked conn RemoteAddr = %q, want the forwarded client", got)
		}
	}
	assertByIP(t, admission, map[string]int{"203.0.113.7": 2})

	// 同一真实 IP 已有 2 条（上限 2）：第三条在升级时被拒，反代地址的配额随连接关闭归还。
	if code := upgrade("203.0.113.7"); code != http.StatusTooManyRequests {
		t.Fatalf("upgrade over the per-IP cap status = %d, want 429", code)
	}
	// 另一个真实 IP 不受影响：上限按真实客户端算，而不是全体共用反代地址。
	if code := upgrade("203.0.113.9"); code != http.StatusNoContent {
		t.Fatalf("upgrade from another client status = %d, want 204", code)
	}
	if got := <-hijacked; got != "203.0.113.9:0" {
		t.Fatalf("hijacked conn RemoteAddr = %q, want 203.0.113.9:0", got)
	}
}
