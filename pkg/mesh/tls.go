package mesh

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

func clientTLSConfig(c Config) (*tls.Config, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	if c.CAFile != "" {
		data, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, err
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(data) {
			return nil, fmt.Errorf("CA file contains no certificates")
		}
		config.RootCAs = roots
	}
	return config, nil
}

// DialHTTP opens a transport connection with the same verified TLS policy as
// normal mesh calls. The caller owns the connection and its operation deadline.
func (n *Node) DialHTTP(ctx context.Context, address string) (net.Conn, error) {
	if err := validateAddress(address); err != nil {
		return nil, err
	}
	u, _ := url.Parse(address)
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 15 * time.Second}
	if u.Scheme == "https" {
		config := n.client.Transport.(*http.Transport).TLSClientConfig.Clone()
		config.ServerName = u.Hostname()
		config.NextProtos = []string{"http/1.1"}
		return (&tls.Dialer{NetDialer: dialer, Config: config}).DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	}
	return dialer.DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
}

// Discover verifies a seed's signed identity before using it as an upgrade peer.
func (n *Node) Discover(ctx context.Context, address string) (Manifest, error) {
	var signed SignedManifest
	if err := n.requestJSON(ctx, address, "discovery", "GET", "/mesh/identity", nil, &signed); err != nil {
		return Manifest{}, err
	}
	if err := n.accept(signed); err != nil {
		return Manifest{}, err
	}
	return cloneManifest(signed.Manifest), nil
}
