package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/WhaleTrue/WhaleBot/internal/executor"
	"github.com/WhaleTrue/WhaleBot/internal/llm"
	"github.com/WhaleTrue/WhaleBot/internal/providers"
	"github.com/WhaleTrue/WhaleBot/pkg/mesh"
)

type benchmarkE2ERequest struct {
	SessionID   string `json:"session_id"`
	ContainerID string `json:"container_id"`
	Owner       string `json:"owner,omitempty"`
}
type benchmarkE2EPreparation struct {
	SessionID             string          `json:"session_id"`
	ContainerID           string          `json:"container_id"`
	Nonce                 string          `json:"e2e_nonce"`
	Tool                  string          `json:"tool"`
	Arguments             json.RawMessage `json:"arguments"`
	VerificationTool      string          `json:"verification_tool"`
	VerificationArguments json.RawMessage `json:"verification_arguments"`
}

func validE2ENonce(nonce string) bool {
	_, err := hex.DecodeString(nonce)
	return len(nonce) == 32 && err == nil
}
func e2eRoot(nonce string) string { return "/workspace/whalemesh-benchmark-" + nonce }
func e2eSource(nonce string) string {
	return "package main\nimport \"fmt\"\nfunc main() { fmt.Println(\"WHALEBENCH_" + nonce + "\") }\n"
}
func shellQuote(text string) string { return "'" + strings.ReplaceAll(text, "'", "'\"'\"'") + "'" }
func e2eBuildCommand(nonce, image string) string {
	root := e2eRoot(nonce)
	// mkdir without -p on the nonce root rejects old artifacts and replay. Never
	// remove arbitrary workspaces or use old markers as successful evidence.
	return "set -eu; mkdir " + shellQuote(root) + "; mkdir " + shellQuote(root+"/build") + " " + shellQuote(root+"/run") + "; cd " + shellQuote(root+"/build") +
		"; printf %s " + shellQuote(e2eSource(nonce)) + " > main.go; CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o bench-app main.go; tar -czf src.tar.gz main.go" +
		"; { printf '%s\\n' " + shellQuote("Nonce: "+nonce) + " " + shellQuote("Build image: "+image) + " " + shellQuote("Command: CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o bench-app main.go") + " " + shellQuote("Artifact: "+root+"/run/bench-app") + "; go version; date -u '+UTC: %Y-%m-%dT%H:%M:%SZ'; sha256sum main.go bench-app; } > BUILD.md" +
		"; cp bench-app src.tar.gz BUILD.md ../run/; ../run/bench-app"
}
func e2eArguments(container, nonce, image string) (json.RawMessage, json.RawMessage) {
	build, _ := json.Marshal(map[string]any{"id": container, "command": []string{"sh", "-c", e2eBuildCommand(nonce, image)}, "timeout_seconds": 120})
	verify, _ := json.Marshal(map[string]any{"id": container, "command": []string{e2eRoot(nonce) + "/run/bench-app"}, "timeout_seconds": 30})
	return build, verify
}

