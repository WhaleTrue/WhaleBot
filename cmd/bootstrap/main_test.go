package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestProvisionKeepsKeysAndSeparatesPrivateIdentity(t *testing.T) {
	root := t.TempDir()
	plan := filepath.Join(root, "plan.json")
	targets := []target{{Name: "alpha", Principal: "alpha", Namespace: "default", Scopes: []string{"mesh.peer"}, BootstrapPassword: true}, {Name: "beta", Principal: "beta", Namespace: "default", Scopes: []string{"mesh.peer"}}}
	data, _ := json.Marshal(targets)
	if err := os.WriteFile(plan, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := provision(plan, root); err != nil {
		t.Fatal(err)
	}
	a, err := os.ReadFile(filepath.Join(root, "alpha", "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "beta", "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	var first, second identity
	if err = json.Unmarshal(a, &first); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &second); err != nil {
		t.Fatal(err)
	}
	if first.PrivateKey == second.PrivateKey {
		t.Fatal("nodes share private key")
	}
	password, err := os.ReadFile(filepath.Join(root, "alpha", "admin-password"))
	if err != nil {
		t.Fatal(err)
	}
	if len(password) != 32 {
		t.Fatalf("unexpected bootstrap password size %d", len(password))
	}
	if err = provision(plan, root); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(filepath.Join(root, "alpha", "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(a) {
		t.Fatal("reprovision replaced persisted identity")
	}
	again, err = os.ReadFile(filepath.Join(root, "alpha", "admin-password"))
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(password) {
		t.Fatal("reprovision replaced administrator password")
	}
	trust, err := os.ReadFile(filepath.Join(root, "beta", "trust.json"))
	if err != nil {
		t.Fatal(err)
	}
	var grants map[string]grant
	if err = json.Unmarshal(trust, &grants); err != nil {
		t.Fatal(err)
	}
	if len(grants) != 2 || grants["alpha"].PublicKey == "" || grants["beta"].PublicKey == "" {
		t.Fatal("public trust missing peer keys")
	}
	targets[0].Principal = "impostor"
	data, _ = json.Marshal(targets)
	if err = os.WriteFile(plan, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = provision(plan, root); err == nil {
		t.Fatal("persisted principal substitution accepted")
	}
}
