package api

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// fsDirs lists the immediate subdirectories of an absolute path on the driver
// host so the UI can browse to the enterprise images folder (e.g. a mounted USB
// / virtual media on the driver host). Directory names only — no file contents.
func fsDirs(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/"
	}
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		writeError(w, http.StatusBadRequest, "path must be absolute")
		return
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		writeError(w, http.StatusBadRequest, "cannot read %q: %v", path, err)
		return
	}
	dirs := []string{}
	for _, e := range entries {
		// os.ReadDir returns symlinks with the symlink type; resolve so mounted
		// media symlinked under /media etc. still shows as a directory.
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			dirs = append(dirs, e.Name())
			continue
		}
		if e.Type()&os.ModeSymlink != 0 && !strings.HasPrefix(e.Name(), ".") {
			if fi, err := os.Stat(filepath.Join(path, e.Name())); err == nil && fi.IsDir() {
				dirs = append(dirs, e.Name())
			}
		}
	}
	sort.Strings(dirs)
	parent := ""
	if path != "/" {
		parent = filepath.Dir(path)
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path, "parent": parent, "dirs": dirs})
}

// blockDevice is a mountable partition/filesystem surfaced to the UI so the
// operator can pick removable media (USB / virtual media) holding the images.
type blockDevice struct {
	Name       string `json:"name"`       // /dev/sdb1
	Size       string `json:"size"`       // human, e.g. "14.4G"
	FSType     string `json:"fstype"`     // e.g. ext4, vfat
	Label      string `json:"label"`      // filesystem label, if any
	Mountpoint string `json:"mountpoint"` // "" if not mounted
	Removable  bool   `json:"removable"`
}

// flexBool accepts both lsblk JSON shapes: true/false (newer util-linux) and
// "0"/"1" strings (EL8 util-linux 2.32).
type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	*b = flexBool(s == "1" || s == "true")
	return nil
}

type lsblkNode struct {
	Name       string      `json:"name"`
	Size       string      `json:"size"`
	FSType     string      `json:"fstype"`
	Label      string      `json:"label"`
	Mountpoint string      `json:"mountpoint"`
	Type       string      `json:"type"`
	Rm         flexBool    `json:"rm"`
	Children   []lsblkNode `json:"children"`
}

// fsDevices lists mountable block devices (those with a filesystem), flagging
// removable media, so the operator can mount a USB / virtual media.
func fsDevices(w http.ResponseWriter, r *http.Request) {
	out, err := exec.Command("lsblk", "-J", "-o", "NAME,SIZE,FSTYPE,LABEL,MOUNTPOINT,TYPE,RM").Output()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "lsblk: %v", err)
		return
	}
	var parsed struct {
		BlockDevices []lsblkNode `json:"blockdevices"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		writeError(w, http.StatusInternalServerError, "parse lsblk: %v", err)
		return
	}
	devs := []blockDevice{}
	var walk func(n lsblkNode, parentRm bool)
	walk = func(n lsblkNode, parentRm bool) {
		rm := bool(n.Rm) || parentRm
		// A node with a filesystem is mountable; skip whole-disk nodes that only
		// contain partitions.
		if n.FSType != "" && n.Type != "disk" {
			devs = append(devs, blockDevice{
				Name:       "/dev/" + n.Name,
				Size:       n.Size,
				FSType:     n.FSType,
				Label:      n.Label,
				Mountpoint: n.Mountpoint,
				Removable:  rm,
			})
		}
		for _, c := range n.Children {
			walk(c, rm)
		}
	}
	for _, n := range parsed.BlockDevices {
		walk(n, bool(n.Rm))
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": devs})
}

// fsMount mounts a block device at an auto-chosen mountpoint under /media and
// returns it.
func fsMount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Device string `json:"device"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	dev := strings.TrimSpace(body.Device)
	if !strings.HasPrefix(dev, "/dev/") {
		writeError(w, http.StatusBadRequest, "device must be a /dev/ path")
		return
	}
	if _, err := os.Stat(dev); err != nil {
		writeError(w, http.StatusBadRequest, "device %q not found", dev)
		return
	}
	mp := "/media/cube-" + filepath.Base(dev)
	if err := os.MkdirAll(mp, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, "mkdir %q: %v", mp, err)
		return
	}
	if out, err := exec.Command("mount", dev, mp).CombinedOutput(); err != nil {
		writeError(w, http.StatusBadGateway, "mount %s: %v: %s", dev, err, strings.TrimSpace(string(out)))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mountpoint": mp})
}
