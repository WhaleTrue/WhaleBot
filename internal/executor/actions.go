package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ExecRequest struct {
	ID             string            `json:"id"`
	Command        []string          `json:"command"`
	Cwd            string            `json:"cwd,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	Async          bool              `json:"async,omitempty"`
	Owner          string            `json:"owner,omitempty"`
}

func (e *Executor) actor(r *http.Request, owner string) (string, error) {
	if owner == "" {
		return "node:" + e.node.Principal(r), nil
	}
	if !e.node.Authorized(r, "container.delegate") {
		return "", errors.New("container.delegate required for owner delegation")
	}
	if len(owner) > 256 || strings.ContainsAny(owner, "\x00\r\n") {
		return "", errors.New("invalid owner")
	}
	*r = *r.WithContext(context.WithValue(r.Context(), delegatedOwnerKey{}, owner))
	return owner, nil
}
func (e *Executor) listHTTP(w http.ResponseWriter, r *http.Request) {
	principal, err := e.actor(r, r.URL.Query().Get("owner"))
	if err != nil {
		fail(w, err)
		return
	}
	all, err := e.containers(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	out := []container{}
	for _, c := range all {
		if e.owned(c.Labels, principal, e.admin(r)) {
			out = append(out, c)
		}
	}
	respond(w, 200, out)
}
func (e *Executor) actionHTTP(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ID     string `json:"id"`
		Action string `json:"action"`
		Owner  string `json:"owner,omitempty"`
	}
	if err := decode(r, &input); err != nil {
		fail(w, err)
		return
	}
	principal, err := e.actor(r, input.Owner)
	if err != nil {
		fail(w, err)
		return
	}
	e.operation.Lock()
	defer e.operation.Unlock()
	c, err := e.inspect(r.Context(), input.ID, principal, e.admin(r))
	if err != nil {
		fail(w, err)
		return
	}
	switch input.Action {
	case "touch":
		err = e.touch(c.ID)
	case "start", "stop", "restart":
		suffix := "/" + input.Action
		if input.Action != "start" {
			suffix += "?t=10"
		}
		err = e.engine.json(r.Context(), "POST", objectPath("containers", c.ID)+suffix, nil, nil)
		if err == nil {
			err = e.touch(c.ID)
		}
	case "remove":
		err = e.remove(r.Context(), c.ID, principal, e.admin(r))
	default:
		err = errors.New("action must be start, stop, restart, remove or touch")
	}
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, 200, map[string]bool{"success": true})
}
func (e *Executor) logsHTTP(w http.ResponseWriter, r *http.Request) {
	principal, err := e.actor(r, r.URL.Query().Get("owner"))
	if err != nil {
		fail(w, err)
		return
	}
	c, err := e.inspect(r.Context(), r.URL.Query().Get("id"), principal, e.admin(r))
	if err != nil {
		fail(w, err)
		return
	}
	response, err := e.engine.request(r.Context(), "GET", objectPath("containers", c.ID)+"/logs?stdout=1&stderr=1&tail=1000", nil, "")
	if err != nil {
		fail(w, err)
		return
	}
	defer response.Body.Close()
	stdout, stderr, err := readFrames(response.Body, 4<<20)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, 200, map[string]string{"stdout": stdout, "stderr": stderr})
}
func (e *Executor) execCreate(ctx context.Context, id string, command []string, cwd string, env map[string]string, root bool) (string, error) {
	environment := []string{}
	for k, v := range env {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) {
			return "", errors.New("invalid exec environment")
		}
		environment = append(environment, k+"="+v)
	}
	user := "65532:65532"
	if root {
		user = "0:0"
	}
	var result struct {
		ID string `json:"Id"`
	}
	err := e.engine.json(ctx, "POST", objectPath("containers", id)+"/exec", map[string]any{"AttachStdout": true, "AttachStderr": true, "AttachStdin": false, "Tty": false, "Cmd": command, "WorkingDir": cwd, "Env": environment, "User": user, "Privileged": false}, &result)
	if err == nil && result.ID == "" {
		err = errors.New("Docker returned empty exec ID")
	}
	return result.ID, err
}
func (e *Executor) execStart(ctx context.Context, containerID, execID string, max int64) (string, string, int, error) {
	response, err := e.engine.request(ctx, "POST", objectPath("exec", execID)+"/start", strings.NewReader(`{"Detach":false,"Tty":false}`), "application/json")
	if err != nil {
		e.killOnFailure(containerID)
		return "", "", -1, err
	}
	defer response.Body.Close()
	stdout, stderr, err := readFrames(response.Body, max)
	if err != nil {
		e.killOnFailure(containerID)
		return "", "", -1, err
	}
	var status struct {
		Running  bool `json:"Running"`
		ExitCode int  `json:"ExitCode"`
	}
	if err = e.engine.json(ctx, "GET", objectPath("exec", execID)+"/json", nil, &status); err != nil {
		return stdout, stderr, -1, err
	}
	if status.Running {
		return stdout, stderr, -1, errors.New("exec stream ended before process exit")
	}
	return stdout, stderr, status.ExitCode, nil
}
func (e *Executor) killOnFailure(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = e.engine.json(ctx, "POST", objectPath("containers", id)+"/kill?signal=KILL", nil, nil)
}
func (e *Executor) runCommand(ctx context.Context, id string, command []string, cwd string, env map[string]string, root bool, max int64) (string, string, int, error) {
	execID, err := e.execCreate(ctx, id, command, cwd, env, root)
	if err != nil {
		return "", "", -1, err
	}
	return e.execStart(ctx, id, execID, max)
}
func (e *Executor) newJob(principal, kind, id string) (Job, error) {
	select {
	case e.jobs <- struct{}{}:
	default:
		return Job{}, errors.New("executor job capacity reached")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.state.Jobs) >= 2048 {
		<-e.jobs
		return Job{}, errors.New("executor durable job retention full")
	}
	j := Job{ID: randomID(), Principal: principal, Kind: kind, ContainerID: id, State: "queued", StartedAt: time.Now().UTC(), ExitCode: -1}
	e.state.Jobs[j.ID] = j
	if err := e.saveLocked(); err != nil {
		delete(e.state.Jobs, j.ID)
		<-e.jobs
		return Job{}, err
	}
	return j, nil
}
func (e *Executor) saveJob(j Job) error {
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Join(e.dir, "jobs"), 0700); err != nil {
		return err
	}
	if err = writeDurable(filepath.Join(e.dir, "jobs", j.ID+".json"), data); err != nil {
		return err
	}
	j.Stdout = ""
	j.Stderr = ""
	e.mu.Lock()
	defer e.mu.Unlock()
	e.state.Jobs[j.ID] = j
	return e.saveLocked()
}
func (e *Executor) execHTTP(w http.ResponseWriter, r *http.Request) {
	var input ExecRequest
	if err := decode(r, &input); err != nil {
		fail(w, err)
		return
	}
	if len(input.Command) == 0 || input.Command[0] == "" {
		fail(w, errors.New("command argv required; no implicit shell"))
		return
	}
	principal, err := e.actor(r, input.Owner)
	if err != nil {
		fail(w, err)
		return
	}
	c, err := e.inspect(r.Context(), input.ID, principal, e.admin(r))
	if err != nil {
		fail(w, err)
		return
	}
	cwd, err := workspacePath(input.Cwd)
	if err != nil {
		fail(w, err)
		return
	}
	if input.TimeoutSeconds == 0 {
		input.TimeoutSeconds = 300
	}
	if input.TimeoutSeconds < 1 || input.TimeoutSeconds > 7200 {
		fail(w, errors.New("timeout_seconds must be 1..7200"))
		return
	}
	job, err := e.newJob(principal, "exec", c.ID)
	if err != nil {
		fail(w, err)
		return
	}
	if err = e.touch(c.ID); err != nil {
		<-e.jobs
		job.State = "failed"
		job.Error = err.Error()
		job.FinishedAt = time.Now().UTC()
		_ = e.saveJob(job)
		fail(w, err)
		return
	}
	run := func(ctx context.Context) Job {
		defer func() { <-e.jobs }()
		ctx, cancel := context.WithTimeout(ctx, time.Duration(input.TimeoutSeconds)*time.Second)
		defer cancel()
		job.State = "running"
		execID, err := e.execCreate(ctx, c.ID, input.Command, cwd, input.Env, false)
		job.EngineID = execID
		if err == nil {
			err = e.saveJob(job)
		}
		if err == nil {
			job.Stdout, job.Stderr, job.ExitCode, err = e.execStart(ctx, c.ID, execID, 4<<20)
		}
		job.State = "complete"
		if err != nil {
			job.State = "failed"
			job.Error = err.Error()
		}
		job.FinishedAt = time.Now().UTC()
		if saveErr := e.saveJob(job); saveErr != nil {
			job.Error = saveErr.Error()
			job.State = "failed"
		}
		_ = e.touch(c.ID)
		return job
	}
	if input.Async {
		go run(e.ctx)
		respond(w, 202, job)
		return
	}
	respond(w, 200, run(r.Context()))
}
func (e *Executor) jobHTTP(w http.ResponseWriter, r *http.Request) {
	principal, err := e.actor(r, r.URL.Query().Get("owner"))
	if err != nil {
		fail(w, err)
		return
	}
	e.mu.Lock()
	j, ok := e.state.Jobs[r.URL.Query().Get("id")]
	e.mu.Unlock()
	if !ok || j.Principal != principal && !e.admin(r) {
		fail(w, errors.New("job not found or not owned"))
		return
	}
	if strings.Contains(r.URL.Path, "/images/") && j.Kind != "pull" {
		fail(w, errors.New("wrong job kind"))
		return
	}
	if data, err := os.ReadFile(filepath.Join(e.dir, "jobs", j.ID+".json")); err == nil {
		var output Job
		if json.Unmarshal(data, &output) == nil {
			j.Stdout = output.Stdout
			j.Stderr = output.Stderr
		}
	}
	respond(w, 200, j)
}
func (e *Executor) imagesHTTP(w http.ResponseWriter, r *http.Request) {
	principal, err := e.actor(r, r.URL.Query().Get("owner"))
	if err != nil {
		fail(w, err)
		return
	}
	var all []struct {
		ID          string   `json:"Id"`
		RepoTags    []string `json:"RepoTags"`
		RepoDigests []string `json:"RepoDigests"`
		Size        int64    `json:"Size"`
	}
	if err := e.engine.json(r.Context(), "GET", "/images/json", nil, &all); err != nil {
		fail(w, err)
		return
	}
	out := make([]any, 0)
	for _, image := range all {
		allowed := false
		for _, refs := range [][]string{image.RepoTags, image.RepoDigests} {
			for _, ref := range refs {
				if e.imageAllowed(ref, principal, strings.Split(r.URL.Query().Get("approval_ids"), ",")) == nil {
					allowed = true
					break
				}
			}
		}
		if allowed {
			out = append(out, image)
		}
	}
	respond(w, 200, map[string]any{"images": out, "node_image": e.options.NodeImage, "allowed_images": e.options.AllowedImages})
}
func (e *Executor) pullHTTP(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Image       string   `json:"image"`
		ApprovalIDs []string `json:"approval_ids"`
		Owner       string   `json:"owner,omitempty"`
	}
	if err := decode(r, &input); err != nil {
		fail(w, err)
		return
	}
	principal, err := e.actor(r, input.Owner)
	if err != nil {
		fail(w, err)
		return
	}
	if err = e.imageAllowed(input.Image, principal, input.ApprovalIDs); err != nil {
		fail(w, err)
		return
	}
	job, err := e.newJob(principal, "pull", "")
	if err != nil {
		fail(w, err)
		return
	}
	go func() {
		defer func() { <-e.jobs }()
		ctx, cancel := context.WithTimeout(e.ctx, 30*time.Minute)
		defer cancel()
		job.State = "running"
		err := e.saveJob(job)
		if err == nil {
			err = e.engine.pull(ctx, input.Image)
		}
		job.State = "complete"
		if err != nil {
			job.State = "failed"
			job.Error = err.Error()
		}
		job.FinishedAt = time.Now().UTC()
		_ = e.saveJob(job)
	}()
	respond(w, 202, job)
}
func workspacePath(value string) (string, error) {
	rel, err := relativePath(value)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return "/workspace", nil
	}
	return "/workspace/" + rel, nil
}
func requestURL(id, provider string) string {
	return "/executor/artifacts/download?id=" + url.QueryEscape(id) + "&provider=" + url.QueryEscape(provider)
}
func commandFailure(stdout, stderr string, code int) error {
	return fmt.Errorf("file helper exit %d: %s%s", code, stdout, stderr)
}
