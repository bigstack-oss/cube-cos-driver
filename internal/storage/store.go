// Package storage persists generated clusters on disk:
// <DataDir>/<shortID>/{clusterDetail.json, <host>.snapshot} + <DataDir>/<shortID>.zip.
// With ExportDir set, node snapshots are also mirrored flat as
// <ExportDir>/<host>.snapshot so nodes can `snapshot pull url <host>`.
package storage

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bigstack-oss/cube-cos-snapshot/internal/generator"
	"github.com/bigstack-oss/cube-cos-snapshot/internal/model"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	DataDir   string
	ExportDir string
}

type Digest struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Nodes []string `json:"nodes"`
}

func (s *Store) clusterDir(shortID string) string { return filepath.Join(s.DataDir, shortID) }
func (s *Store) zipPath(shortID string) string    { return filepath.Join(s.DataDir, shortID+".zip") }

func (s *Store) detailAt(dir string) (model.ClusterDetail, error) {
	var d model.ClusterDetail
	raw, err := os.ReadFile(filepath.Join(dir, "clusterDetail.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return d, ErrNotFound
		}
		return d, err
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return d, err
	}
	return d, nil
}

// Save validates, generates everything in memory, writes to a temp dir, then
// atomically swaps it in. A failed generation never clobbers existing state.
func (s *Store) Save(d model.ClusterDetail) error {
	if err := d.Validate(); err != nil {
		return err
	}
	shortID := d.ShortID()
	now := time.Now()
	ctl := generator.GetControlInfo(d.NodeData)

	detailJSON, err := json.MarshalIndent(d, "", "    ")
	if err != nil {
		return err
	}
	snapshots := map[string][]byte{}
	for _, n := range d.NodeData {
		b, err := generator.BuildNodeSnapshot(n, d, ctl, now)
		if err != nil {
			return fmt.Errorf("node %s: %w", n.Hostname, err)
		}
		snapshots[n.Hostname] = b
	}
	clusterZip, err := generator.BuildClusterZip(shortID, detailJSON, snapshots)
	if err != nil {
		return err
	}

	// Hostnames of a previous version, for export cleanup.
	var staleHosts []string
	if prev, err := s.detailAt(s.clusterDir(shortID)); err == nil {
		for _, n := range prev.NodeData {
			staleHosts = append(staleHosts, n.Hostname)
		}
	}

	suffix := make([]byte, 4)
	rand.Read(suffix)
	tmp := filepath.Join(s.DataDir, ".tmp-"+shortID+"-"+hex.EncodeToString(suffix))
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	if err := os.WriteFile(filepath.Join(tmp, "clusterDetail.json"), detailJSON, 0o644); err != nil {
		return err
	}
	for h, b := range snapshots {
		if err := os.WriteFile(filepath.Join(tmp, h+".snapshot"), b, 0o644); err != nil {
			return err
		}
	}

	// Swap in: zip first (plain file write), then the directory.
	if err := os.WriteFile(s.zipPath(shortID), clusterZip, 0o644); err != nil {
		return err
	}
	old := s.clusterDir(shortID)
	if err := os.RemoveAll(old); err != nil {
		return err
	}
	if err := os.Rename(tmp, old); err != nil {
		return err
	}

	if s.ExportDir != "" {
		for _, h := range staleHosts {
			if _, ok := snapshots[h]; !ok {
				os.Remove(filepath.Join(s.ExportDir, h+".snapshot"))
			}
		}
		for h, b := range snapshots {
			if err := os.WriteFile(filepath.Join(s.ExportDir, h+".snapshot"), b, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) List() ([]Digest, error) {
	entries, err := os.ReadDir(s.DataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Digest{}, nil
		}
		return nil, err
	}
	digests := []Digest{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		d, err := s.detailAt(filepath.Join(s.DataDir, e.Name()))
		if err != nil {
			continue // not a cluster dir
		}
		var nodes []string
		for _, n := range d.NodeData {
			nodes = append(nodes, n.Hostname)
		}
		sort.Strings(nodes)
		digests = append(digests, Digest{ID: e.Name(), Name: d.ClusterInfo.Name, Nodes: nodes})
	}
	sort.Slice(digests, func(i, j int) bool { return digests[i].Name < digests[j].Name })
	return digests, nil
}

func (s *Store) Detail(shortID string) (model.ClusterDetail, error) {
	return s.detailAt(s.clusterDir(shortID))
}

func (s *Store) Delete(shortID string) error {
	d, err := s.Detail(shortID)
	if err != nil {
		return err
	}
	if s.ExportDir != "" {
		for _, n := range d.NodeData {
			os.Remove(filepath.Join(s.ExportDir, n.Hostname+".snapshot"))
		}
	}
	os.Remove(s.zipPath(shortID))
	return os.RemoveAll(s.clusterDir(shortID))
}

func (s *Store) ClusterZipPath(shortID string) (path, downloadName string, err error) {
	d, err := s.Detail(shortID)
	if err != nil {
		return "", "", err
	}
	p := s.zipPath(shortID)
	if _, err := os.Stat(p); err != nil {
		return "", "", ErrNotFound
	}
	return p, d.ClusterInfo.Name + ".zip", nil
}

func (s *Store) SnapshotPath(shortID, hostname string) (string, error) {
	p := filepath.Join(s.clusterDir(shortID), hostname+".snapshot")
	if _, err := os.Stat(p); err != nil {
		return "", ErrNotFound
	}
	return p, nil
}
