// Package relay provides optional outbound-only HTTP transport for mesh nodes.
// It forwards original signed requests; destination authorization stays local.
package relay

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/WhaleMesh/WhaleMesh/pkg/mesh"
	"github.com/hashicorp/yamux"
)

const upgradeProtocol = "whalemesh-relay-v1"

type Options struct {
	URLs              []string
	ReconnectInterval time.Duration
}
type connection struct {
	session   *yamux.Session
	transport *http.Transport
	target    string
}
type hub struct {
	node     *mesh.Node
	mu       sync.Mutex
	sessions map[string]*connection
	closing  bool
}
type hello struct {
	InstanceID string `json:"instance_id"`
	Name       string `json:"name"`
}

func Register(node *mesh.Node, dataDir string) ([]mesh.Capability, error) {
	var urls []string
	for _, value := range strings.Split(os.Getenv("MESH_RELAYS"), ",") {
		if value = strings.TrimSpace(value); value != "" {
			urls = append(urls, value)
		}
	}
	return RegisterWithOptions(node, dataDir, Options{URLs: urls})
}
func RegisterWithOptions(node *mesh.Node, _ string, options Options) ([]mesh.Capability, error) {
	h := &hub{node: node, sessions: map[string]*connection{}}
	if options.ReconnectInterval == 0 {
		options.ReconnectInterval = time.Second
	}
	if options.ReconnectInterval < 10*time.Millisecond {
		return nil, fmt.Errorf("relay reconnect interval too short")
	}
	node.Handle("POST", "/relay/connect", []string{"mesh.relay"}, h.connect)
	// The route restores the original signed path before checking authentication.
	// This is a transport entry, not an unauthenticated business endpoint.
	node.HandlePublic("", "/relay/forward/{principal}/{instance}/{path...}", h.forward)
	if err := node.AddWorker(func(ctx context.Context) error {
		<-ctx.Done()
		h.mu.Lock()
		h.closing = true
		for _, connection := range h.sessions {
			_ = connection.session.Close()
			connection.transport.CloseIdleConnections()
		}
		h.sessions = map[string]*connection{}
		h.mu.Unlock()
		return nil
	}); err != nil {
		return nil, err
	}
	identity := node.Manifest().Identity
	for _, address := range options.URLs {
		parsed, err := url.Parse(address)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.RawQuery != "" || parsed.User != nil || parsed.Fragment != "" {
			return nil, fmt.Errorf("invalid relay URL %q", address)
		}
		if parsed.Scheme == "http" {
			slog.Warn("relay uses unencrypted HTTP; use HTTPS for multi-host transport", "address", address)
		}
		advertised := strings.TrimRight(address, "/") + "/relay/forward/" + url.PathEscape(identity.Principal) + "/" + identity.InstanceID
		if err := node.AddAdvertise(advertised); err != nil {
			return nil, err
		}
		address := address
		if err := node.AddWorker(func(ctx context.Context) error { return maintain(ctx, node, address, options.ReconnectInterval) }); err != nil {
			return nil, err
		}
	}
	return []mesh.Capability{{Schema: "whalemesh.capability/v1", ID: "mesh.relay", Interface: "whalemesh.mesh.relay", Version: "1.0.0", Endpoint: "/relay/connect", InputSchema: mesh.JSONSchemaFor(hello{}), OutputSchema: json.RawMessage(`{"type":"null","description":"HTTP 101 Upgrade to whalemesh-relay-v1 yamux transport; no JSON response body."}`), Ready: true, Scopes: []string{"mesh.relay"}, Semantics: mesh.Semantics{Streaming: true}, Operations: []mesh.Operation{{Name: "connect", Method: "POST", Path: "/relay/connect", Scopes: []string{"mesh.relay"}, Semantics: mesh.Semantics{Streaming: true}}}}}, nil
}
func yamuxConfig() *yamux.Config {
	config := yamux.DefaultConfig()
	config.KeepAliveInterval = 10 * time.Second
	config.ConnectionWriteTimeout = 10 * time.Second
	config.LogOutput = io.Discard
	return config
}
func (h *hub) connect(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), upgradeProtocol) || !headerToken(r.Header.Get("Connection"), "upgrade") {
		mesh.WriteError(w, 400, "upgrade_required", "relay requires HTTP/1.1 upgrade")
		return
	}
	var message hello
	if err := mesh.ReadJSON(w, r, &message); err != nil {
		return
	}
	if len(message.InstanceID) != 32 || strings.ContainsAny(message.InstanceID, "/\\") {
		mesh.WriteError(w, 400, "invalid_instance", "invalid incarnation")
		return
	}
	principal := h.node.Principal(r)
	var target string
	for _, peer := range h.node.Peers() {
		identity := peer.Manifest.Identity
		if identity.Principal == principal && identity.InstanceID == message.InstanceID && identity.Name == message.Name {
			target = mesh.IdentityKey(identity)
			break
		}
	}
	if target == "" {
		mesh.WriteError(w, 403, "unannounced_identity", "publish the signed node identity before connecting")
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		mesh.WriteError(w, 500, "upgrade_unsupported", "HTTP server cannot hijack connection")
		return
	}
	conn, buffer, err := hijacker.Hijack()
	if err != nil {
		return
	}
	if _, err = buffer.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: " + upgradeProtocol + "\r\n\r\n"); err != nil {
		_ = conn.Close()
		return
	}
	if err = buffer.Flush(); err != nil {
		_ = conn.Close()
		return
	}
	_ = conn.SetDeadline(time.Time{})
	session, err := yamux.Client(bufferedConn{Conn: conn, reader: buffer.Reader}, yamuxConfig())
	if err != nil {
		_ = conn.Close()
		return
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return openStream(ctx, session) }, ResponseHeaderTimeout: 30 * time.Second, IdleConnTimeout: 30 * time.Second, MaxIdleConnsPerHost: 16}
	current := &connection{session: session, transport: transport, target: target}
	key := url.QueryEscape(principal) + "/" + url.QueryEscape(message.InstanceID)
	h.mu.Lock()
	if h.closing {
		h.mu.Unlock()
		_ = session.Close()
		return
	}
	old := h.sessions[key]
	h.sessions[key] = current
	h.mu.Unlock()
	if old != nil {
		_ = old.session.Close()
		old.transport.CloseIdleConnections()
	}
	slog.Info("mesh relay connected", "principal", h.node.Principal(r), "instance", message.InstanceID)
	go func() {
		<-session.CloseChan()
		transport.CloseIdleConnections()
		h.mu.Lock()
		if h.sessions[key] == current {
			delete(h.sessions, key)
		}
		h.mu.Unlock()
		slog.Info("mesh relay disconnected", "target", key)
	}()
}
func (h *hub) forward(w http.ResponseWriter, r *http.Request) {
	key := url.QueryEscape(r.PathValue("principal")) + "/" + url.QueryEscape(r.PathValue("instance"))
	h.mu.Lock()
	connection := h.sessions[key]
	h.mu.Unlock()
	target := r.Header.Get("X-Mesh-Target")
	if connection != nil {
		target = connection.target
	}
	parts := strings.SplitN(r.URL.EscapedPath(), "/", 6)
	if len(parts) != 6 {
		mesh.WriteError(w, 400, "invalid_path", "missing forwarded application path")
		return
	}
	application, err := url.Parse("/" + parts[5])
	if err != nil {
		mesh.WriteError(w, 400, "invalid_path", "invalid forwarded path")
		return
	}
	application.RawQuery = r.URL.RawQuery
	r = r.Clone(r.Context())
	r.URL = application
	r.RequestURI = application.RequestURI()
	h.node.AuthenticateRelay(w, r, target, func(w http.ResponseWriter, r *http.Request) {
		if connection == nil || connection.session.IsClosed() {
			mesh.WriteError(w, 503, "relay_unavailable", "target has no connected outbound session")
			return
		}
		proxy := httputil.ReverseProxy{Director: func(out *http.Request) { out.URL.Scheme = "http"; out.URL.Host = "mesh"; out.Host = "mesh" }, Transport: connection.transport, FlushInterval: -1, ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			mesh.WriteError(w, 502, "relay_transport", err.Error())
		}}
		proxy.ServeHTTP(w, r)
	})
}
func openStream(ctx context.Context, session *yamux.Session) (net.Conn, error) {
	type result struct {
		conn net.Conn
		err  error
	}
	ready := make(chan result)
	go func() {
		conn, err := session.Open()
		select {
		case ready <- result{conn, err}:
		case <-ctx.Done():
			if conn != nil {
				_ = conn.Close()
			}
		}
	}()
	select {
	case result := <-ready:
		return result.conn, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func maintain(ctx context.Context, node *mesh.Node, address string, minimum time.Duration) error {
	backoff := minimum
	for ctx.Err() == nil {
		start := time.Now()
		err := connectAndServe(ctx, node, address)
		if ctx.Err() != nil {
			return nil
		}
		slog.Warn("mesh relay reconnecting", "address", address, "error", err, "backoff", backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		if time.Since(start) > 30*time.Second {
			backoff = minimum
		} else {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
	return nil
}
func connectAndServe(ctx context.Context, node *mesh.Node, address string) error {
	handshakeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	remote, err := node.Discover(handshakeCtx, address)
	if err != nil {
		return err
	}
	conn, err := node.DialHTTP(handshakeCtx, address)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	body, _ := json.Marshal(hello{InstanceID: node.Manifest().Identity.InstanceID, Name: node.Manifest().Identity.Name})
	request, err := http.NewRequestWithContext(handshakeCtx, "POST", strings.TrimRight(address, "/")+"/relay/connect", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", upgradeProtocol)
	request.Header.Set("X-Mesh-Target", mesh.IdentityKey(remote.Identity))
	if err = node.SignRequest(request, body); err != nil {
		return err
	}
	if err = request.Write(conn); err != nil {
		return err
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		return err
	}
	if response.StatusCode != 101 {
		defer response.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("relay refused: %s %s", response.Status, data)
	}
	if !strings.EqualFold(response.Header.Get("Upgrade"), upgradeProtocol) {
		return fmt.Errorf("relay upgrade protocol mismatch")
	}
	_ = conn.SetDeadline(time.Time{})
	session, err := yamux.Server(bufferedConn{Conn: conn, reader: reader}, yamuxConfig())
	if err != nil {
		return err
	}
	defer session.Close()
	var tlsState *tls.ConnectionState
	if secure, ok := conn.(*tls.Conn); ok {
		state := secure.ConnectionState()
		tlsState = &state
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tlsState != nil {
			r.TLS = tlsState
		}
		node.Handler().ServeHTTP(w, r)
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = session.Close()
			_ = server.Close()
		case <-done:
		}
	}()
	err = server.Serve(session)
	_ = server.Close()
	if errors.Is(err, http.ErrServerClosed) || ctx.Err() != nil {
		return nil
	}
	return err
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c bufferedConn) Read(data []byte) (int, error) { return c.reader.Read(data) }
func headerToken(value, token string) bool {
	for _, part := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}
