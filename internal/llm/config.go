package llm

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type store struct {
	mu     sync.RWMutex
	path   string
	config Config
}

func openStore(dir string) (*store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	s := &store{path: filepath.Join(dir, "llm-config.json"), config: Config{Models: []Profile{}}}
	b, err := os.ReadFile(s.path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		if len(b) > 1<<20 {
			return nil, errors.New("model configuration exceeds 1 MiB")
		}
		if err = decode(bytes.NewReader(b), &s.config); err != nil {
			return nil, err
		}
		if err = validateConfig(s.config); err != nil {
			return nil, err
		}
	}
	if s.config.Revision == "" {
		if err := s.replace(s.config); err != nil {
			return nil, err
		}
	}
	return s, nil
}
func decode(r io.Reader, v any) error {
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("expected exactly one JSON value")
	}
	return nil
}
func validateConfig(c Config) error {
	if len(c.Models) > 64 {
		return errors.New("at most 64 model profiles")
	}
	seen := map[string]bool{}
	active := c.ActiveModelID == ""
	for _, p := range c.Models {
		if p.ID == "" || p.Name == "" || p.Model == "" || len(p.ID) > 128 || len(p.Model) > 256 || len(p.APIKey) > 8192 {
			return errors.New("invalid model profile")
		}
		if seen[p.ID] {
			return fmt.Errorf("duplicate model id %q", p.ID)
		}
		seen[p.ID] = true
		u, err := url.Parse(p.BaseURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("base_url must be an HTTP(S) URL without credentials, query or fragment")
		}
		if p.TimeoutSeconds < 0 || p.TimeoutSeconds > 1800 {
			return errors.New("timeout_seconds must be 0..1800")
		}
		if p.ID == c.ActiveModelID {
			active = true
		}
		switch p.CostClass {
		case "", "unknown", "free", "paid":
		default:
			return errors.New("cost_class must be free, paid or unknown")
		}
		features := map[string]bool{}
		for _, feature := range p.Capabilities {
			if feature != "tools" && feature != "streaming" {
				return errors.New("unsupported model capability")
			}
			if features[feature] {
				return errors.New("duplicate model capability")
			}
			features[feature] = true
		}
	}
	if !active {
		return errors.New("active_model_id is not in models")
	}
	return nil
}
func (s *store) replace(next Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range next.Models {
		if next.Models[i].APIKey == "" {
			for _, old := range s.config.Models {
				if old.ID == next.Models[i].ID {
					next.Models[i].APIKey = old.APIKey
				}
			}
		}
	}
	if err := validateConfig(next); err != nil {
		return err
	}
	var revision [16]byte
	if _, err := rand.Read(revision[:]); err != nil {
		return err
	}
	next.Revision = hex.EncodeToString(revision[:])
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".llm-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, s.path); err != nil {
		return err
	}
	s.config = next
	return nil
}
func (s *store) public() PublicConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := PublicConfig{Revision: s.config.Revision, ActiveModelID: s.config.ActiveModelID, Models: []PublicProfile{}}
	for _, p := range s.config.Models {
		out.Models = append(out.Models, PublicProfile{ID: p.ID, Name: p.Name, BaseURL: p.BaseURL, Model: p.Model, HasAPIKey: p.APIKey != "", TimeoutSeconds: p.TimeoutSeconds, CostClass: p.CostClass, Capabilities: append([]string(nil), p.Capabilities...)})
	}
	return out
}
func (s *store) profile(request Request, mock bool) (Profile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	revision := s.config.Revision
	if mock {
		revision = "mock:" + revision
	}
	if request.ModelRevision == "" || request.ModelRevision != revision {
		return Profile{}, errors.New("model configuration revision changed; select the model again")
	}
	if mock {
		if request.Model != "mock" {
			return Profile{}, errors.New("mock provider only supports model mock")
		}
		if len(request.Tools) > 0 {
			return Profile{}, errors.New("mock provider does not support tools")
		}
		return Profile{ID: "mock", Model: "mock", CostClass: "free", Capabilities: []string{"streaming"}}, nil
	}
	for _, p := range s.config.Models {
		if p.ID != request.Model {
			continue
		}
		if request.FreeOnly && p.CostClass != "free" {
			return Profile{}, errors.New("free-only invocation requires an explicitly free model")
		}
		if len(request.Tools) > 0 && !hasFeature(p.Capabilities, "tools") {
			return Profile{}, errors.New("selected model does not declare tools")
		}
		if request.Stream && !hasFeature(p.Capabilities, "streaming") {
			return Profile{}, errors.New("selected model does not declare streaming")
		}
		return p, nil
	}
	return Profile{}, errors.New("requested saved model is unavailable")
}
func completionURL(base string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	if strings.HasSuffix(base, "/v1") {
		return base + "/chat/completions"
	}
	return base + "/v1/chat/completions"
}
