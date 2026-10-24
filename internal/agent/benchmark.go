package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/WhaleMesh/WhaleMesh/internal/llm"
)

type BenchmarkCase struct {
	Category  string  `json:"category"`
	Name      string  `json:"name"`
	Pass      bool    `json:"pass"`
	Score     float64 `json:"score"`
	Detail    string  `json:"detail,omitempty"`
	LatencyMS int64   `json:"latency_ms"`
}
type Benchmark struct {
	ID            string             `json:"id"`
	Owner         string             `json:"-"`
	Model         string             `json:"model"`
	Provider      string             `json:"provider"`
	ModelRevision string             `json:"model_revision"`
	UpstreamModel string             `json:"upstream_model"`
	StartedAt     time.Time          `json:"started_at"`
	FinishedAt    time.Time          `json:"finished_at"`
	Status        string             `json:"status"`
	CaseSet       string             `json:"case_set"`
	Cases         []BenchmarkCase    `json:"cases"`
	Usage         llm.Usage          `json:"usage"`
	Scores        map[string]float64 `json:"scores"`
	Error         string             `json:"error,omitempty"`
}

// persistedBenchmark keeps owner private in API responses while retaining ownership on disk.
type persistedBenchmark struct {
	Owner     string    `json:"owner"`
	Benchmark Benchmark `json:"run"`
}
type benchmarkRequest struct {
	Owner       string `json:"owner,omitempty"`
	Model       string `json:"model,omitempty"`
	Provider    string `json:"provider,omitempty"`
	IncludeE2E  bool   `json:"include_e2e"`
	SessionID   string `json:"session_id,omitempty"`
	ContainerID string `json:"container_id,omitempty"`
}
type benchmarkJob struct {
	input benchmarkRequest
	run   Benchmark
}

func (s *runtime) persistBenchmarks(runs []Benchmark) error {
	records := make([]persistedBenchmark, 0, len(runs))
	for _, run := range runs {
		records = append(records, persistedBenchmark{Owner: run.Owner, Benchmark: run})
	}
	return save(filepath.Join(s.dir, "benchmarks.json"), records)
}
func (s *runtime) handleBenchmark(w http.ResponseWriter, r *http.Request) {
	var input benchmarkRequest
	if err := strict(w, r, &input); err != nil {
		fail(w, 400, "invalid_json", err)
		return
	}
	owner, err := s.owner(r, input.Owner)
	if err != nil {
		fail(w, 403, "forbidden", err)
		return
	}
	input.Owner = owner
	if input.SessionID != "" && !validID.MatchString(input.SessionID) {
		fail(w, 400, "invalid_session", errors.New("invalid session_id"))
		return
	}
	if input.IncludeE2E && (input.SessionID == "" || input.ContainerID == "") {
		fail(w, 400, "missing_e2e_target", errors.New("e2e requires session_id and an approved container_id"))
		return
	}
	run := Benchmark{ID: id(), Owner: owner, Model: input.Model, StartedAt: time.Now().UTC(), Status: "running", CaseSet: "mesh-v1", Scores: map[string]float64{}, Cases: []BenchmarkCase{}}
	s.mu.Lock()
	for _, old := range s.benchmarks {
		if old.Status == "running" {
			s.mu.Unlock()
			fail(w, 409, "benchmark_busy", errors.New("benchmark already running"))
			return
		}
	}
	next := append(append([]Benchmark{}, s.benchmarks...), run)
	if len(next) > 100 {
		next = next[len(next)-100:]
	}
	if err = s.persistBenchmarks(next); err != nil {
		s.mu.Unlock()
		fail(w, 500, "persistence_failed", err)
		return
	}
	s.benchmarks = next
	s.mu.Unlock()
	s.benchmarkJobs <- benchmarkJob{input: input, run: run}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	jsonReply(w, run)
}

