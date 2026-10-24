// Command chat is an ordinary mesh consumer and terminal chat adapter.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/WhaleTrue/WhaleBot/pkg/mesh"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	message := strings.Join(os.Args[1:], " ")
	if message == "" {
		return fmt.Errorf("usage: chat <message>")
	}
	config, err := mesh.LoadConfigFromEnv()
	if err != nil {
		return err
	}
	node, err := mesh.New(config)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	ctx, deadline := context.WithTimeout(ctx, 90*time.Second)
	defer deadline()
	if err = node.Start(ctx); err != nil {
		return err
	}
	defer func() {
		shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = node.Close(shutdown)
	}()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var providers []mesh.Binding
	for {
		providers, err = node.Resolve(mesh.Requirement{ID: "chat-agent", Capability: "agent.run", Version: "^1.0.0", Policy: "priority"})
		if err == nil && len(providers) > 0 {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("agent.run unavailable: %w", ctx.Err())
		case <-ticker.C:
		}
	}
	session := os.Getenv("CHAT_SESSION")
	if session == "" {
		session = "terminal-" + fmt.Sprint(time.Now().UnixNano())
	}
	var response json.RawMessage
	if err = node.Call(ctx, providers[0], "POST", "/agent/run", map[string]any{"session_id": session, "message": message}, &response); err != nil {
		return err
	}
	var body struct {
		Reply string `json:"reply"`
	}
	if err = json.Unmarshal(response, &body); err != nil {
		return err
	}
	if body.Reply == "" {
		return fmt.Errorf("agent returned no reply: %s", response)
	}
	fmt.Println(body.Reply)
	return nil
}
