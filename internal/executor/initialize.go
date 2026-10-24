package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// initializeVolumes never grants the workload root privileges. A short-lived normal
// node helper prepares only owned named volumes, with no network or capabilities.
func (e *Executor) initializeVolumes(ctx context.Context, record desired, mounts []map[string]any, files, secrets map[string][]byte) error {
	arguments := []string{"--executor-files", "initialize"}
	for _, mount := range mounts {
		target, _ := mount["Target"].(string)
		readOnly, _ := mount["ReadOnly"].(bool)
		if !readOnly && target != "/run/whalemesh" && target != "/run/secrets" {
			arguments = append(arguments, target)
		}
	}
	labels := e.labels(record.Principal, record.Deployment.ID, record.Deployment.Scope, record.Deployment.SessionID, record.Deployment.TaskID)
	labels[labelPrefix+"initializer"] = "true"
	config := map[string]any{"Image": e.options.NodeImage, "Entrypoint": []string{"/app/node"}, "Cmd": arguments, "User": "0:0", "Labels": labels, "HostConfig": map[string]any{"ReadonlyRootfs": true, "CapDrop": []string{"ALL"}, "CapAdd": []string{"CHOWN", "FOWNER"}, "SecurityOpt": []string{"no-new-privileges:true"}, "NetworkMode": "none", "Memory": int64(64 << 20), "NanoCpus": int64(100000000), "PidsLimit": int64(16), "Mounts": mounts, "RestartPolicy": map[string]string{"Name": "no"}}}
	var created struct {
		ID string `json:"Id"`
	}
	if err := e.engine.json(ctx, "POST", "/containers/create", config, &created); err != nil {
		return err
	}
	if created.ID == "" {
		return errors.New("empty initializer ID")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = e.engine.json(cleanup, "DELETE", objectPath("containers", created.ID)+"?force=1&v=1", nil, nil)
	}()
	if err := e.engine.json(ctx, "POST", objectPath("containers", created.ID)+"/start", nil, nil); err != nil {
		return err
	}
	var status struct {
		StatusCode int `json:"StatusCode"`
		Error      *struct {
			Message string `json:"Message"`
		} `json:"Error"`
	}
	if err := e.engine.json(ctx, "POST", objectPath("containers", created.ID)+"/wait?condition=not-running", nil, &status); err != nil {
		return err
	}
	if status.StatusCode != 0 {
		detail := ""
		if response, logErr := e.engine.request(ctx, "GET", objectPath("containers", created.ID)+"/logs?stdout=1&stderr=1&tail=10", nil, ""); logErr == nil {
			stdout, stderr, _ := readFrames(response.Body, 8192)
			_ = response.Body.Close()
			detail = strings.TrimSpace(stdout + " " + stderr)
		}
		return fmt.Errorf("volume initializer exit %d: %s", status.StatusCode, detail)
	}
	if status.Error != nil && status.Error.Message != "" {
		return errors.New(status.Error.Message)
	}
	if err := e.archivePut(ctx, created.ID, "/run/whalemesh", files, 0400); err != nil {
		return err
	}
	// Docker archive writes cannot target the read-only rootfs's tmpfs. Install
	// trusted tooling once in the private identity volume, read-only to workloads.
	if err := e.installHelper(ctx, created.ID, "/run/whalemesh"); err != nil {
		return err
	}
	if len(secrets) > 0 {
		if err := e.archivePut(ctx, created.ID, "/run/secrets", secrets, 0400); err != nil {
			return err
		}
	}
	return nil
}
func (e *Executor) recoverExecutions(ctx context.Context) {
	e.mu.Lock()
	interrupted := []Job{}
	for _, j := range e.state.Jobs {
		if j.Kind == "exec" && j.State == "interrupted" && j.EngineID != "" {
			interrupted = append(interrupted, j)
		}
	}
	e.mu.Unlock()
	for _, j := range interrupted {
		var status struct {
			Running bool `json:"Running"`
		}
		if err := e.engine.json(ctx, "GET", objectPath("exec", j.EngineID)+"/json", nil, &status); err != nil {
			continue
		}
		if status.Running {
			if _, err := e.inspect(ctx, j.ContainerID, j.Principal, false); err == nil {
				e.killOnFailure(j.ContainerID)
			}
		}
	}
}