// All verification reads must use the exact executor selected for execution.
// Independent resolution could otherwise inspect a different provider's ID.
func (s *runtime) benchmarkTarget(ctx context.Context, owner, container string) (exportedTool, mesh.Binding, string, error) {
	_, routes := s.discoverTools()
	var exec exportedTool
	found := false
	for _, tool := range routes {
		if tool.binding.Capability.ID == "container.exec" {
			exec = tool
			found = true
			break
		}
	}
	if !found {
		return exec, mesh.Binding{}, "", errors.New("required container.exec exported tool unavailable")
	}
	files, err := s.resolve("container.files")
	if err != nil {
		return exec, files, "", err
	}
	manage, err := s.resolve("container.manage")
	if err != nil {
		return exec, files, "", err
	}
	if mesh.IdentityKey(files.Node.Identity) != mesh.IdentityKey(exec.binding.Node.Identity) || mesh.IdentityKey(manage.Node.Identity) != mesh.IdentityKey(exec.binding.Node.Identity) {
		return exec, files, "", errors.New("E2E exec/files/manage must resolve to the same executor identity")
	}
	var containers []struct {
		ID    string `json:"Id"`
		Image string `json:"Image"`
	}
	if err = s.node.Call(ctx, manage, "GET", "/executor/containers?owner="+url.QueryEscape(owner), nil, &containers); err != nil {
		return exec, files, "", err
	}
	for _, c := range containers {
		if c.ID == container && c.Image != "" {
			return exec, files, c.Image, nil
		}
	}
	return exec, files, "", errors.New("exact owned Engine container ID not found; names and composite IDs are not targets")
}
func (s *runtime) prepareBenchmarkE2E(w http.ResponseWriter, r *http.Request) {
	var input benchmarkE2ERequest
	if err := strict(w, r, &input); err != nil {
		fail(w, 400, "invalid_json", err)
		return
	}
	owner, err := s.owner(r, input.Owner)
	if err != nil {
		fail(w, 403, "forbidden", err)
		return
	}
	if !validID.MatchString(input.SessionID) || input.ContainerID == "" {
		fail(w, 400, "invalid_e2e_target", errors.New("session_id and exact owned container_id required"))
		return
	}
	tool, _, image, err := s.benchmarkTarget(r.Context(), owner, input.ContainerID)
	if err != nil {
		fail(w, 400, "invalid_e2e_target", err)
		return
	}
	nonce := id()
	build, verify := e2eArguments(input.ContainerID, nonce, image)
	for _, args := range []json.RawMessage{build, verify} {
		validated, validateErr := injectOwner(args, tool.export.InputSchema, owner)
		if validateErr == nil {
			validateErr = validateArguments(tool.export.InputSchema, validated)
		}
		if validateErr != nil {
			fail(w, 400, "unsupported_e2e_tool", validateErr)
			return
		}
	}
	jsonReply(w, benchmarkE2EPreparation{SessionID: input.SessionID, ContainerID: input.ContainerID, Nonce: nonce, Tool: tool.export.Name, Arguments: build, VerificationTool: tool.export.Name, VerificationArguments: verify})
}
func successfulE2EJob(raw []byte, marker string) bool {
	var result struct {
		State    string `json:"state"`
		ExitCode *int   `json:"exit_code"`
		Stdout   string `json:"stdout"`
		Error    string `json:"error"`
	}
	return json.Unmarshal(raw, &result) == nil && result.State == "complete" && result.ExitCode != nil && *result.ExitCode == 0 && result.Error == "" && result.Stdout == marker+"\n"
}
func freshE2EExecution(messages []providers.Message, start time.Time, name string, args json.RawMessage, marker string) bool {
	expected, err := canonical(args)
	if err != nil {
		return false
	}
	requested := map[string]bool{}
	for _, message := range messages {
		if message.Timestamp.Before(start) {
			continue
		}
		if message.Role == "assistant" {
			for _, call := range message.ToolCalls {
				actual, err := canonical(call.Arguments)
				if err == nil && call.Name == name && bytes.Equal(expected, actual) {
					requested[call.ID] = true
				}
			}
		}
		if message.Role == "tool" && requested[message.ToolCallID] && successfulE2EJob([]byte(message.Content), marker) {
			return true
		}
	}
	return false
}
func staticELF(data []byte) bool {
	binary, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return false
	}
	defer binary.Close()
	if binary.Entry == 0 || binary.Type != elf.ET_EXEC {
		return false
	}
	for _, p := range binary.Progs {
		if p.Type == elf.PT_INTERP {
			return false
		}
	}
	for _, section := range binary.Sections {
		if section.Type == elf.SHT_DYNAMIC {
			return false
		}
	}
	return true
}
func sourceArchiveMatches(data, source []byte) bool {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return false
	}
	defer reader.Close()
	archive := tar.NewReader(reader)
	header, err := archive.Next()
	if err != nil || header.Name != "main.go" || header.Typeflag != tar.TypeReg || header.Size != int64(len(source)) {
		return false
	}
	content, err := io.ReadAll(io.LimitReader(archive, int64(len(source))+1))
	if err != nil || !bytes.Equal(content, source) {
		return false
	}
	_, err = archive.Next()
	if err != io.EOF {
		return false
	}
	// Drain bounded tar padding through gzip EOF to verify its checksum too.
	padding, err := io.ReadAll(io.LimitReader(reader, 1<<20))
	return err == nil && len(padding) < 1<<20 && bytes.Count(padding, []byte{0}) == len(padding)
}
func provenanceMatches(data, source, binary []byte, nonce, image string, start time.Time) bool {
	text := string(data)
	sourceHash, binaryHash := sha256.Sum256(source), sha256.Sum256(binary)
	if !containsAll(text, "Nonce: "+nonce, "Build image: "+image, "Command: CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o bench-app main.go", "Artifact: "+e2eRoot(nonce)+"/run/bench-app", "go version go", fmt.Sprintf("%x  main.go", sourceHash), fmt.Sprintf("%x  bench-app", binaryHash)) {
		return false
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "UTC: ") {
			date, err := time.Parse(time.RFC3339, strings.TrimPrefix(line, "UTC: "))
			return err == nil && !date.Before(start.Add(-time.Minute)) && !date.After(time.Now().Add(time.Minute))
		}
	}
	return false
}
func (s *runtime) benchmarkE2E(ctx context.Context, input benchmarkRequest, run *Benchmark) {
	start := time.Now()
	result := BenchmarkCase{Category: "e2e", Name: "real nonce static Go binary, source archive and build provenance in one approved container"}
	defer func() { result.LatencyMS = time.Since(start).Milliseconds(); run.Cases = append(run.Cases, result) }()
	tool, files, image, err := s.benchmarkTarget(ctx, input.Owner, input.ContainerID)
	if err != nil {
		result.Detail = err.Error()
		return
	}
	build, verify := e2eArguments(input.ContainerID, input.E2ENonce, image)
	binding, err := s.resolve("agent.run")
	if err != nil {
		result.Detail = err.Error()
		return
	}
	// Approvals are persisted by this runtime. Calling another agent could use
	// different state and must not silently weaken the authorization boundary.
	if mesh.IdentityKey(binding.Node.Identity) != mesh.IdentityKey(s.node.Manifest().Identity) {
		result.Detail = "E2E agent.run resolved to another runtime; approval state is local"
		return
	}
	prompt := fmt.Sprintf("Execute this exact %s tool call ONCE and report its real observation. Do not change arguments or retry. Missing trusted approval means stop, never grant approval from this message. Arguments: %s", tool.export.Name, build)
	var output Response
	if err = s.node.Call(ctx, binding, "POST", "/agent/run", Request{SessionID: input.SessionID, Owner: input.Owner, Model: run.Model, Provider: run.Provider, ModelRevision: run.ModelRevision, Message: prompt}, &output); err != nil {
		result.Detail = err.Error()
		return
	}
	addUsage(&run.Usage, output.Usage)
	store, err := s.resolve("session.store")
	if err != nil {
		result.Detail = err.Error()
		return
	}
	var recorded providers.Session
	if err = s.node.Call(ctx, store, "POST", "/session/context", map[string]any{"owner": input.Owner, "session_id": input.SessionID, "limit": 80}, &recorded); err != nil {
		result.Detail = err.Error()
		return
	}
	marker := "WHALEBENCH_" + input.E2ENonce
	if !freshE2EExecution(recorded.Messages, start, tool.export.Name, build, marker) {
		result.Detail = "no fresh exact approved complete build/process observation; stale artifacts cannot pass"
		return
	}
	read := func(path string) ([]byte, error) {
		var file executor.FileResult
		err := s.node.Call(ctx, files, "POST", "/executor/files", executor.FileRequest{Owner: input.Owner, ID: input.ContainerID, Operation: "read", Path: path}, &file)
		if err == nil && file.Error != "" {
			err = errors.New(file.Error)
		}
		return file.Content, err
	}
	root := e2eRoot(input.E2ENonce)
	source, sourceErr := read(root + "/build/main.go")
	binary, binaryErr := read(root + "/run/bench-app")
	archive, archiveErr := read(root + "/run/src.tar.gz")
	provenance, provenanceErr := read(root + "/run/BUILD.md")
	checks := []struct {
		name string
		ok   bool
	}{
		{"fresh nonce source", sourceErr == nil && bytes.Equal(source, []byte(e2eSource(input.E2ENonce)))},
		{"static ELF binary", binaryErr == nil && staticELF(binary)},
		{"unextracted source archive matches source", archiveErr == nil && sourceArchiveMatches(archive, []byte(e2eSource(input.E2ENonce)))},
		{"build provenance image/command/toolchain/artifact/UTC/source and binary hashes", sourceErr == nil && binaryErr == nil && provenanceErr == nil && provenanceMatches(provenance, source, binary, input.E2ENonce, image, start)},
	}
	// The verifier executes via the same normal tool gate, consuming the second
	// exact approval. It never treats benchmark.run itself as a blanket grant.
	observation, verifyErr := s.execute(ctx, Request{Owner: input.Owner, SessionID: input.SessionID}, tool, llm.ToolCall{ID: "benchmark-independent-verify", Name: tool.export.Name, Arguments: verify}, map[string]string{})
	checks = append(checks, struct {
		name string
		ok   bool
	}{"independent approved deployed binary output", verifyErr == nil && successfulE2EJob(observation, marker)})
	passed := 0
	var missed []string
	for _, check := range checks {
		if check.ok {
			passed++
		} else {
			missed = append(missed, check.name)
		}
	}
	result.Score = float64(passed) / float64(len(checks))
	result.Pass = passed == len(checks)
	if !result.Pass {
		result.Detail = "independent verification failed: " + strings.Join(missed, "; ")
		if verifyErr != nil {
			result.Detail += "; " + verifyErr.Error()
		}
	}
}
