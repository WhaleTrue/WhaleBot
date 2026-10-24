package mesh

import (
	"crypto/ed25519"
	"encoding/json"
	"time"
)

type Manifest struct {
	Schema    string            `json:"schema"`
	Identity  Identity          `json:"identity"`
	Transport Transport         `json:"transport"`
	Provides  []Capability      `json:"provides"`
	Requires  []Requirement     `json:"requires"`
	Lifecycle Lifecycle         `json:"lifecycle"`
	Security  Security          `json:"security"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}
type Identity struct {
	Namespace   string            `json:"namespace"`
	Name        string            `json:"name"`
	InstanceID  string            `json:"instance_id"`
	HostID      string            `json:"host_id"`
	Principal   string            `json:"principal"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
}
type Transport struct {
	Protocols    []string `json:"protocols"`
	Listen       []string `json:"listen"`
	Advertise    []string `json:"advertise"`
	Reachability string   `json:"reachability"`
	Security     string   `json:"security"`
}
type Capability struct {
	Schema            string            `json:"schema"`
	ID                string            `json:"id"`
	Interface         string            `json:"interface"`
	Version           string            `json:"version"`
	Endpoint          string            `json:"endpoint"`
	TransportSecurity string            `json:"transport_security,omitempty"`
	InputSchema       json.RawMessage   `json:"input_schema"`
	OutputSchema      json.RawMessage   `json:"output_schema"`
	Operations        []Operation       `json:"operations"`
	Ready             bool              `json:"ready"`
	Attributes        map[string]string `json:"attributes"`
	Scopes            []string          `json:"scopes"`
	Semantics         Semantics         `json:"semantics"`
	Tool              *ToolExport       `json:"tool,omitempty"`
}
type Operation struct {
	Name              string    `json:"name"`
	Method            string    `json:"method"`
	Path              string    `json:"path"`
	TransportSecurity string    `json:"transport_security,omitempty"`
	Semantics         Semantics `json:"semantics"`
	Scopes            []string  `json:"scopes,omitempty"`
}
type Semantics struct {
	ReadOnly      bool `json:"read_only"`
	Idempotent    bool `json:"idempotent"`
	Mutating      bool `json:"mutating"`
	SideEffecting bool `json:"side_effecting"`
	Streaming     bool `json:"streaming"`
}
type ToolExport struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema"`
	Method       string          `json:"method"`
	Path         string          `json:"path"`
	Scopes       []string        `json:"scopes"`
	Confirmation bool            `json:"confirmation"`
}
type Requirement struct {
	ID         string            `json:"id"`
	Capability string            `json:"capability"`
	Interface  string            `json:"interface"`
	Version    string            `json:"version"`
	Namespace  string            `json:"namespace"`
	Locality   string            `json:"locality"`
	Policy     string            `json:"policy"`
	Pin        string            `json:"pin"`
	Providers  []string          `json:"providers,omitempty"`
	Optional   bool              `json:"optional"`
	Many       bool              `json:"many"`
	Sticky     bool              `json:"sticky"`
	Attributes map[string]string `json:"attributes"`
	Scopes     []string          `json:"scopes"`
}
type Lifecycle struct {
	Alive    bool `json:"alive"`
	Ready    bool `json:"ready"`
	Draining bool `json:"draining"`
}
type Security struct {
	Identity       string   `json:"identity"`
	TrustNamespace string   `json:"trust_namespace"`
	Authentication []string `json:"authentication"`
	Permissions    []string `json:"permissions"`
}
type Peer struct {
	Manifest Manifest  `json:"manifest"`
	Sequence uint64    `json:"sequence"`
	LastSeen time.Time `json:"last_seen"`
}
type Binding struct {
	Node       Manifest   `json:"node"`
	Capability Capability `json:"capability"`
	Reason     string     `json:"reason"`
}
type Constraint = Requirement
type TrustGrant struct {
	PublicKey string   `json:"public_key"`
	Namespace string   `json:"namespace"`
	Scopes    []string `json:"scopes"`
	Provides  []string `json:"provides"`
}
type Config struct {
	Manifest                Manifest
	Listen                  string
	Seeds                   []string
	IdentityFile            string
	TrustFile               string
	PrivateKey              ed25519.PrivateKey
	Trust                   map[string]TrustGrant
	Interval                time.Duration
	TTL                     time.Duration
	DataDir                 string
	TLSCert                 string
	TLSKey                  string
	CAFile                  string
	AllowPlaintextSensitive bool
}
type TopologyEvent struct {
	Type     string    `json:"type"`
	Identity Identity  `json:"identity"`
	Sequence uint64    `json:"sequence"`
	At       time.Time `json:"at"`
	Reason   string    `json:"reason,omitempty"`
}
