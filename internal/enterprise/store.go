package enterprise

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Store persists enterprise install jobs so status survives a server restart.
type Store struct{ dir string }

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) path(clusterID, module string) string {
	return filepath.Join(s.dir, clusterID+"-"+module+".json")
}

func (s *Store) Save(in *Install) error {
	data, err := json.MarshalIndent(in, "", "    ")
	if err != nil {
		return err
	}
	p := s.path(in.ClusterID, in.Module)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Delete removes a persisted install record and its password sidecar (used to
// cascade-drop a dependent module's record when its parent is uninstalled).
// Absent files are ignored.
func (s *Store) Delete(clusterID, module string) error {
	base := filepath.Join(s.dir, clusterID+"-"+module)
	err := os.Remove(base + ".json")
	if os.IsNotExist(err) {
		err = nil
	}
	if pwErr := os.Remove(base + ".pw"); pwErr != nil && !os.IsNotExist(pwErr) && err == nil {
		err = pwErr
	}
	return err
}

func (s *Store) Load(clusterID, module string) (*Install, bool) {
	data, err := os.ReadFile(s.path(clusterID, module))
	if err != nil {
		return nil, false
	}
	var in Install
	if err := json.Unmarshal(data, &in); err != nil {
		return nil, false
	}
	return &in, true
}

// List returns every persisted install, for the dashboard and boot-time
// rehydrate. Unreadable/malformed files are skipped.
func (s *Store) List() []*Install {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var out []*Install
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, name))
		if err != nil {
			continue
		}
		var in Install
		if json.Unmarshal(data, &in) == nil && in.ClusterID != "" {
			out = append(out, &in)
		}
	}
	return out
}
