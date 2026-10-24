package executor

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Engine uses Docker's HTTP API without a CLI, SDK, shell or daemon subprocess.
type Engine struct {
	base   string
	client *http.Client
}
type EngineError struct {
	Status  int
	Message string
}

func (e *EngineError) Error() string {
	return fmt.Sprintf("Docker Engine HTTP %d: %s", e.Status, e.Message)
}
func NewEngine(host string, allowLocalTCP bool) (*Engine, error) {
	if host == "" {
		host = "unix:///var/run/docker.sock"
	}
	u, err := url.Parse(host)
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{ResponseHeaderTimeout: 30 * time.Second, IdleConnTimeout: 90 * time.Second, MaxIdleConnsPerHost: 8}
	base := "http://docker"
	switch u.Scheme {
	case "unix":
		if u.Path == "" {
			return nil, errors.New("Docker Unix socket path required")
		}
		socket := u.Path
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}
	case "tcp", "http", "https":
		if u.User != nil || u.Host == "" || (u.Path != "" && u.Path != "/") {
			return nil, errors.New("invalid Docker host")
		}
		hostname := u.Hostname()
		ip := net.ParseIP(hostname)
		if u.Scheme != "https" && (!allowLocalTCP || ip == nil || !ip.IsLoopback()) {
			return nil, errors.New("plaintext Docker TCP requires explicit loopback testing opt-in")
		}
		scheme := u.Scheme
		if scheme == "tcp" {
			scheme = "http"
		}
		base = scheme + "://" + u.Host
		if scheme == "https" {
			certDir := os.Getenv("DOCKER_CERT_PATH")
			ca, err := os.ReadFile(filepath.Join(certDir, "ca.pem"))
			if err != nil {
				return nil, err
			}
			roots := x509.NewCertPool()
			if !roots.AppendCertsFromPEM(ca) {
				return nil, errors.New("invalid Docker CA")
			}
			cert, err := tls.LoadX509KeyPair(filepath.Join(certDir, "cert.pem"), filepath.Join(certDir, "key.pem"))
			if err != nil {
				return nil, err
			}
			tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, Certificates: []tls.Certificate{cert}}
		}
	default:
		return nil, errors.New("Docker host must use unix, https, or explicitly enabled loopback TCP")
	}
	return &Engine{base: base, client: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (e *Engine) request(ctx context.Context, method, path string, input io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, e.base+path, input)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	response, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	// Docker documents 304 for an already-running start or already-stopped stop.
	// These lifecycle requests have already achieved their requested state.
	if response.StatusCode == http.StatusNotModified && method == "POST" {
		resource := strings.SplitN(path, "?", 2)[0]
		if strings.HasPrefix(resource, "/containers/") && (strings.HasSuffix(resource, "/start") || strings.HasSuffix(resource, "/stop")) {
			return response, nil
		}
	}
	if response.StatusCode >= 300 {
		defer response.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(response.Body, 8192))
		var detail struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(b, &detail)
		if detail.Message == "" {
			detail.Message = string(b)
		}
		return nil, &EngineError{response.StatusCode, detail.Message}
	}
	return response, nil
}
func (e *Engine) json(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	r, err := e.request(ctx, method, path, body, "application/json")
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if output == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20))
		return err
	}
	return json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(output)
}
func missing(err error) bool            { var e *EngineError; return errors.As(err, &e) && e.Status == 404 }
func objectPath(kind, id string) string { return "/" + kind + "/" + url.PathEscape(id) }
func (e *Engine) pull(ctx context.Context, image string) error {
	r, err := e.request(ctx, "POST", "/images/create?fromImage="+url.QueryEscape(image), nil, "")
	if err != nil {
		return err
	}
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<20))
	for {
		var event struct {
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		err = decoder.Decode(&event)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if event.Error != "" {
			return errors.New(event.Error)
		}
		if event.ErrorDetail.Message != "" {
			return errors.New(event.ErrorDetail.Message)
		}
	}
}

// Docker multiplexes non-TTY stdout and stderr using eight-byte frames.
func readFrames(r io.Reader, max int64) (string, string, error) {
	var stdout, stderr bytes.Buffer
	var total int64
	var header [8]byte
	for {
		_, err := io.ReadFull(r, header[:])
		if err == io.EOF {
			return stdout.String(), stderr.String(), nil
		}
		if err != nil {
			return "", "", err
		}
		if header[1] != 0 || header[2] != 0 || header[3] != 0 || (header[0] != 1 && header[0] != 2) {
			return "", "", errors.New("invalid Docker stream frame")
		}
		n := int64(binary.BigEndian.Uint32(header[4:]))
		total += n
		if total > max {
			return "", "", errors.New("Docker output exceeds limit")
		}
		var target io.Writer = &stdout
		if header[0] == 2 {
			target = &stderr
		}
		if _, err = io.CopyN(target, r, n); err != nil {
			return "", "", err
		}
	}
}
func validID(s string) bool { return s != "" && !strings.ContainsAny(s, "/\\\x00?#") && len(s) <= 200 }
