package mesh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const MaxBodyBytes = 16 << 20
const maxRequestDuration = 5 * time.Minute

type principalKey struct{}
type authIdentity struct {
	Principal string
	Grant     TrustGrant
}

func scopeAllowed(grant TrustGrant, scope string) bool {
	return patternAllowed(grant.Scopes, scope)
}
func patternAllowed(patterns []string, scope string) bool {
	for _, allowed := range patterns {
		if allowed == scope || allowed == "*" || strings.HasSuffix(allowed, ".*") && strings.HasPrefix(scope, strings.TrimSuffix(allowed, "*")) {
			return true
		}
	}
	return false
}
func (n *Node) Principal(r *http.Request) string {
	identity, _ := r.Context().Value(principalKey{}).(authIdentity)
	return identity.Principal
}
func (n *Node) Authorized(r *http.Request, scope string) bool {
	identity, ok := r.Context().Value(principalKey{}).(authIdentity)
	return ok && scopeAllowed(identity.Grant, scope)
}

// SignRequest signs an immutable, bounded body. The destination checks the target
// principal, application path, deadline, timestamp and one-use nonce.
func (n *Node) SignRequest(r *http.Request, body []byte) error {
	if len(body) > MaxBodyBytes {
		return fmt.Errorf("request body too large")
	}
	now := time.Now()
	deadline := now.Add(30 * time.Second)
	if d, ok := r.Context().Deadline(); ok {
		deadline = d
	}
	if deadline.After(now.Add(maxRequestDuration)) {
		deadline = now.Add(maxRequestDuration)
	}
	if !deadline.After(now) {
		return context.DeadlineExceeded
	}
	r.Header.Set("X-Mesh-Principal", n.config.Manifest.Identity.Principal)
	r.Header.Set("X-Mesh-Namespace", n.config.Manifest.Identity.Namespace)
	r.Header.Set("X-Mesh-Time", strconv.FormatInt(now.Unix(), 10))
	r.Header.Set("X-Mesh-Deadline", strconv.FormatInt(deadline.UnixMilli(), 10))
	r.Header.Set("X-Mesh-Nonce", randomID())
	if r.Header.Get("X-Mesh-Path") == "" {
		r.Header.Set("X-Mesh-Path", r.URL.RequestURI())
	}
	if r.Header.Get("X-Mesh-Target") == "" {
		r.Header.Set("X-Mesh-Target", "discovery")
	}
	if r.Header.Get("X-Trace-ID") == "" {
		trace := TraceID(r.Context())
		if trace == "" {
			trace = randomID()
		}
		r.Header.Set("X-Trace-ID", trace)
	}
	if r.Header.Get("X-Request-ID") == "" {
		r.Header.Set("X-Request-ID", randomID())
	}
	if !validCorrelation(r.Header.Get("X-Trace-ID")) || !validCorrelation(r.Header.Get("X-Request-ID")) {
		return fmt.Errorf("invalid correlation identifier")
	}
	sum := sha256.Sum256(body)
	r.Header.Set("X-Mesh-Body", hex.EncodeToString(sum[:]))
	r.Header.Set("X-Mesh-Signature", base64.StdEncoding.EncodeToString(ed25519.Sign(n.key, requestSignature(r))))
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	return nil
}
func requestSignature(r *http.Request) []byte {
	return []byte(strings.Join([]string{"whalemesh.request/v1", r.Method, r.Header.Get("X-Mesh-Path"), r.Header.Get("X-Mesh-Target"), r.Header.Get("X-Mesh-Principal"), r.Header.Get("X-Mesh-Namespace"), r.Header.Get("X-Mesh-Time"), r.Header.Get("X-Mesh-Deadline"), r.Header.Get("X-Mesh-Nonce"), r.Header.Get("X-Mesh-Body"), r.Header.Get("Content-Type"), r.Header.Get("X-Mesh-Owner"), r.Header.Get("X-Trace-ID"), r.Header.Get("X-Request-ID")}, "\n"))
}
func (n *Node) authenticate(w http.ResponseWriter, r *http.Request, scopes []string, handler http.HandlerFunc) {
	own := n.config.Manifest.Identity
	n.authenticateTarget(w, r, scopes, IdentityKey(own), handler)
}

