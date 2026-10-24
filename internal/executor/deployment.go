package executor

import (
	"archive/tar"
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
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/WhaleTrue/WhaleBot/pkg/mesh"
)

type container struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	State  json.RawMessage   `json:"State"`
	Labels map[string]string `json:"Labels"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

func (e *Executor) containers(ctx context.Context) ([]container, error) {
	filters, _ := json.Marshal(map[string][]string{"label": {labelPrefix + "managed=true", labelPrefix + "executor=" + e.node.Manifest().Identity.Principal, labelPrefix + "executor_key=" + e.node.KeyID(), labelPrefix + "namespace=" + e.node.Manifest().Identity.Namespace}})
	var out []container
	err := e.engine.json(ctx, "GET", "/containers/json?all=1&filters="+url.QueryEscape(string(filters)), nil, &out)
	return out, err
}
func (e *Executor) inspect(ctx context.Context, id, principal string, admin bool) (container, error) {
	if !validID(id) {
		return container{}, errors.New("invalid container ID")
	}
	var c container
	if err := e.engine.json(ctx, "GET", objectPath("containers", id)+"/json", nil, &c); err != nil {
		return c, err
	}
	c.Labels = c.Config.Labels
	if !e.owned(c.Labels, principal, admin) {
		return c, errors.New("container is outside authorized ownership")
	}
	if c.ID == "" {
		return c, errors.New("Engine returned empty container ID")
	}
	return c, nil
}
func (e *Executor) validate(d mesh.Deployment, principal string) error {
	if err := mesh.ValidateDeployment(d); err != nil {
		return err
	}
	if !identifier(d.ID) {
		return errors.New("deployment ID must use lowercase letters, digits, hyphen or underscore")
	}
	if d.Namespace != e.node.Manifest().Identity.Namespace {
		return errors.New("deployment namespace is not executor namespace")
	}
	if err := e.imageAllowed(d.Image, principal, d.ApprovalIDs); err != nil {
		return err
	}
	if d.Network.Name != "" && d.Network.Name != e.options.Network {
		return errors.New("network is not administrator configured mesh network")
	}
	if len(d.Network.Ports) > 0 {
		return errors.New("host port publication disabled; use authenticated mesh reachability")
	}
	if d.Resources.MemoryBytes < 0 || d.Resources.MemoryBytes > e.options.MaxMemoryBytes || d.Resources.NanoCPUs < 0 || d.Resources.NanoCPUs > e.options.MaxNanoCPUs || d.Resources.PidsLimit < 0 || d.Resources.PidsLimit > e.options.MaxPids {
		return errors.New("resource limit exceeds executor policy")
	}
	for key := range d.Env {
		if strings.HasPrefix(key, "MESH_") || key == "NODE_PACKAGES" || key == "DOCKER_HOST" || strings.HasPrefix(key, "EXECUTOR_") {
			return errors.New("reserved bootstrap environment variable")
		}
		if strings.ContainsAny(key, "=\x00") || key == "" {
			return errors.New("invalid environment key")
		}
	}
	for _, v := range d.Volumes {
		if !identifier(v.Source) {
			return errors.New("volume source must be a logical named volume")
		}
		clean := path.Clean(v.Target)
		if clean != v.Target || !strings.HasPrefix(clean, "/") || clean == "/" {
			return errors.New("invalid volume target")
		}
		for _, reserved := range []string{"/run", "/proc", "/sys", "/dev", "/tmp", "/workspace", "/app", "/identity", "/data"} {
			if clean == reserved || strings.HasPrefix(clean, reserved+"/") {
				return errors.New("volume target overlaps reserved path")
			}
		}
	}
	for _, s := range d.Secrets {
		if !strings.HasPrefix(s.Target, "/run/secrets/") || !identifier(strings.TrimPrefix(s.Target, "/run/secrets/")) || s.Name == "" {
			return errors.New("secret target must be /run/secrets/<safe filename>")
		}
	}
	principals := map[string]bool{}
	for i := range d.Replicas {
		identity, _, err := e.identity(d, i)
		if err != nil {
			return err
		}
		var child struct {
			Principal string `json:"principal"`
		}
		_ = json.Unmarshal(identity, &child)
		if principals[child.Principal] {
			return errors.New("replicas cannot share a principal")
		}
		principals[child.Principal] = true
	}
	return nil
}
func (e *Executor) identity(d mesh.Deployment, replica int) ([]byte, []byte, error) {
	if e.options.IdentitiesDir == "" {
		return nil, nil, errors.New("EXECUTOR_IDENTITIES_DIR required for child identities")
	}
	dir := filepath.Join(e.options.IdentitiesDir, d.ID+"-"+strconv.Itoa(replica))
	identity, err := os.ReadFile(filepath.Join(dir, "identity.json"))
	if err != nil {
		return nil, nil, fmt.Errorf("preprovisioned child identity: %w", err)
	}
	trust, err := os.ReadFile(filepath.Join(dir, "trust.json"))
	if err != nil {
		return nil, nil, err
	}
	var value struct {
		Principal  string `json:"principal"`
		PrivateKey string `json:"private_key"`
	}
	if err = json.Unmarshal(identity, &value); err != nil || value.Principal == "" || value.PrivateKey == "" || value.Principal == e.node.Manifest().Identity.Principal {
		return nil, nil, errors.New("invalid or reused executor identity")
	}
	key, err := base64.StdEncoding.DecodeString(value.PrivateKey)
	if err != nil || len(key) != ed25519.PrivateKeySize || !ed25519.NewKeyFromSeed(key[:ed25519.SeedSize]).Equal(ed25519.PrivateKey(key)) {
		return nil, nil, errors.New("invalid child Ed25519 private key")
	}
	fingerprint := sha256.Sum256(key[ed25519.SeedSize:])
	if hex.EncodeToString(fingerprint[:]) == e.node.KeyID() {
		return nil, nil, errors.New("child identity cannot reuse executor private key")
	}
	var grants map[string]mesh.TrustGrant
	if err = json.Unmarshal(trust, &grants); err != nil {
		return nil, nil, fmt.Errorf("child trust: %w", err)
	}
	grant, ok := grants[value.Principal]
	public, err := base64.StdEncoding.DecodeString(grant.PublicKey)
	if !ok || grant.Namespace != d.Namespace || err != nil || !ed25519.PublicKey(public).Equal(ed25519.PrivateKey(key).Public()) {
		return nil, nil, errors.New("child identity requires matching namespace and public key trust grant")
	}
	for _, requirements := range [][]mesh.Requirement{d.Node.Requires, d.Requires} {
		for _, requirement := range requirements {
			for _, scope := range requirement.Scopes {
				allowed := false
				for _, permission := range grant.Scopes {
					if permission == scope || permission == "*" || strings.HasSuffix(permission, ".*") && strings.HasPrefix(scope, strings.TrimSuffix(permission, "*")) {
						allowed = true
						break
					}
				}
				if !allowed {
					return nil, nil, fmt.Errorf("assigned child identity lacks scope %q", scope)
				}
			}
		}
	}
	return identity, trust, nil
}
func (e *Executor) deploymentsHTTP(w http.ResponseWriter, r *http.Request) {
	principal, err := e.actor(r, r.URL.Query().Get("owner"))
	if err != nil {
		fail(w, err)
		return
	}
	if r.Method == "GET" {
		e.mu.Lock()
		out := make([]desired, 0)
		for _, d := range e.state.Deployments {
			if d.Principal == principal || e.admin(r) {
				out = append(out, d)
			}
		}
		e.mu.Unlock()
		respond(w, 200, out)
		return
	}
	e.operation.Lock()
	defer e.operation.Unlock()
	var d mesh.Deployment
	if r.Method == "DELETE" {
		key := deploymentKey(e.node.Manifest().Identity.Namespace, r.URL.Query().Get("id"))
		e.mu.Lock()
		record, ok := e.state.Deployments[key]
		e.mu.Unlock()
		if !ok || (record.Principal != principal && !e.admin(r)) {
			fail(w, errors.New("deployment not found or not owned"))
			return
		}
		d = record.Deployment
		d.DesiredState = "absent"
		principal = record.Principal
	} else {
		data, readErr := io.ReadAll(io.LimitReader(r.Body, mesh.MaxDocumentBytes+1))
		r.Body.Close()
		if readErr != nil {
			fail(w, readErr)
			return
		}
		var decodeErr error
		d, decodeErr = mesh.DecodeDeployment(data)
		if decodeErr != nil {
			fail(w, decodeErr)
			return
		}
		if err := e.validate(d, principal); err != nil {
			fail(w, err)
			return
		}
	}
	key := deploymentKey(d.Namespace, d.ID)
	e.mu.Lock()
	old, exists := e.state.Deployments[key]
	if exists && old.Principal != principal {
		e.mu.Unlock()
		fail(w, errors.New("deployment belongs to another principal"))
		return
	}
	e.state.Deployments[key] = desired{Deployment: d, Principal: principal, UpdatedAt: time.Now().UTC()}
	err = e.saveLocked()
	e.mu.Unlock()
	if err != nil {
		fail(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	err = e.reconcile(ctx, key)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, 200, map[string]any{"deployment": d, "reconciled": true})
}
func (e *Executor) reconcile(ctx context.Context, key string) error {
	e.mu.Lock()
	record, ok := e.state.Deployments[key]
	e.mu.Unlock()
	if !ok {
		return nil
	}
	d := record.Deployment
	err := e.reconcileDesired(ctx, record)
	e.mu.Lock()
	if current, ok := e.state.Deployments[key]; ok {
		current.Error = ""
		if err != nil {
			current.Error = err.Error()
		}
		e.state.Deployments[key] = current
	}
	saveErr := e.saveLocked()
	e.mu.Unlock()
	if err != nil {
		return err
	}
	if d.DesiredState == "absent" && saveErr == nil {
		return nil
	}
	return saveErr
}
func (e *Executor) reconcileDesired(ctx context.Context, record desired) error {
	d := record.Deployment
	all, err := e.containers(ctx)
	if err != nil {
		return err
	}
	owned := []container{}
	for _, c := range all {
		if e.owned(c.Labels, record.Principal, false) && c.Labels[labelPrefix+"deployment"] == d.ID {
			owned = append(owned, c)
		}
	}
	if d.DesiredState == "absent" || d.Replicas == 0 {
		for _, c := range owned {
			if err = e.remove(ctx, c.ID, record.Principal, false); err != nil {
				return err
			}
		}
		return e.cleanupVolumes(ctx, d.ID, record.Principal)
	}
	if err = e.validate(d, record.Principal); err != nil {
		return err
	}
	b, _ := json.Marshal(d)
	hash := sha256.Sum256(b)
	revision := hex.EncodeToString(hash[:])[:12]
	keep := map[string]bool{}
	for i := range d.Replicas {
		replica := strconv.Itoa(i)
		var current *container
		for index := range owned {
			c := &owned[index]
			if c.Labels[labelPrefix+"replica"] == replica && c.Labels[labelPrefix+"revision"] == revision {
				current = c
				break
			}
		}
		if current == nil {
			previous := []container{}
			for _, c := range owned {
				if c.Labels[labelPrefix+"replica"] == replica {
					previous = append(previous, c)
					if err = e.engine.json(ctx, "POST", objectPath("containers", c.ID)+"/stop?t=10", nil, nil); err != nil {
						return err
					}
				}
			}
			created, createErr := e.create(ctx, record, i, revision)
			if createErr != nil {
				for _, c := range previous {
					_ = e.engine.json(ctx, "POST", objectPath("containers", c.ID)+"/start", nil, nil)
				}
				return createErr
			}
			current = &created
		}
		keep[current.ID] = true
		if d.DesiredState == "stopped" {
			if err = e.engine.json(ctx, "POST", objectPath("containers", current.ID)+"/stop?t=10", nil, nil); err != nil {
				return err
			}
		} else {
			if err = e.engine.json(ctx, "POST", objectPath("containers", current.ID)+"/start", nil, nil); err != nil {
				return err
			}
		}
	}
	for _, c := range owned {
		if !keep[c.ID] {
			if err = e.remove(ctx, c.ID, record.Principal, false); err != nil {
				return err
			}
		}
	}
	return nil
}
func volumeName(namespace, executor, deployment, logical string) string {
	sum := sha256.Sum256([]byte(namespace + "/" + executor + "/" + deployment + "/" + logical))
	return "whalemesh-" + hex.EncodeToString(sum[:16])
}
func (e *Executor) volume(ctx context.Context, record desired, logical string) (string, error) {
	d := record.Deployment
	name := volumeName(d.Namespace, e.node.Manifest().Identity.Principal+"/"+e.node.KeyID(), d.ID, logical)
	var out struct {
		Labels map[string]string `json:"Labels"`
	}
	err := e.engine.json(ctx, "GET", objectPath("volumes", name), nil, &out)
	if err == nil {
		if !e.owned(out.Labels, record.Principal, false) || out.Labels[labelPrefix+"deployment"] != d.ID {
			return "", errors.New("volume ownership mismatch")
		}
		return name, nil
	}
	if !missing(err) {
		return "", err
	}
	err = e.engine.json(ctx, "POST", "/volumes/create", map[string]any{"Name": name, "Labels": e.labels(record.Principal, d.ID, d.Scope, d.SessionID, d.TaskID)}, nil)
	return name, err
}
func (e *Executor) create(ctx context.Context, record desired, replica int, revision string) (container, error) {
	d := record.Deployment
	identity, trust, err := e.identity(d, replica)
	if err != nil {
		return container{}, err
	}
	var image struct {
		ID           string `json:"Id"`
		OS           string `json:"Os"`
		Architecture string `json:"Architecture"`
		Config       struct {
			Volumes map[string]any `json:"Volumes"`
		} `json:"Config"`
	}
	if err = e.engine.json(ctx, "GET", objectPath("images", d.Image)+"/json", nil, &image); err != nil {
		if !missing(err) {
			return container{}, err
		}
		if err = e.engine.pull(ctx, d.Image); err != nil {
			return container{}, err
		}
		if err = e.engine.json(ctx, "GET", objectPath("images", d.Image)+"/json", nil, &image); err != nil {
			return container{}, err
		}
	}
	if image.OS != "linux" {
		return container{}, errors.New("executor supports Linux Mesh Node images only")
	}
	if len(image.Config.Volumes) > 0 {
		return container{}, errors.New("image-declared volumes prohibited; declare owned volumes explicitly")
	}
	mounts := []map[string]any{}
	for _, pair := range []struct{ name, target string }{{"workspace-" + strconv.Itoa(replica), "/workspace"}, {"identity-" + strconv.Itoa(replica), "/run/whalemesh"}, {"secrets-" + strconv.Itoa(replica), "/run/secrets"}, {"data-" + strconv.Itoa(replica), "/data"}} {
		name, err := e.volume(ctx, record, pair.name)
		if err != nil {
			return container{}, err
		}
		mounts = append(mounts, map[string]any{"Type": "volume", "Source": name, "Target": pair.target, "VolumeOptions": map[string]any{"NoCopy": true}})
	}
	for _, v := range d.Volumes {
		name, err := e.volume(ctx, record, "user-"+v.Source)
		if err != nil {
			return container{}, err
		}
		mounts = append(mounts, map[string]any{"Type": "volume", "Source": name, "Target": v.Target, "ReadOnly": v.ReadOnly, "VolumeOptions": map[string]any{"NoCopy": true}})
	}
	labels := e.labels(record.Principal, d.ID, d.Scope, d.SessionID, d.TaskID)
	labels[labelPrefix+"replica"] = strconv.Itoa(replica)
	labels[labelPrefix+"revision"] = revision
	labels[labelPrefix+"created"] = time.Now().UTC().Format(time.RFC3339Nano)
	name := "wm-" + d.ID[:min(24, len(d.ID))] + "-" + strconv.Itoa(replica) + "-" + e.node.KeyID()[:8] + "-" + revision
	manifest := childManifest(d, e.node.Manifest().Identity.HostID)
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		return container{}, err
	}
	env := []string{"MESH_MANIFEST=/run/whalemesh/manifest.json", "MESH_LISTEN=:8080", "MESH_ADVERTISE=http://" + name + ":8080", "MESH_NAMESPACE=" + d.Namespace, "MESH_NAME=" + d.Node.Name, "MESH_HOST_ID=" + manifest.Identity.HostID, "MESH_DATA=/data", "MESH_IDENTITY_FILE=/run/whalemesh/identity.json", "MESH_TRUST_FILE=/run/whalemesh/trust.json", "MESH_SEEDS=" + strings.Join(e.node.Manifest().Transport.Advertise, ",")}
	for key, value := range d.Env {
		env = append(env, key+"="+value)
	}
	memory, cpus, pids := d.Resources.MemoryBytes, d.Resources.NanoCPUs, d.Resources.PidsLimit
	if memory == 0 {
		memory = 512 << 20
	}
	if cpus == 0 {
		cpus = 1e9
	}
	if pids == 0 {
		pids = 128
	}
	network := e.options.Network
	if !d.Network.Egress {
		var info struct {
			Internal bool `json:"Internal"`
		}
		if err = e.engine.json(ctx, "GET", objectPath("networks", network), nil, &info); err != nil {
			return container{}, err
		}
		if !info.Internal {
			return container{}, errors.New("egress=false requires administrator-provisioned internal Docker network")
		}
	}
	config := map[string]any{"Image": image.ID, "User": "65532:65532", "WorkingDir": "/workspace", "Env": env, "Labels": labels, "Hostname": name, "HostConfig": map[string]any{"ReadonlyRootfs": true, "Privileged": false, "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges:true"}, "Memory": memory, "MemorySwap": memory, "NanoCpus": cpus, "PidsLimit": pids, "Mounts": mounts, "Tmpfs": map[string]string{"/tmp": "rw,nosuid,nodev,size=67108864,mode=1777"}, "RestartPolicy": map[string]string{"Name": d.RestartPolicy}, "NetworkMode": network, "LogConfig": map[string]any{"Type": "json-file", "Config": map[string]string{"max-size": "10m", "max-file": "2"}}}, "NetworkingConfig": map[string]any{"EndpointsConfig": map[string]any{network: map[string]any{"Aliases": []string{name}}}}}
	if len(d.Command) > 0 {
		config["Entrypoint"] = d.Command
		config["Cmd"] = []string{}
	}
	files := map[string][]byte{"identity.json": identity, "trust.json": trust, "manifest.json": manifestData}
	secrets := map[string][]byte{}
	for _, secret := range d.Secrets {
		bindings, resolveErr := e.node.Resolve(mesh.Requirement{Capability: "secrets.resolve", Namespace: d.Namespace})
		if resolveErr != nil || len(bindings) == 0 {
			return container{}, errors.New("secrets.resolve provider unavailable")
		}
		var value struct {
			Value string `json:"value"`
		}
		if err = e.node.Call(ctx, bindings[0], "POST", "/secrets/resolve", map[string]string{"key": secret.Name, "owner": record.Principal}, &value); err != nil {
			return container{}, err
		}
		secrets[strings.TrimPrefix(secret.Target, "/run/secrets/")] = []byte(value.Value)
	}
	if err = e.initializeVolumes(ctx, record, mounts, files, secrets); err != nil {
		return container{}, err
	}
	for _, mount := range mounts {
		if mount["Target"] == "/run/whalemesh" || mount["Target"] == "/run/secrets" {
			mount["ReadOnly"] = true
		}
	}
	var out container
	if err = e.engine.json(ctx, "POST", "/containers/create?name="+url.QueryEscape(name), config, &out); err != nil {
		return out, err
	}
	success := false
	defer func() {
		if !success {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = e.engine.json(cleanup, "DELETE", objectPath("containers", out.ID)+"?force=1&v=1", nil, nil)
		}
	}()
	if err = e.engine.json(ctx, "POST", objectPath("containers", out.ID)+"/start", nil, nil); err != nil {
		return out, err
	}
	if err = e.touch(out.ID); err != nil {
		return out, err
	}
	out.Labels = labels
	success = true
	return out, nil
}
func childManifest(d mesh.Deployment, host string) mesh.Manifest {
	requirements := make([]mesh.Requirement, 0, len(d.Node.Requires)+len(d.Requires))
	requirements = append(requirements, d.Node.Requires...)
	requirements = append(requirements, d.Requires...)
	return mesh.Manifest{
		Identity: mesh.Identity{Namespace: d.Namespace, Name: d.Node.Name, HostID: host, Labels: d.Node.Labels, Annotations: d.Node.Annotations},
		Requires: requirements,
	}
}

func (e *Executor) archivePut(ctx context.Context, id, destination string, files map[string][]byte, mode int64) error {
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for name, data := range files {
		if strings.Contains(name, "/") {
			directory := path.Dir(name)
			if err := writer.WriteHeader(&tar.Header{Name: directory + "/", Typeflag: tar.TypeDir, Mode: 0700, Uid: 65532, Gid: 65532}); err != nil {
				return err
			}
		}
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(data)), Uid: 65532, Gid: 65532}); err != nil {
			return err
		}
		if _, err := writer.Write(data); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	r, err := e.engine.request(ctx, "PUT", objectPath("containers", id)+"/archive?path="+url.QueryEscape(destination)+"&noOverwriteDirNonDir=1", &buffer, "application/x-tar")
	if err != nil {
		return err
	}
	defer r.Body.Close()
	_, err = io.Copy(io.Discard, r.Body)
	return err
}
func (e *Executor) remove(ctx context.Context, id, principal string, admin bool) error {
	c, err := e.inspect(ctx, id, principal, admin)
	if err != nil {
		if missing(err) {
			return nil
		}
		return err
	}
	if err = e.engine.json(ctx, "DELETE", objectPath("containers", c.ID)+"?force=1&v=1", nil, nil); err != nil {
		return err
	}
	e.mu.Lock()
	delete(e.state.Activity, c.ID)
	err = e.saveLocked()
	e.mu.Unlock()
	return err
}
func (e *Executor) cleanupVolumes(ctx context.Context, deployment, principal string) error {
	filters, _ := json.Marshal(map[string][]string{"label": {labelPrefix + "executor=" + e.node.Manifest().Identity.Principal, labelPrefix + "deployment=" + deployment}})
	var response struct {
		Volumes []struct {
			Name   string            `json:"Name"`
			Labels map[string]string `json:"Labels"`
		} `json:"Volumes"`
	}
	if err := e.engine.json(ctx, "GET", "/volumes?filters="+url.QueryEscape(string(filters)), nil, &response); err != nil {
		return err
	}
	for _, v := range response.Volumes {
		if e.owned(v.Labels, principal, false) {
			if err := e.engine.json(ctx, "DELETE", objectPath("volumes", v.Name), nil, nil); err != nil && !missing(err) {
				return err
			}
		}
	}
	return nil
}
func (e *Executor) loop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		e.background()
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (e *Executor) background() {
	ctx, cancel := context.WithTimeout(e.ctx, 2*time.Minute)
	defer cancel()
	e.operation.Lock()
	defer e.operation.Unlock()
	e.cleanupArtifacts()
	e.recoverExecutions(ctx)
	e.mu.Lock()
	records := map[string]desired{}
	for key, value := range e.state.Deployments {
		records[key] = value
	}
	e.mu.Unlock()
	for key, record := range records {
		if record.Deployment.Scope != "global" && record.Deployment.DesiredState != "absent" {
			all, err := e.containers(ctx)
			if err != nil {
				continue
			}
			last := record.UpdatedAt
			busy := false
			e.mu.Lock()
			for _, c := range all {
				if c.Labels[labelPrefix+"deployment"] == record.Deployment.ID {
					if activity := e.state.Activity[c.ID]; activity.After(last) {
						last = activity
					}
					for _, j := range e.state.Jobs {
						if j.ContainerID == c.ID && (j.State == "running" || j.State == "queued") {
							busy = true
						}
					}
				}
			}
			if !busy && time.Since(last) > time.Duration(record.Deployment.TTLSeconds)*time.Second {
				record.Deployment.DesiredState = "absent"
				e.state.Deployments[key] = record
				_ = e.saveLocked()
			}
			e.mu.Unlock()
		}
		_ = e.reconcile(ctx, key)
	}
	e.mu.Lock()
	changed := false
	for id, j := range e.state.Jobs {
		if !j.FinishedAt.IsZero() && time.Since(j.FinishedAt) > 7*24*time.Hour {
			delete(e.state.Jobs, id)
			changed = true
		}
	}
	for id, a := range e.state.Approvals {
		if time.Now().After(a.ExpiresAt) {
			delete(e.state.Approvals, id)
			changed = true
		}
	}
	if changed {
		_ = e.saveLocked()
	}
	e.mu.Unlock()
}
