package mesh

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/WhaleTrue/WhaleBot/schemas"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	NodeSchema       = "whalemesh.node/v1"
	CapabilitySchema = "whalemesh.capability/v1"
	DeploymentSchema = "whalemesh.deployment/v1"
	MaxDocumentBytes = 1 << 20
)

// Deployment is an executor-local desired state, not a privileged Docker API.
// Secrets carry references only. ApprovalIDs refer to independently verified grants.
type Deployment struct {
	Schema        string            `json:"schema"`
	ID            string            `json:"id"`
	Namespace     string            `json:"namespace"`
	Node          NodeTemplate      `json:"node"`
	Requires      []Requirement     `json:"requires,omitempty"`
	Image         string            `json:"image"`
	Command       []string          `json:"command"`
	Env           map[string]string `json:"env"`
	Secrets       []SecretRef       `json:"secrets"`
	Volumes       []VolumeMount     `json:"volumes"`
	Resources     Resources         `json:"resources"`
	Network       NetworkPolicy     `json:"network"`
	Replicas      int               `json:"replicas"`
	Scope         string            `json:"scope"`
	SessionID     string            `json:"session_id"`
	TaskID        string            `json:"task_id"`
	TTLSeconds    int64             `json:"ttl_seconds"`
	RestartPolicy string            `json:"restart_policy"`
	DesiredState  string            `json:"desired_state"`
	Labels        map[string]string `json:"labels"`
	ApprovalIDs   []string          `json:"approval_ids"`
}

