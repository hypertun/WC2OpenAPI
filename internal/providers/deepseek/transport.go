package deepseek

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"

	utls "github.com/refraction-networking/utls"
)

type DialContextFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// newUTLSHTTPClient creates a DS2API-style HTTP client with Safari TLS fingerprint
// and forced HTTP/1.1 to bypass WAF detection
func newUTLSHTTPClient(timeout time.Duration) *http.Client {
	dialContext := (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext

	base := &http.Transport{
		ForceAttemptHTTP2:   false,
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
		DialContext:         dialContext,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
	}

	return &http.Client{Timeout: timeout, Transport: base}
}

// forceHTTP11ALPN forces HTTP/1.1 in the ALPN extension to avoid HTTP/2 detection
func forceHTTP11ALPN(uConn *utls.UConn) error {
	if err := uConn.BuildHandshakeState(); err != nil {
		return err
	}
	for _, ext := range uConn.Extensions {
		alpnExt, ok := ext.(*utls.ALPNExtension)
		if !ok {
			continue
		}
		alpnExt.AlpnProtocols = []string{"http/1.1"}
		return nil
	}
	return nil
}
