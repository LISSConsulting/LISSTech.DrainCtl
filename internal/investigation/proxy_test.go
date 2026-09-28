//go:build windows

package investigation

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestProductionTransportBypassesHostileProxyAndDialsOnlyFixedEndpoint(t *testing.T) {
	proxy := hostileProxy(t)
	t.Setenv("HTTP_PROXY", "http://"+proxy.Addr().String())
	t.Setenv("HTTPS_PROXY", "http://"+proxy.Addr().String())
	t.Setenv("NO_PROXY", "")

	var dialed atomic.Value
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(_ context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" {
				t.Fatalf("network = %q", network)
			}
			dialed.Store(address)
			return nil, errors.New("dial blocked by test")
		},
	}
	client := NewProviderClientWithTransport(transport)
	if client.Transport().Proxy != nil {
		t.Fatal("production provider transport retained a proxy")
	}

	_, err := client.Send(context.Background(), &testConfiguration{credential: []byte("secret")}, []byte(`{"v":1}`), validFacts(), nil, authorizeTestSend)
	if err == nil {
		t.Fatal("failing injected dial did not fail the request")
	}
	address, ok := dialed.Load().(string)
	if !ok || address != "api.openai.com:443" {
		t.Fatalf("dial target = %q, want fixed api target", address)
	}
	if connections := proxy.connections.Load(); connections != 0 {
		t.Fatalf("hostile proxy received %d connections", connections)
	}
}

type proxyListener struct {
	net.Listener
	connections atomic.Int64
}

func hostileProxy(t *testing.T) *proxyListener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxy := &proxyListener{Listener: listener}
	t.Cleanup(func() { _ = proxy.Close() })
	go func() {
		for {
			connection, err := proxy.Accept()
			if err != nil {
				return
			}
			proxy.connections.Add(1)
			_ = connection.Close()
		}
	}()
	return proxy
}
