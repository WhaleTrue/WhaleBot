// Command bootstrap provisions independent node keys and public trust grants.
// It runs once during deployment, not during discovery or capability calls.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

type target struct {
	Name              string   `json:"name"`
	Principal         string   `json:"principal"`
	Namespace         string   `json:"namespace"`
	Scopes            []string `json:"scopes"`
	Provides          []string `json:"provides"`
	BootstrapPassword bool     `json:"bootstrap_password,omitempty"`
	Approver          bool     `json:"approver,omitempty"`
}
type identity struct {
	Principal  string `json:"principal"`
	PrivateKey string `json:"private_key"`
}
type grant struct {
	PublicKey string   `json:"public_key"`
	Namespace string   `json:"namespace"`
	Scopes    []string `json:"scopes"`
	Provides  []string `json:"provides"`
}

var safeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

func main() {
	plan := flag.String("plan", "/config/identities.json", "identity plan JSON file")
	root := flag.String("root", "/identities", "private identity directories")
	flag.Parse()
	if err := provision(*plan, *root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func provision(plan, root string) error {
	f, err := os.Open(plan)
	if err != nil {
		return err
	}
	defer f.Close()
	var targets []target
	dec := json.NewDecoder(io.LimitReader(f, 1<<20))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&targets); err != nil {
		return err
	}
	var trailing any
	if err = dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("identity plan must contain one JSON document")
	}
	if len(targets) == 0 || len(targets) > 256 {
		return errors.New("identity plan requires 1..256 nodes")
	}
	grants := map[string]grant{}
	names := map[string]bool{}
	for _, t := range targets {
		if !safeName.MatchString(t.Name) || !safeName.MatchString(t.Namespace) || !safeName.MatchString(t.Principal) || names[t.Name] {
			return fmt.Errorf("invalid or duplicate identity %q", t.Name)
		}
		if _, ok := grants[t.Principal]; ok {
			return fmt.Errorf("duplicate principal %q", t.Principal)
		}
		names[t.Name] = true
		dir := filepath.Join(root, t.Name)
		if err = os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		path := filepath.Join(dir, "identity.json")
		var id identity
		data, readErr := os.ReadFile(path)
		if errors.Is(readErr, os.ErrNotExist) {
			_, key, keyErr := ed25519.GenerateKey(rand.Reader)
			if keyErr != nil {
				return keyErr
			}
			id = identity{t.Principal, base64.StdEncoding.EncodeToString(key)}
			data, err = json.MarshalIndent(id, "", "  ")
			if err != nil {
				return err
			}
			out, createErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if createErr != nil {
				return createErr
			}
			_, writeErr := out.Write(data)
			syncErr := out.Sync()
			closeErr := out.Close()
			if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
				return err
			}
		} else if readErr != nil {
			return readErr
		} else if err = json.Unmarshal(data, &id); err != nil {
			return err
		}
		key, decodeErr := base64.StdEncoding.DecodeString(id.PrivateKey)
		if decodeErr != nil || len(key) != ed25519.PrivateKeySize || id.Principal != t.Principal {
			return fmt.Errorf("stored identity mismatch: %s", t.Name)
		}
		if t.BootstrapPassword {
			passwordPath := filepath.Join(dir, "admin-password")
			if _, statErr := os.Stat(passwordPath); errors.Is(statErr, os.ErrNotExist) {
				secret := make([]byte, 24)
				if _, err = rand.Read(secret); err != nil {
					return err
				}
				password, openErr := os.OpenFile(passwordPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if openErr != nil {
					return openErr
				}
				_, writeErr := password.WriteString(base64.RawURLEncoding.EncodeToString(secret))
				if err = errors.Join(writeErr, password.Sync(), password.Close()); err != nil {
					return err
				}
			} else if statErr != nil {
				return statErr
			}
		}
		grants[t.Principal] = grant{PublicKey: base64.StdEncoding.EncodeToString(ed25519.PrivateKey(key).Public().(ed25519.PublicKey)), Namespace: t.Namespace, Scopes: t.Scopes, Provides: t.Provides}
	}
	data, err := json.MarshalIndent(grants, "", "  ")
	if err != nil {
		return err
	}
	approvalKeys := map[string]string{}
	for _, t := range targets {
		if t.Approver {
			approvalKeys[t.Principal] = grants[t.Principal].PublicKey
		}
	}
	approvals, err := json.MarshalIndent(approvalKeys, "", "  ")
	if err != nil {
		return err
	}
	for _, t := range targets {
		path := filepath.Join(root, t.Name, "trust.json")
		temp := path + ".tmp"
		if err = os.WriteFile(temp, data, 0600); err != nil {
			return err
		}
		if err = os.Rename(temp, path); err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(root, t.Name, "approval-keys.json"), approvals, 0600); err != nil {
			return err
		}
	}
	fmt.Printf("Provisioned %d independent identities; public trust distributed.\n", len(targets))
	return nil
}
