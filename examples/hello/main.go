// Hello is an ordinary authenticated capability provider. It has no Docker access.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/WhaleMesh/WhaleMesh/pkg/mesh"
)

type greetingRequest struct {
	Name string `json:"name"`
}
type greetingResponse struct {
	Message string `json:"message"`
}

var greetingInput = json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","additionalProperties":false,"required":["name"],"properties":{"name":{"type":"string","minLength":1,"maxLength":128}}}`)
var greetingOutput = json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","additionalProperties":false,"required":["message"],"properties":{"message":{"type":"string"}}}`)

func greet(w http.ResponseWriter, r *http.Request) {
	var input greetingRequest
	if err := mesh.ReadJSON(w, r, &input); err != nil {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || !utf8.ValidString(input.Name) || utf8.RuneCountInString(input.Name) > 128 {
		mesh.WriteError(w, 400, "invalid_name", "name must contain 1 to 128 Unicode characters")
		return
	}
	mesh.WriteJSON(w, 200, greetingResponse{Message: "Hello, " + input.Name + "!"})
}
func register(node *mesh.Node) ([]mesh.Capability, error) {
	if node == nil {
		return nil, errors.New("node required")
	}
	semantics := mesh.Semantics{ReadOnly: true, Idempotent: true}
	capability := mesh.Capability{
		Schema: mesh.CapabilitySchema, ID: "hello.invoke", Interface: "hello.invoke", Version: "1.0.0", Endpoint: "/hello", Ready: true,
		InputSchema: greetingInput, OutputSchema: greetingOutput, Scopes: []string{"hello.invoke"}, Semantics: semantics,
		Operations: []mesh.Operation{{Name: "greet", Method: "POST", Path: "/hello", Scopes: []string{"hello.invoke"}, Semantics: semantics}},
		Tool:       &mesh.ToolExport{Name: "hello", Description: "Return a greeting for a name.", InputSchema: greetingInput, Method: "POST", Path: "/hello", Scopes: []string{"hello.invoke"}},
	}
	if err := mesh.ValidateCapability(capability); err != nil {
		return nil, err
	}
	node.Handle("POST", "/hello", []string{"hello.invoke"}, greet)
	return []mesh.Capability{capability}, nil
}
func run(ctx context.Context) error {
	config, err := mesh.LoadConfigFromEnv()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	node, err := mesh.New(config)
	if err != nil {
		return err
	}
	capabilities, err := register(node)
	if err != nil {
		return err
	}
	if err = node.UpdateCapabilities(capabilities); err != nil {
		return err
	}
	if err = node.Start(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return node.Close(shutdown)
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}