// NodeTemplate describes public node intent; the image supplies capabilities.
// Executor provisioning and SDK startup supply identity, transport and lifecycle.
type NodeTemplate struct {
	Name        string            `json:"name"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Requires    []Requirement     `json:"requires,omitempty"`
}
type SecretRef struct {
	Name   string `json:"name"`
	Target string `json:"target"`
}
type VolumeMount struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
}
type Resources struct {
	MemoryBytes int64 `json:"memory_bytes"`
	NanoCPUs    int64 `json:"nano_cpus"`
	PidsLimit   int64 `json:"pids_limit"`
}
type NetworkPolicy struct {
	Name   string        `json:"name"`
	Egress bool          `json:"egress"`
	Ports  []PortMapping `json:"ports"`
}
type PortMapping struct {
	ContainerPort int    `json:"container_port"`
	HostPort      int    `json:"host_port"`
	Protocol      string `json:"protocol"`
}

type localSchemaLoader struct{}

func (localSchemaLoader) Load(location string) (any, error) {
	return nil, fmt.Errorf("external schema resource forbidden: %s", location)
}

var schemaOnce sync.Once
var compiledSchemas map[string]*jsonschema.Schema
var schemaError error

func compileContracts() {
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(localSchemaLoader{})
	compiledSchemas = make(map[string]*jsonschema.Schema)
	for _, name := range []string{"node", "capability", "deployment"} {
		data, err := schemas.Files.ReadFile(name + ".schema.json")
		if err != nil {
			schemaError = err
			return
		}
		value, err := parseDocument(data)
		if err != nil {
			schemaError = err
			return
		}
		if err = compiler.AddResource("https://whalemesh.dev/schemas/"+name+".schema.json", value); err != nil {
			schemaError = err
			return
		}
	}
	for _, name := range []string{"node", "capability", "deployment"} {
		value, err := compiler.Compile("https://whalemesh.dev/schemas/" + name + ".schema.json")
		if err != nil {
			schemaError = err
			return
		}
		compiledSchemas[name] = value
	}
	requirement, err := compiler.Compile("https://whalemesh.dev/schemas/node.schema.json#/$defs/requirement")
	if err != nil {
		schemaError = err
		return
	}
	compiledSchemas["requirement"] = requirement
}
func validateContract(name string, value any) error {
	schemaOnce.Do(compileContracts)
	if schemaError != nil {
		return fmt.Errorf("compile contracts: %w", schemaError)
	}
	return compiledSchemas[name].Validate(value)
}
func validateTyped(name string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	document, err := parseDocument(data)
	if err != nil {
		return err
	}
	return validateContract(name, document)
}

// parseDocument rejects duplicate fields, trailing documents, excessive depth and size.
func parseDocument(data []byte) (any, error) {
	if len(data) > MaxDocumentBytes {
		return nil, errors.New("JSON document exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var readValue func(int) (any, error)
	readValue = func(depth int) (any, error) {
		if depth > 64 {
			return nil, errors.New("JSON depth exceeds 64")
		}
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return token, nil
		}
		switch delimiter {
		case '{':
			object := map[string]any{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, errors.New("invalid object key")
				}
				if _, exists := object[key]; exists {
					return nil, fmt.Errorf("duplicate JSON field %q", key)
				}
				value, err := readValue(depth + 1)
				if err != nil {
					return nil, err
				}
				object[key] = value
			}
			_, err = decoder.Token()
			return object, err
		case '[':
			values := []any{}
			for decoder.More() {
				value, err := readValue(depth + 1)
				if err != nil {
					return nil, err
				}
				values = append(values, value)
			}
			_, err = decoder.Token()
			return values, err
		default:
			return nil, errors.New("invalid JSON delimiter")
		}
	}
	value, err := readValue(0)
	if err != nil {
		return nil, err
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, errors.New("trailing JSON data")
	}
	return value, nil
}
func decodeContract(data []byte, name string, target any) error {
	value, err := parseDocument(data)
	if err != nil {
		return err
	}
	if err = validateContract(name, value); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}
func DecodeManifest(data []byte) (Manifest, error) {
	var value Manifest
	if err := decodeContract(data, "node", &value); err != nil {
		return value, err
	}
	return value, ValidateManifest(value)
}
func DecodeDeployment(data []byte) (Deployment, error) {
	var value Deployment
	if err := decodeContract(data, "deployment", &value); err != nil {
		return value, err
	}
	return value, ValidateDeployment(value)
}
func DecodeCapability(data []byte) (Capability, error) {
	var value Capability
	if err := decodeContract(data, "capability", &value); err != nil {
		return value, err
	}
	return value, ValidateCapability(value)
}

var sensitiveKey = regexp.MustCompile(`(?i)(^|[._-])(password|passwd|token|secret|private[_-]?key|api[_-]?key|credential)([._-]|$)`)

func publicMap(values map[string]string) error {
	for key, value := range values {
		if sensitiveKey.MatchString(key) {
			return fmt.Errorf("public field %q may not contain credentials; use a secret reference", key)
		}
		if strings.Contains(value, "-----BEGIN ") || strings.HasPrefix(value, "sk-") || strings.HasPrefix(value, "Bearer ") {
			return fmt.Errorf("public field %q contains credential material", key)
		}
	}
	return nil
}
func validEndpoint(endpoint string) error {
	if strings.HasPrefix(endpoint, "/") && !strings.HasPrefix(endpoint, "//") {
		return safeOperationPath(endpoint)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("endpoint must be HTTP(S) without credentials, query or fragment")
	}
	if parsed.Path != "" {
		return safeOperationPath(parsed.EscapedPath())
	}
	return nil
}
func ValidateManifest(value Manifest) error {
	if err := validateTyped("node", value); err != nil {
		return err
	}
	if value.Security.Identity != value.Identity.Principal || value.Security.TrustNamespace != value.Identity.Namespace {
		return errors.New("security identity and trust namespace must match node identity")
	}
	for _, values := range []map[string]string{value.Identity.Labels, value.Identity.Annotations, value.Metadata} {
		if err := publicMap(values); err != nil {
			return err
		}
	}
	for _, endpoint := range value.Transport.Advertise {
		if err := validEndpoint(endpoint); err != nil {
			return fmt.Errorf("advertise: %w", err)
		}
		if strings.HasPrefix(endpoint, "/") {
			return errors.New("advertise requires absolute HTTP(S) URL")
		}
	}
	seen := map[string]bool{}
	for _, capability := range value.Provides {
		if seen[capability.ID] {
			return fmt.Errorf("duplicate capability %q", capability.ID)
		}
		seen[capability.ID] = true
		if err := ValidateCapability(capability); err != nil {
			return fmt.Errorf("capability %s: %w", capability.ID, err)
		}
	}
	seen = map[string]bool{}
	for _, requirement := range value.Requires {
		if requirement.ID != "" && seen[requirement.ID] {
			return fmt.Errorf("duplicate requirement %q", requirement.ID)
		}
		seen[requirement.ID] = true
		if err := ValidateRequirement(requirement); err != nil {
			return err
		}
	}
	return nil
}
func ValidateCapability(value Capability) error {
	if err := validateTyped("capability", value); err != nil {
		return err
	}
	if _, err := parseVersion(value.Version); err != nil {
		return err
	}
	if err := validEndpoint(value.Endpoint); err != nil {
		return err
	}
	if err := publicMap(value.Attributes); err != nil {
		return err
	}
	if err := checkSemantics(value.Semantics); err != nil {
		return err
	}
	for _, schema := range []json.RawMessage{value.InputSchema, value.OutputSchema} {
		if err := ValidateJSONSchema(schema); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, operation := range value.Operations {
		key := operation.Method + " " + operation.Path
		if seen[key] {
			return fmt.Errorf("duplicate operation %s", key)
		}
		seen[key] = true
		if err := safeOperationPath(operation.Path); err != nil {
			return err
		}
		if err := checkSemantics(operation.Semantics); err != nil {
			return err
		}
		for _, scope := range operation.Scopes {
			if !hasScope(value.Scopes, scope) {
				return fmt.Errorf("operation scope %q not declared by capability", scope)
			}
		}
	}
	if value.Tool != nil {
		if err := ValidateJSONSchema(value.Tool.InputSchema); err != nil {
			return fmt.Errorf("tool input schema: %w", err)
		}
		if err := safeOperationPath(value.Tool.Path); err != nil {
			return err
		}
		if !seen[value.Tool.Method+" "+value.Tool.Path] {
			return errors.New("tool export must reference a declared operation")
		}
		for _, scope := range value.Tool.Scopes {
			if !hasScope(value.Scopes, scope) {
				return fmt.Errorf("tool scope %q not declared by capability", scope)
			}
		}
		for _, operation := range value.Operations {
			if operation.Method == value.Tool.Method && operation.Path == value.Tool.Path {
				for _, scope := range operation.Scopes {
					if !hasScope(value.Tool.Scopes, scope) {
						return fmt.Errorf("tool export omits required operation scope %q", scope)
					}
				}
			}
		}
	}
	return nil
}
func hasScope(values []string, scope string) bool {
	for _, value := range values {
		if value == scope {
			return true
		}
	}
	return false
}
func checkSemantics(value Semantics) error {
	if value.ReadOnly && (value.Mutating || value.SideEffecting) {
		return errors.New("read_only semantics conflict with mutation or side effects")
	}
	return nil
}
func safeOperationPath(value string) error {
	if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || path.Clean(value) != value || strings.ContainsAny(value, "?#%\\\x00\r\n") {
		return fmt.Errorf("unsafe operation path %q", value)
	}
	return nil
}
func ValidateRequirement(value Requirement) error {
	if err := validateTyped("requirement", value); err != nil {
		return err
	}
	if value.Capability == "" && value.Interface == "" {
		return errors.New("requirement needs capability or interface")
	}
	if err := validateConstraint(value.Version); err != nil {
		return err
	}
	return publicMap(value.Attributes)
}

// ValidateJSONSchema compiles a real Draft 2020-12 schema without network access.
func ValidateJSONSchema(data json.RawMessage) error {
	value, err := parseDocument(data)
	if err != nil {
		return err
	}
	switch value.(type) {
	case bool, map[string]any:
	default:
		return errors.New("JSON Schema must be object or boolean")
	}
	if object, ok := value.(map[string]any); ok {
		if draft, exists := object["$schema"]; exists && draft != "https://json-schema.org/draft/2020-12/schema" {
			return errors.New("only JSON Schema draft 2020-12 is supported")
		}
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(localSchemaLoader{})
	if err = compiler.AddResource("https://whalemesh.dev/input.schema.json", value); err != nil {
		return err
	}
	_, err = compiler.Compile("https://whalemesh.dev/input.schema.json")
	return err
}
func ValidateDeployment(value Deployment) error {
	if err := validateTyped("deployment", value); err != nil {
		return err
	}
	for _, values := range []map[string]string{value.Node.Labels, value.Node.Annotations} {
		if err := publicMap(values); err != nil {
			return err
		}
	}
	if len(value.Node.Requires)+len(value.Requires) > 256 {
		return errors.New("deployment supports at most 256 combined requirements")
	}
	seen := map[string]bool{}
	for _, requirements := range [][]Requirement{value.Node.Requires, value.Requires} {
		for _, requirement := range requirements {
			if requirement.ID != "" && seen[requirement.ID] {
				return fmt.Errorf("duplicate deployment requirement %q", requirement.ID)
			}
			seen[requirement.ID] = true
			if err := ValidateRequirement(requirement); err != nil {
				return err
			}
		}
	}
	if value.Scope == "session" && value.SessionID == "" {
		return errors.New("session scope requires session_id")
	}
	if value.Scope == "task" && value.TaskID == "" {
		return errors.New("task scope requires task_id")
	}
	if value.Scope != "global" && value.TTLSeconds == 0 {
		return errors.New("session and task deployments require bounded TTL")
	}
	if value.Scope == "global" && (value.SessionID != "" || value.TaskID != "") {
		return errors.New("global deployment cannot bind session or task")
	}
	if value.Network.Name == "host" || value.Network.Name == "none" || value.Network.Name == "bridge" {
		return errors.New("deployment must use an executor-owned network")
	}
	if err := publicMap(value.Env); err != nil {
		return err
	}
	if err := publicMap(value.Labels); err != nil {
		return err
	}
	for key := range value.Env {
		if strings.HasPrefix(key, "MESH_") || strings.HasPrefix(key, "NODE_") {
			return fmt.Errorf("reserved deployment environment %q", key)
		}
	}
	for key := range value.Labels {
		if strings.HasPrefix(key, "whalemesh.") {
			return fmt.Errorf("reserved executor label %q", key)
		}
	}
	targets := map[string]bool{}
	for _, volume := range value.Volumes {
		if err := safeMount(volume.Target); err != nil {
			return err
		}
		if targets[volume.Target] {
			return errors.New("duplicate mount target")
		}
		targets[volume.Target] = true
	}
	for _, secret := range value.Secrets {
		if err := safeMount(secret.Target); err != nil {
			return err
		}
		if !strings.HasPrefix(secret.Target, "/run/secrets/") {
			return errors.New("secret target must be under /run/secrets")
		}
		if targets[secret.Target] {
			return errors.New("duplicate mount target")
		}
		targets[secret.Target] = true
	}
	ports := map[string]bool{}
	for _, port := range value.Network.Ports {
		key := fmt.Sprintf("%d/%s", port.ContainerPort, port.Protocol)
		if ports[key] {
			return errors.New("duplicate container port")
		}
		ports[key] = true
	}
	approvals := map[string]bool{}
	for _, id := range value.ApprovalIDs {
		if approvals[id] {
			return errors.New("duplicate approval reference")
		}
		approvals[id] = true
	}
	return nil
}
func safeMount(target string) error {
	if !strings.HasPrefix(target, "/") || target == "/" || path.Clean(target) != target || strings.ContainsAny(target, "\\\x00\r\n") {
		return fmt.Errorf("unsafe mount target %q", target)
	}
	for _, reserved := range []string{"/proc", "/sys", "/dev", "/etc", "/identity", "/run/identity", "/var/run", "/run/docker.sock"} {
		if target == reserved || strings.HasPrefix(target, reserved+"/") || strings.HasPrefix(reserved, target+"/") {
			return fmt.Errorf("reserved mount target %q", target)
		}
	}
	return nil
}

type semanticVersion struct {
	major, minor, patch uint64
	pre                 string
}

var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

func parseVersion(value string) (semanticVersion, error) {
	match := versionPattern.FindStringSubmatch(value)
	if match == nil {
		return semanticVersion{}, fmt.Errorf("invalid semantic version %q", value)
	}
	parts := [3]uint64{}
	for index := range parts {
		number, err := strconv.ParseUint(match[index+1], 10, 64)
		if err != nil {
			return semanticVersion{}, err
		}
		parts[index] = number
	}
	for _, identifier := range strings.Split(match[4], ".") {
		if len(identifier) > 1 && identifier[0] == '0' && numericIdentifier(identifier) {
			return semanticVersion{}, errors.New("numeric prerelease identifiers must not have leading zeroes")
		}
	}
	return semanticVersion{parts[0], parts[1], parts[2], match[4]}, nil
}
func numericIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}
func compareVersion(left, right semanticVersion) int {
	for _, pair := range [][2]uint64{{left.major, right.major}, {left.minor, right.minor}, {left.patch, right.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if left.pre == right.pre {
		return 0
	}
	if left.pre == "" {
		return 1
	}
	if right.pre == "" {
		return -1
	}
	a, b := strings.Split(left.pre, "."), strings.Split(right.pre, ".")
	for index := 0; index < len(a) && index < len(b); index++ {
		if a[index] == b[index] {
			continue
		}
		an, bn := numericIdentifier(a[index]), numericIdentifier(b[index])
		if an && bn {
			if len(a[index]) < len(b[index]) || len(a[index]) == len(b[index]) && a[index] < b[index] {
				return -1
			}
			return 1
		}
		if an {
			return -1
		}
		if bn {
			return 1
		}
		if a[index] < b[index] {
			return -1
		}
		return 1
	}
	if len(a) < len(b) {
		return -1
	}
	return 1
}
func constraintTerms(value string) []string {
	return strings.Fields(strings.ReplaceAll(value, ",", " "))
}
func splitConstraint(value string) (string, string) {
	for _, operator := range []string{">=", "<=", ">", "<", "^", "~", "="} {
		if strings.HasPrefix(value, operator) {
			return operator, strings.TrimPrefix(value, operator)
		}
	}
	return "=", value
}
func validateConstraint(value string) error {
	if value == "" || value == "*" {
		return nil
	}
	for _, group := range strings.Split(value, ",") {
		if strings.TrimSpace(group) == "" {
			return errors.New("empty version comparison")
		}
	}
	terms := constraintTerms(value)
	if len(terms) == 0 {
		return errors.New("empty version constraint")
	}
	for _, term := range terms {
		_, version := splitConstraint(term)
		if _, err := parseVersion(version); err != nil {
			return err
		}
	}
	return nil
}

// CompatibleVersion supports exact versions, *, ^, ~ and comma/space comparison conjunctions.
// Prereleases only match constraints that explicitly name a prerelease.
func CompatibleVersion(offered, required string) bool {
	version, err := parseVersion(offered)
	if err != nil {
		return false
	}
	if required == "" || required == "*" {
		return version.pre == ""
	}
	if validateConstraint(required) != nil {
		return false
	}
	terms := constraintTerms(required)
	if version.pre != "" {
		explicit := false
		for _, term := range terms {
			_, text := splitConstraint(term)
			target, _ := parseVersion(text)
			if target.pre != "" && version.major == target.major && version.minor == target.minor && version.patch == target.patch {
				explicit = true
				break
			}
		}
		if !explicit {
			return false
		}
	}
	for _, term := range terms {
		operator, text := splitConstraint(term)
		target, _ := parseVersion(text)
		comparison := compareVersion(version, target)
		switch operator {
		case "=":
			if comparison != 0 {
				return false
			}
		case ">=":
			if comparison < 0 {
				return false
			}
		case "<=":
			if comparison > 0 {
				return false
			}
		case ">":
			if comparison <= 0 {
				return false
			}
		case "<":
			if comparison >= 0 {
				return false
			}
		case "~":
			if comparison < 0 || version.major != target.major || version.minor != target.minor {
				return false
			}
		case "^":
			if comparison < 0 || version.major != target.major {
				return false
			}
			if target.major == 0 && (version.minor != target.minor || (target.minor == 0 && version.patch != target.patch)) {
				return false
			}
		}
	}
	return true
}
func CompatibleInterface(offered Capability, required Requirement) bool {
	return (required.Capability == "" || offered.ID == required.Capability) && (required.Interface == "" || offered.Interface == required.Interface) && CompatibleVersion(offered.Version, required.Version)
}
