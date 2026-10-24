package mesh

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

type identityFile struct {
	Principal  string `json:"principal"`
	PrivateKey string `json:"private_key"`
}

func LoadConfigFromEnv() (Config, error) {
	c := Config{Listen: envDefault("MESH_LISTEN", ":8080"), DataDir: envDefault("MESH_DATA", "/data"), IdentityFile: envDefault("MESH_IDENTITY_FILE", "/run/identity.json"), TrustFile: envDefault("MESH_TRUST_FILE", "/run/trust.json")}
	c.TLSCert = os.Getenv("MESH_TLS_CERT")
	c.TLSKey = os.Getenv("MESH_TLS_KEY")
	c.CAFile = os.Getenv("MESH_CA_FILE")
	c.AllowPlaintextSensitive = os.Getenv("MESH_ALLOW_PLAINTEXT_SENSITIVE") == "1"
	if path := os.Getenv("MESH_MANIFEST"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return c, err
		}
		if err = decodeConfigJSON(data, &c.Manifest); err != nil {
			return c, err
		}
	}
	c.Manifest.Identity.Namespace = envDefault("MESH_NAMESPACE", valueDefault(c.Manifest.Identity.Namespace, "default"))
	c.Manifest.Identity.Name = envDefault("MESH_NAME", valueDefault(c.Manifest.Identity.Name, "node"))
	if host := os.Getenv("MESH_HOST_ID"); host != "" {
		c.Manifest.Identity.HostID = host
	} else if c.Manifest.Identity.HostID == "" {
		c.Manifest.Identity.HostID, _ = os.Hostname()
	}
	if advertise := os.Getenv("MESH_ADVERTISE"); advertise != "" {
		c.Manifest.Transport.Advertise = splitList(advertise)
	}
	c.Seeds = splitList(os.Getenv("MESH_SEEDS"))
	var err error
	if value := os.Getenv("MESH_INTERVAL"); value != "" {
		c.Interval, err = time.ParseDuration(value)
		if err != nil {
			return c, fmt.Errorf("MESH_INTERVAL: %w", err)
		}
	}
	if value := os.Getenv("MESH_TTL"); value != "" {
		c.TTL, err = time.ParseDuration(value)
		if err != nil {
			return c, fmt.Errorf("MESH_TTL: %w", err)
		}
	}
	return c, nil
}
func loadCredentials(c *Config) error {
	if c.IdentityFile != "" {
		data, err := os.ReadFile(c.IdentityFile)
		if err != nil {
			return fmt.Errorf("read identity: %w", err)
		}
		var identity identityFile
		if err = decodeConfigJSON(data, &identity); err != nil {
			return err
		}
		key, err := base64.StdEncoding.DecodeString(identity.PrivateKey)
		if err != nil {
			return fmt.Errorf("identity key: %w", err)
		}
		c.PrivateKey = ed25519.PrivateKey(key)
		if c.Manifest.Identity.Principal != "" && c.Manifest.Identity.Principal != identity.Principal {
			return fmt.Errorf("identity principal mismatch")
		}
		c.Manifest.Identity.Principal = identity.Principal
	}
	if len(c.PrivateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("Ed25519 private key must contain 64 bytes")
	}
	derived := ed25519.NewKeyFromSeed(c.PrivateKey.Seed())
	if !derived.Equal(c.PrivateKey) {
		return fmt.Errorf("invalid Ed25519 private key")
	}
	if c.TrustFile != "" {
		data, err := os.ReadFile(c.TrustFile)
		if err != nil {
			return fmt.Errorf("read trust: %w", err)
		}
		if err = decodeConfigJSON(data, &c.Trust); err != nil {
			return err
		}
	}
	if c.Manifest.Identity.Principal == "" {
		return fmt.Errorf("principal required")
	}
	grant, ok := c.Trust[c.Manifest.Identity.Principal]
	if !ok || grant.Namespace != c.Manifest.Identity.Namespace {
		return fmt.Errorf("own principal requires matching namespace trust grant")
	}
	key, err := base64.StdEncoding.DecodeString(grant.PublicKey)
	if err != nil || !ed25519.PublicKey(key).Equal(c.PrivateKey.Public()) {
		return fmt.Errorf("own trust public key mismatch")
	}
	for principal, grant := range c.Trust {
		key, err := base64.StdEncoding.DecodeString(grant.PublicKey)
		if principal == "" || grant.Namespace == "" || err != nil || len(key) != ed25519.PublicKeySize {
			return fmt.Errorf("invalid trust grant for %q", principal)
		}
	}
	return nil
}
func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return hex.EncodeToString(b[:])
}
func splitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
func envDefault(k, v string) string { return valueDefault(os.Getenv(k), v) }
func valueDefault(s, v string) string {
	if s != "" {
		return s
	}
	return v
}
func decodeConfigJSON(data []byte, value any) error {
	if _, err := parseDocument(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}
