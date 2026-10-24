package mesh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type RemoteError struct {
	Status  int
	Code    string
	Message string
}

func (e *RemoteError) Error() string {
	return fmt.Sprintf("mesh HTTP %d %s: %s", e.Status, e.Code, e.Message)
}

type responseBody struct {
	io.ReadCloser
	once   sync.Once
	finish func()
}

func (b *responseBody) Close() error { err := b.ReadCloser.Close(); b.once.Do(b.finish); return err }
func (b *responseBody) Read(p []byte) (int, error) {
	count, err := b.ReadCloser.Read(p)
	if err != nil {
		b.once.Do(b.finish)
	}
	return count, err
}

type ownerKey struct{}

// WithOwner carries a caller-validated delegated user identity into the signed
// transport header. Destination policy must still authorize delegation.
func WithOwner(ctx context.Context, owner string) context.Context {
	return context.WithValue(ctx, ownerKey{}, owner)
}

// Do sends an already signed HTTP request. Redirects never forward credentials.
func (n *Node) Do(r *http.Request) (*http.Response, error) { return n.client.Do(r) }
func (n *Node) Call(ctx context.Context, binding Binding, method, path string, input, output any) error {
	response, err := n.Stream(ctx, binding, method, path, input)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return decodeResponse(response, output)
}
func (n *Node) Stream(ctx context.Context, binding Binding, method, path string, input any) (*http.Response, error) {
	if binding.Node.Identity.Namespace != n.config.Manifest.Identity.Namespace {
		return nil, fmt.Errorf("cross-namespace call denied")
	}
	grant, ok := n.trust[binding.Node.Identity.Principal]
	if !ok || grant.Namespace != binding.Node.Identity.Namespace {
		return nil, fmt.Errorf("binding principal not trusted")
	}
	if err := validateProvides(binding.Node, grant); err != nil {
		return nil, err
	}
	own := n.trust[n.config.Manifest.Identity.Principal]
	if len(binding.Capability.Scopes) > 0 {
		allowed := false
		for _, scope := range binding.Capability.Scopes {
			if scopeAllowed(own, scope) {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, fmt.Errorf("caller lacks all advertised capability scopes")
		}
	}
	address := n.selectAddress(binding.Node.Transport.Advertise)
	if address == "" {
		return nil, fmt.Errorf("binding has no reachable advertised HTTP transport")
	}
	if path == "" {
		path = binding.Capability.Endpoint
	}
	if parsed, err := url.Parse(binding.Capability.Endpoint); err == nil && parsed.IsAbs() && path == binding.Capability.Endpoint {
		address = parsed.Scheme + "://" + parsed.Host
		path = parsed.RequestURI()
	}
	requiresTLS := binding.Capability.TransportSecurity == "tls"
	matched := len(binding.Capability.Operations) == 0
	for _, operation := range binding.Capability.Operations {
		if operationMatches(operation, method, path) {
			matched = true
			requiresTLS = requiresTLS || operation.TransportSecurity == "tls"
			for _, scope := range operation.Scopes {
				if !scopeAllowed(own, scope) {
					return nil, fmt.Errorf("caller lacks operation scope %s", scope)
				}
			}
		}
	}
	if !matched {
		return nil, fmt.Errorf("operation is not advertised by capability %s", binding.Capability.ID)
	}
	if requiresTLS && strings.HasPrefix(address, "http://") && !n.config.AllowPlaintextSensitive {
		return nil, fmt.Errorf("capability operation requires HTTPS; isolated bridge exception requires explicit MESH_ALLOW_PLAINTEXT_SENSITIVE=1")
	}
	key := bindingKey(binding)
	n.mu.Lock()
	n.active[key]++
	n.mu.Unlock()
	finish := func() {
		n.mu.Lock()
		n.active[key]--
		if n.active[key] == 0 {
			delete(n.active, key)
		}
		n.mu.Unlock()
	}
	response, err := n.send(ctx, address, IdentityKey(binding.Node.Identity), method, path, input)
	if err != nil {
		finish()
		n.failures.Add(1)
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		err = decodeResponse(response, nil)
		finish()
		n.failures.Add(1)
		return nil, err
	}
	response.Body = &responseBody{ReadCloser: response.Body, finish: finish}
	return response, nil
}
func (n *Node) requestJSON(ctx context.Context, address, target, method, path string, input, output any) error {
	response, err := n.send(ctx, address, target, method, path, input)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return decodeResponse(response, output)
}
func (n *Node) send(ctx context.Context, address, target, method, path string, input any) (*http.Response, error) {
	if err := validateAddress(address); err != nil {
		return nil, err
	}
	application, err := url.Parse(path)
	if err != nil || application.IsAbs() || application.Host != "" || !strings.HasPrefix(application.Path, "/") || application.Fragment != "" {
		return nil, fmt.Errorf("invalid application path")
	}
	var body []byte
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil {
			return nil, err
		}
	}
	if len(body) > MaxBodyBytes {
		return nil, fmt.Errorf("request body too large")
	}
	duration := 30 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		duration = time.Until(deadline)
		if duration > maxRequestDuration {
			duration = maxRequestDuration
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, duration)
	endpoint := strings.TrimRight(address, "/") + path
	request, err := http.NewRequestWithContext(callCtx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Mesh-Target", target)
	if owner, ok := ctx.Value(ownerKey{}).(string); ok {
		request.Header.Set("X-Mesh-Owner", owner)
	}
	request.Header.Set("X-Mesh-Path", path)
	if err = n.SignRequest(request, body); err != nil {
		cancel()
		return nil, err
	}
	response, err := n.client.Do(request)
	n.mu.Lock()
	if err != nil || response.StatusCode >= 500 {
		n.routeFailures[address] = time.Now()
	} else {
		delete(n.routeFailures, address)
	}
	n.mu.Unlock()
	if err != nil {
		cancel()
		return nil, err
	}
	response.Body = &responseBody{ReadCloser: response.Body, finish: cancel}
	return response, nil
}
func decodeResponse(response *http.Response, output any) error {
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxBodyBytes+1))
	if err != nil {
		return err
	}
	if len(data) > MaxBodyBytes {
		return fmt.Errorf("response body exceeds limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &envelope)
		if envelope.Error.Message == "" {
			envelope.Error.Message = string(bytes.TrimSpace(data))
		}
		return &RemoteError{Status: response.StatusCode, Code: envelope.Error.Code, Message: envelope.Error.Message}
	}
	if output == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	return json.Unmarshal(data, output)
}
func operationMatches(operation Operation, method, requestPath string) bool {
	if operation.Method != method && !(method == "HEAD" && operation.Method == "GET") {
		return false
	}
	u, err := url.Parse(requestPath)
	if err != nil {
		return false
	}
	pattern := strings.Split(strings.Trim(operation.Path, "/"), "/")
	segments := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, part := range pattern {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "...}") {
			return i <= len(segments)
		}
		if i >= len(segments) {
			return false
		}
		if part != segments[i] && !(strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}")) {
			return false
		}
	}
	return len(pattern) == len(segments)
}
func (n *Node) selectAddress(addresses []string) string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	chosen := ""
	var oldest time.Time
	for _, address := range addresses {
		if validateAddress(address) != nil {
			continue
		}
		failed := n.routeFailures[address]
		if failed.IsZero() || time.Since(failed) > 30*time.Second {
			return address
		}
		if chosen == "" || failed.Before(oldest) {
			chosen = address
			oldest = failed
		}
	}
	return chosen
}