// AuthenticateRelay verifies the original application request at a forwarding
// hop. The final destination verifies the same signature again independently.
func (n *Node) AuthenticateRelay(w http.ResponseWriter, r *http.Request, target string, handler http.HandlerFunc) {
	n.authenticateTarget(w, r, []string{"mesh.relay"}, target, handler)
}
func (n *Node) authenticateTarget(w http.ResponseWriter, r *http.Request, scopes []string, expectedTarget string, handler http.HandlerFunc) {
	principal := r.Header.Get("X-Mesh-Principal")
	grant, ok := n.trust[principal]
	if !ok || grant.Namespace != n.config.Manifest.Identity.Namespace || r.Header.Get("X-Mesh-Namespace") != grant.Namespace {
		WriteError(w, 401, "untrusted_principal", "principal or namespace not trusted")
		return
	}
	timestamp, err := strconv.ParseInt(r.Header.Get("X-Mesh-Time"), 10, 64)
	now := time.Now()
	deadline, deadlineErr := strconv.ParseInt(r.Header.Get("X-Mesh-Deadline"), 10, 64)
	expiry := time.UnixMilli(deadline)
	if err != nil || deadlineErr != nil || timestamp < now.Add(-time.Minute).Unix() || timestamp > now.Add(time.Minute).Unix() || !expiry.After(now) || expiry.After(now.Add(maxRequestDuration+time.Second)) {
		WriteError(w, 401, "expired_signature", "invalid request time or deadline")
		return
	}
	path := r.URL.RequestURI()
	target := r.Header.Get("X-Mesh-Target")
	discovery := target == "discovery" && ((r.Method == "POST" && path == "/mesh/exchange") || (r.Method == "GET" && (path == "/mesh/identity" || path == "/.well-known/whalemesh")))
	if r.Header.Get("X-Mesh-Path") != path || target != expectedTarget && !discovery {
		WriteError(w, 401, "wrong_target", "signature target does not match destination")
		return
	}
	signature, err := base64.StdEncoding.DecodeString(r.Header.Get("X-Mesh-Signature"))
	key, _ := base64.StdEncoding.DecodeString(grant.PublicKey)
	// Prove the sender before allocating its request budget or reading a body.
	if err != nil || !ed25519.Verify(ed25519.PublicKey(key), requestSignature(r), signature) {
		WriteError(w, 401, "invalid_signature", "invalid request signature")
		return
	}
	for _, scope := range scopes {
		if !scopeAllowed(grant, scope) {
			WriteError(w, 403, "scope_denied", "missing scope: "+scope)
			return
		}
	}
	if n.operationRequiresTLS(r.Method, path) && r.TLS == nil && !n.config.AllowPlaintextSensitive {
		WriteError(w, 403, "tls_required", "capability operation requires TLS")
		return
	}
	if failure := n.beginInvocation(principal, path); failure != nil {
		w.Header().Set("Retry-After", "1")
		WriteError(w, failure.Status, failure.Code, failure.Message)
		return
	}
	defer n.endInvocation(principal)
	controller := http.NewResponseController(w)
	bodyDeadline := expiry
	if limit := time.Now().Add(15 * time.Second); bodyDeadline.After(limit) {
		bodyDeadline = limit
	}
	_ = controller.SetReadDeadline(bodyDeadline)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	_ = controller.SetReadDeadline(time.Time{})
	if err != nil {
		if !expiry.After(time.Now()) {
			WriteError(w, 408, "deadline_exceeded", "request deadline exceeded")
		} else {
			WriteError(w, 413, "body_too_large", "request body exceeds limit or read deadline")
		}
		return
	}
	digest := sha256.Sum256(body)
	if r.Header.Get("X-Mesh-Body") != hex.EncodeToString(digest[:]) {
		WriteError(w, 401, "invalid_signature", "request body differs from signed digest")
		return
	}
	if !expiry.After(time.Now()) {
		WriteError(w, 408, "deadline_exceeded", "request deadline exceeded")
		return
	}
	nonce := r.Header.Get("X-Mesh-Nonce")
	if len(nonce) != 32 {
		WriteError(w, 401, "invalid_nonce", "invalid nonce")
		return
	}
	n.mu.Lock()
	if n.nonceCounts == nil {
		n.nonceCounts = make(map[string]int)
	}
	if !now.Before(n.nextNonceSweep) {
		for k, until := range n.nonces {
			if !until.After(now) {
				delete(n.nonces, k)
				n.nonceCounts[k[:len(k)-33]]--
			}
		}
		n.nextNonceSweep = now.Add(time.Second)
	}
	nonceKey := principal + "/" + nonce
	_, replayed := n.nonces[nonceKey]
	full := n.nonceCounts[principal] >= 100000/len(n.trust)
	if !replayed && !full {
		// Timestamp acceptance has second precision; retain through its entire
		// inclusive skew window, or the earlier signed request deadline.
		until := time.Unix(timestamp, 0).Add(time.Minute + time.Second)
		if expiry.Before(until) {
			until = expiry
		}
		n.nonces[nonceKey] = until
		n.nonceCounts[principal]++
	}
	n.mu.Unlock()
	if replayed {
		WriteError(w, 401, "replayed_request", "nonce already used")
		return
	}
	if full {
		w.Header().Set("Retry-After", "1")
		WriteError(w, 429, "replay_budget", "principal replay-protection budget exhausted")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	ctx, cancel := context.WithDeadline(r.Context(), expiry)
	defer cancel()
	ctx = context.WithValue(ctx, principalKey{}, authIdentity{principal, grant})
	trace, request := r.Header.Get("X-Trace-ID"), r.Header.Get("X-Request-ID")
	if !validCorrelation(trace) || !validCorrelation(request) {
		WriteError(w, 400, "invalid_correlation", "invalid trace or request ID")
		return
	}
	ctx = context.WithValue(ctx, correlationKey{}, correlation{trace: trace, request: request})
	w.Header().Set("X-Trace-ID", trace)
	w.Header().Set("X-Request-ID", request)
	controller = http.NewResponseController(w)
	_ = controller.SetWriteDeadline(expiry)
	// net/http clears this after flushing the response. Clearing it here can
	// turn a timed-out handler's empty response into a successful HTTP 200.
	handler(w, r.WithContext(ctx))
}

func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func ReadJSON(w http.ResponseWriter, r *http.Request, value any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		WriteError(w, 400, "invalid_json", err.Error())
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		err = fmt.Errorf("request must contain one JSON value")
		WriteError(w, 400, "invalid_json", err.Error())
		return err
	}
	return nil
}
