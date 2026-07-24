package orchestrator

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

var ErrNotFound = errors.New("deploy not found")

// Store persists deploy jobs so status survives a server restart.
type Store struct{ dir string }

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) path(clusterID string) string {
	return filepath.Join(s.dir, clusterID+".json")
}

func (s *Store) Save(d *Deploy) error {
	data, err := json.MarshalIndent(d, "", "    ")
	if err != nil {
		return err
	}
	tmp := s.path(d.ClusterID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(d.ClusterID))
}

func (s *Store) Load(clusterID string) (*Deploy, error) {
	data, err := os.ReadFile(s.path(clusterID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var d Deploy
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, err
	}
	return &d, nil
}
