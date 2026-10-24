package executor

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func writeDurable(name string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(name), ".whalemesh-*")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temporary, name)
}
func (e *Executor) loadJobOutputs() {
	for id, j := range e.state.Jobs {
		if !identifier(id) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(e.dir, "jobs", id+".json"))
		if err != nil {
			continue
		}
		var stored Job
		if json.Unmarshal(data, &stored) == nil && stored.ID == id && stored.Principal == j.Principal {
			stored.Stdout = ""
			stored.Stderr = ""
			e.state.Jobs[id] = stored
		}
	}
}
