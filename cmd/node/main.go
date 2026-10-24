// Command node embeds ordinary capability providers selected by deployment.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/WhaleMesh/WhaleMesh/internal/agent"
	"github.com/WhaleMesh/WhaleMesh/internal/executor"
	"github.com/WhaleMesh/WhaleMesh/internal/llm"
	"github.com/WhaleMesh/WhaleMesh/internal/providers"
	"github.com/WhaleMesh/WhaleMesh/internal/relay"
	"github.com/WhaleMesh/WhaleMesh/internal/telegram"
	"github.com/WhaleMesh/WhaleMesh/internal/web"
	"github.com/WhaleMesh/WhaleMesh/pkg/mesh"
)

type register func(*mesh.Node, string) ([]mesh.Capability, error)

func main() {
	if executor.RunFileHelper(os.Args[1:]) {
		return
	}
	if err := run(); err != nil {
		slog.Error("node stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	config, err := mesh.LoadConfigFromEnv()
	if err != nil {
		return err
	}
	configured := make(map[string]bool, len(config.Manifest.Requires))
	for _, requirement := range config.Manifest.Requires {
		if requirement.ID != "" {
			configured[requirement.ID] = true
		}
	}
	addDefaults := func(defaults []mesh.Requirement) {
		for _, requirement := range defaults {
			if !configured[requirement.ID] {
				config.Manifest.Requires = append(config.Manifest.Requires, requirement)
				configured[requirement.ID] = true
			}
		}
	}
	for _, name := range strings.Split(os.Getenv("NODE_PACKAGES"), ",") {
		switch strings.TrimSpace(name) {
		case "agent":
			addDefaults(agent.Requirements())
		case "web":
			addDefaults(web.Requirements())
		case "telegram":
			addDefaults(telegram.Requirements())
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	node, err := mesh.New(config)
	if err != nil {
		return err
	}
	// These are deployment bundles, not protocol roles or service selectors.
	packages := map[string]register{"agent": agent.Register, "llm": llm.Register, "providers": providers.Register, "executor": func(n *mesh.Node, d string) ([]mesh.Capability, error) { return executor.RegisterContext(ctx, n, d) }, "web": web.Register, "telegram": telegram.Register, "relay": relay.Register}
	selected := map[string]bool{}
	var capabilities []mesh.Capability
	for _, name := range strings.Split(os.Getenv("NODE_PACKAGES"), ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if selected[name] {
			return fmt.Errorf("duplicate package %q", name)
		}
		selected[name] = true
		register, ok := packages[name]
		if !ok {
			return fmt.Errorf("unknown deployment package %q", name)
		}
		provided, registerErr := register(node, config.DataDir)
		if registerErr != nil {
			return fmt.Errorf("register %s: %w", name, registerErr)
		}
		capabilities = append(capabilities, provided...)
	}
	if err = node.UpdateCapabilities(capabilities); err != nil {
		return err
	}
	if err = node.Start(ctx); err != nil {
		return err
	}
	if selected["telegram"] {
		telegram.Start(ctx)
	}
	slog.Info("mesh node listening", "name", node.Manifest().Identity.Name, "instance", node.Manifest().Identity.InstanceID, "listen", config.Listen)
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return errors.Join(node.Close(shutdown), providers.Close(node))
}