func (s *runtime) benchmarkWorker(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			next := append([]Benchmark(nil), s.benchmarks...)
			for i := range next {
				if next[i].Status == "running" {
					next[i].Status = "failed"
					next[i].Error = "node shutdown"
					next[i].FinishedAt = time.Now().UTC()
				}
			}
			err := s.persistBenchmarks(next)
			if err == nil {
				s.benchmarks = next
			}
			s.mu.Unlock()
			if err != nil {
				return err
			}
			return ctx.Err()
		case job := <-s.benchmarkJobs:
			run := job.run
			run.Scores = map[string]float64{}
			run.Cases = []BenchmarkCase{}
			budget, cancel := context.WithTimeout(ctx, 15*time.Minute)
			s.executeBenchmark(budget, job.input, &run)
			cancel()
			run.FinishedAt = time.Now().UTC()
			run.Status = "completed"
			if run.Error != "" {
				run.Status = "failed"
			}
			s.mu.Lock()
			next := append([]Benchmark(nil), s.benchmarks...)
			for i := range next {
				if next[i].ID == run.ID {
					next[i] = run
				}
			}
			err := s.persistBenchmarks(next)
			if err == nil {
				s.benchmarks = next
			}
			s.mu.Unlock()
			if err != nil {
				return err
			}
		}
	}
}
func (s *runtime) listBenchmarks(w http.ResponseWriter, r *http.Request) {
	owner, err := s.owner(r, r.URL.Query().Get("owner"))
	if err != nil {
		fail(w, 403, "forbidden", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items := []Benchmark{}
	for _, run := range s.benchmarks {
		if run.Owner == owner {
			items = append(items, run)
		}
	}
	jsonReply(w, map[string]any{"runs": items})
}
func (s *runtime) deleteBenchmarks(w http.ResponseWriter, r *http.Request) {
	owner, err := s.owner(r, r.URL.Query().Get("owner"))
	if err != nil {
		fail(w, 403, "forbidden", err)
		return
	}
	runID := strings.TrimPrefix(r.URL.Path, "/benchmark/runs")
	runID = strings.TrimPrefix(runID, "/")
	s.mu.Lock()
	defer s.mu.Unlock()
	next := []Benchmark{}
	for _, run := range s.benchmarks {
		if run.Owner == owner && (runID == "" || run.ID == runID) {
			if run.Status == "running" {
				fail(w, 409, "benchmark_busy", errors.New("cannot delete running benchmark"))
				return
			}
			continue
		}
		next = append(next, run)
	}
	if err = s.persistBenchmarks(next); err != nil {
		fail(w, 500, "persistence_failed", err)
		return
	}
	s.benchmarks = next
	jsonReply(w, map[string]bool{"deleted": true})
}
func scoreBenchmark(run *Benchmark) {
	totals := map[string]float64{}
	counts := map[string]int{}
	for _, c := range run.Cases {
		totals[c.Category] += c.Score
		counts[c.Category]++
	}
	for category, sum := range totals {
		run.Scores[category] = 100 * sum / float64(counts[category])
	}
	run.Scores["total"] = .25*run.Scores["plan_gate"] + .35*run.Scores["tool_call"] + .40*run.Scores["react"]
}
func (s *runtime) executeBenchmark(ctx context.Context, input benchmarkRequest, run *Benchmark) {
	defer scoreBenchmark(run)
	model, err := s.modelFor(ctx, Request{Model: input.Model, Provider: input.Provider})
	if err != nil {
		run.Error = err.Error()
		return
	}
	run.Provider = model.Node.Identity.Principal
	run.Model = model.Model
	run.ModelRevision = model.Revision
	run.UpstreamModel = model.UpstreamModel
	invoke := func(messages []llm.Message, tools []llm.Tool) (llm.Response, error) {
		if model.Capability.Attributes["supports_tools"] == "false" {
			tools = nil
		}
		out, err := s.invoke(ctx, model, llm.Request{Messages: messages, Tools: tools, MaxTokens: 2048}, nil)
		if err == nil {
			addUsage(&run.Usage, out.Usage)
		}
		return out, err
	}
	for _, c := range planCases {
		if ctx.Err() != nil {
			run.Error = ctx.Err().Error()
			return
		}
		start := time.Now()
		out, err := invoke([]llm.Message{{Role: "system", Content: `This is a simulated benchmark, not an authorization decision. Return only JSON {"plan_required":boolean,"approval_required":boolean}. Destructive or mutating work needs approval. Multi-step build/deploy work needs a plan. Simple greetings and read-only inventory do not.`}, {Role: "user", Content: c.prompt}}, nil)
		result := BenchmarkCase{Category: "plan_gate", Name: c.name, LatencyMS: time.Since(start).Milliseconds()}
		var decision struct {
			Plan     bool `json:"plan_required"`
			Approval bool `json:"approval_required"`
		}
		if err != nil {
			result.Detail = err.Error()
			run.Cases = append(run.Cases, result)
			run.Error = err.Error()
			return
		} else if json.Unmarshal([]byte(out.Message.Content), &decision) != nil {
			result.Detail = "not valid decision JSON"
		} else {
			if decision.Plan == c.plan {
				result.Score += .5
			}
			if decision.Approval == c.approval {
				result.Score += .5
			}
			result.Pass = result.Score == 1
		}
		run.Cases = append(run.Cases, result)
	}
	for _, scenario := range simulatedScenarios() {
		if ctx.Err() != nil {
			run.Error = ctx.Err().Error()
			return
		}
		start := time.Now()
		messages := []llm.Message{{Role: "system", Content: "This is a simulated benchmark. Tools have canned observations and no real side effects. Use tool calls, inspect before mutations, recover from errors, report factual observations. An external trusted policy has already approved this simulated scenario."}, {Role: "user", Content: scenario.prompt}}
		calls := []simulationCall{}
		final := ""
		detail := ""
		for step := range 8 {
			tools := simulationTools()
			if step == 7 {
				tools = nil
			}
			out, err := invoke(messages, tools)
			if err != nil {
				run.Error = err.Error()
				return
			}
			messages = append(messages, out.Message)
			if len(out.Message.ToolCalls) == 0 {
				final = out.Message.Content
				break
			}
			for _, call := range out.Message.ToolCalls {
				var args simulationCall
				if call.Name != "container" || json.Unmarshal(call.Arguments, &args) != nil {
					detail = "invalid tool name or arguments"
					continue
				}
				calls = append(calls, args)
				observation := scenario.respond(args, len(calls))
				messages = append(messages, llm.Message{Role: "tool", ToolCallID: call.ID, Content: observation})
			}
		}
		format := 0.0
		if len(calls) > 0 && detail == "" {
			format = 1
		}
		run.Cases = append(run.Cases, BenchmarkCase{Category: "tool_call", Name: scenario.name + " arguments", Pass: format == 1, Score: format, Detail: detail, LatencyMS: time.Since(start).Milliseconds()})
		score := scenario.score(calls, final)
		if detail != "" {
			score = 0
		}
		run.Cases = append(run.Cases, BenchmarkCase{Category: scenario.category, Name: scenario.name, Pass: score == 1, Score: score, Detail: detail, LatencyMS: time.Since(start).Milliseconds()})
	}
	if input.IncludeE2E {
		s.benchmarkE2E(ctx, input, run)
	}
}
