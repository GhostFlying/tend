package netx

import (
	"crypto/tls"
	"net"
)

// httpsListener advertises multiplexing to browsers. http.Server.Serve enables
// HTTP/2 for this TLS listener; omitting ALPN leaves every SSE on an HTTP/1 slot.
// Certificate selection stays with the original tailnet client.
func httpsListener(listener net.Listener, certificate func(*tls.ClientHelloInfo) (*tls.Certificate, error)) net.Listener {
	return tls.NewListener(listener, &tls.Config{
		GetCertificate: certificate,
		NextProtos:     []string{"h2", "http/1.1"},
	})
}
