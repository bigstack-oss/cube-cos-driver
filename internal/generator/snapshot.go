package generator

import (
	"archive/zip"
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/bigstack-oss/cube-cos-snapshot/internal/model"
)

//go:embed all:template
var templateFS embed.FS

// BuildNodeSnapshot returns the .snapshot zip for one node: the embedded
// template tree with the three policy files replaced and a fresh Comment.
func BuildNodeSnapshot(n model.NodeConfig, d model.ClusterDetail, ctl ControlInfo, now time.Time) ([]byte, error) {
	rendered := map[string]string{
		"Comment": fmt.Sprintf("Generated for %s on %s\n",
			d.ClusterInfo.Name, now.Format("2006-01-02 15:04:05")),
		"etc/policies/cubesys/cubesys1_0.yml": RenderCubesys(n, d.ClusterConfig, ctl),
		"etc/policies/network/network1_0.yml": RenderNetwork(n, d.ClusterConfig),
		"etc/policies/time/time1_0.yml":       RenderTime(d.ClusterConfig),
	}

	var names []string
	err := fs.WalkDir(templateFS, "template", func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() {
			names = append(names, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, path := range names {
		rel := path[len("template/"):]
		content, ok := rendered[rel]
		if !ok {
			b, err := templateFS.ReadFile(path)
			if err != nil {
				return nil, err
			}
			content = string(b)
		}
		w, err := zw.Create(rel)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(content)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// BuildClusterZip bundles clusterDetail.json and all node snapshots under a
// <shortID>/ prefix (legacy cluster zip layout).
func BuildClusterZip(shortID string, detailJSON []byte, snapshots map[string][]byte) ([]byte, error) {
	var hostnames []string
	for h := range snapshots {
		hostnames = append(hostnames, h)
	}
	sort.Strings(hostnames)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(shortID + "/clusterDetail.json")
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(detailJSON); err != nil {
		return nil, err
	}
	for _, h := range hostnames {
		w, err := zw.Create(shortID + "/" + h + ".snapshot")
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(snapshots[h]); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
