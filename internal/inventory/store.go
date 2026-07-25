package inventory

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

	"github.com/bigstack-oss/cube-cos-snapshot/internal/secret"
)

var ErrNotFound = errors.New("machine not found")

// Store persists machine records under <dir> with encrypted passwords.
type Store struct {
	dir string
	box *secret.Box
}

func NewStore(dir string, box *secret.Box) (*Store, error) {
	if box == nil {
		return nil, errors.New("inventory: secret box is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir, box: box}, nil
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Store) path(id string) string { return filepath.Join(s.dir, id+".json") }

func (s *Store) readRecord(id string) (*record, error) {
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var r record
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) writeRecord(r *record) error {
	data, err := json.MarshalIndent(r, "", "    ")
	if err != nil {
		return err
	}
	tmp := s.path(r.ID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(r.ID))
}

// Input is the mutable data for create/update.
type Input struct {
	Label    string
	Address  string
	Username string
	// Password: nil = keep existing (update); non-nil = set (empty clears).
	Password *string
}

func validate(in Input) error {
	if strings.TrimSpace(in.Label) == "" {
		return errors.New("label is required")
	}
	if strings.TrimSpace(in.Address) == "" {
		return errors.New("bmc address is required")
	}
	return nil
}

func (s *Store) Create(in Input) (Machine, error) {
	if err := validate(in); err != nil {
		return Machine{}, err
	}
	r := &record{
		ID:         newID(),
		Label:      in.Label,
		BMC:        BMC{Address: in.Address, Username: in.Username},
		FetchState: FetchIdle,
	}
	if in.Password != nil && *in.Password != "" {
		enc, err := s.box.Encrypt(*in.Password)
		if err != nil {
			return Machine{}, err
		}
		r.PasswordEnc = enc
	}
	if err := s.writeRecord(r); err != nil {
		return Machine{}, err
	}
	return r.toMachine(), nil
}

func (s *Store) Update(id string, in Input) (Machine, error) {
	if err := validate(in); err != nil {
		return Machine{}, err
	}
	r, err := s.readRecord(id)
	if err != nil {
		return Machine{}, err
	}
	r.Label = in.Label
	r.BMC = BMC{Address: in.Address, Username: in.Username}
	if in.Password != nil {
		if *in.Password == "" {
			r.PasswordEnc = ""
		} else {
			enc, err := s.box.Encrypt(*in.Password)
			if err != nil {
				return Machine{}, err
			}
			r.PasswordEnc = enc
		}
	}
	if err := s.writeRecord(r); err != nil {
		return Machine{}, err
	}
	return r.toMachine(), nil
}

func (s *Store) Delete(id string) error {
	if _, err := s.readRecord(id); err != nil {
		return err
	}
	return os.Remove(s.path(id))
}

func (s *Store) Get(id string) (Machine, error) {
	r, err := s.readRecord(id)
	if err != nil {
		return Machine{}, err
	}
	return r.toMachine(), nil
}

func (s *Store) List() ([]Machine, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	machines := []Machine{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		r, err := s.readRecord(id)
		if err != nil {
			continue
		}
		machines = append(machines, r.toMachine())
	}
	sort.Slice(machines, func(i, j int) bool { return machines[i].Label < machines[j].Label })
	return machines, nil
}

// Credentials returns the decrypted BMC login for discovery/control.
func (s *Store) Credentials(id string) (address, username, password string, err error) {
	r, err := s.readRecord(id)
	if err != nil {
		return "", "", "", err
	}
	if r.PasswordEnc != "" {
		password, err = s.box.Decrypt(r.PasswordEnc)
		if err != nil {
			return "", "", "", fmt.Errorf("decrypt password: %w", err)
		}
	}
	return r.BMC.Address, r.BMC.Username, password, nil
}

// SetFetchState updates the transient fetch status (before/after discovery).
func (s *Store) SetFetchState(id string, state FetchState, fetchErr string) error {
	r, err := s.readRecord(id)
	if err != nil {
		return err
	}
	r.FetchState = state
	r.FetchError = fetchErr
	return s.writeRecord(r)
}

// Assign binds machine id to (clusterID, hostname), enforcing 1:1: any other
// machine already assigned to the same node is unassigned first, and this
// machine's own previous assignment is overwritten.
func (s *Store) Assign(id, clusterID, hostname, osDisk string) (Machine, error) {
	target, err := s.readRecord(id)
	if err != nil {
		return Machine{}, err
	}
	// Clear any other machine holding this node.
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return Machine{}, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		otherID := strings.TrimSuffix(e.Name(), ".json")
		if otherID == id {
			continue
		}
		other, err := s.readRecord(otherID)
		if err != nil {
			continue
		}
		if removeAssignment(other, clusterID, hostname) {
			if err := s.writeRecord(other); err != nil {
				return Machine{}, err
			}
		}
	}
	// A machine may hold assignments across clusters (and, deliberately, more
	// than one node in a cluster — surfaced as a non-blocking UI error). Add or
	// update this (cluster, hostname).
	migrateAssignments(target)
	found := false
	for i := range target.Assignments {
		if target.Assignments[i].ClusterID == clusterID && target.Assignments[i].Hostname == hostname {
			target.Assignments[i].OSDisk = osDisk
			found = true
			break
		}
	}
	if !found {
		target.Assignments = append(target.Assignments, Assignment{ClusterID: clusterID, Hostname: hostname, OSDisk: osDisk})
	}
	target.Assignment = nil // canonical form is the list now
	if err := s.writeRecord(target); err != nil {
		return Machine{}, err
	}
	return target.toMachine(), nil
}

// Unassign clears all of a machine's node bindings.
func (s *Store) Unassign(id string) (Machine, error) {
	r, err := s.readRecord(id)
	if err != nil {
		return Machine{}, err
	}
	r.Assignment = nil
	r.Assignments = nil
	if err := s.writeRecord(r); err != nil {
		return Machine{}, err
	}
	return r.toMachine(), nil
}

// migrateAssignments folds a legacy single Assignment into the list in place.
func migrateAssignments(r *record) {
	if len(r.Assignments) == 0 && r.Assignment != nil {
		r.Assignments = []Assignment{*r.Assignment}
	}
	r.Assignment = nil
}

// removeAssignment drops a (clusterID, hostname) binding from a record's list
// (and legacy field), returning whether anything changed.
func removeAssignment(r *record, clusterID, hostname string) bool {
	migrateAssignments(r)
	kept := r.Assignments[:0]
	changed := false
	for _, a := range r.Assignments {
		if a.ClusterID == clusterID && a.Hostname == hostname {
			changed = true
			continue
		}
		kept = append(kept, a)
	}
	r.Assignments = kept
	return changed
}

// UnassignCluster clears all bindings for a cluster (called on cluster
// delete). Best-effort; missing store is not an error.
func (s *Store) UnassignCluster(clusterID string) error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		r, err := s.readRecord(id)
		if err != nil {
			continue
		}
		migrateAssignments(r)
		kept := r.Assignments[:0]
		changed := false
		for _, a := range r.Assignments {
			if a.ClusterID == clusterID {
				changed = true
				continue
			}
			kept = append(kept, a)
		}
		r.Assignments = kept
		if changed {
			if err := s.writeRecord(r); err != nil {
				return err
			}
		}
	}
	return nil
}

// SetInventory records a successful discovery result.
func (s *Store) SetInventory(id string, inv Inventory) error {
	r, err := s.readRecord(id)
	if err != nil {
		return err
	}
	// Preserve richer previously-discovered facts that a sparser fetch omits.
	// A Redfish fetch fills NICs/disks/CPU/mem; when it fails and we fall back
	// to IPMI FRU (serial only), a wholesale replace would wipe that good data.
	// So fields the new result leaves empty keep their prior value.
	if r.Inventory != nil {
		inv = mergeInventory(inv, *r.Inventory)
	}
	r.Inventory = &inv
	r.FetchState = FetchOK
	r.FetchError = ""
	return s.writeRecord(r)
}

// mergeInventory returns next with any empty rich field filled from prev, so a
// downgraded (IPMI-fallback) fetch never clobbers Redfish-discovered hardware.
// A successful re-fetch that DOES carry these fields still overwrites them.
func mergeInventory(next, prev Inventory) Inventory {
	if len(next.NICs) == 0 {
		next.NICs = prev.NICs
	}
	if len(next.Disks) == 0 {
		next.Disks = prev.Disks
	}
	if len(next.Cards) == 0 {
		next.Cards = prev.Cards
	}
	if next.CPUCount == 0 {
		next.CPUCount = prev.CPUCount
	}
	if next.CPUCores == 0 {
		next.CPUCores = prev.CPUCores
	}
	if next.CPUModel == "" {
		next.CPUModel = prev.CPUModel
	}
	if next.MemoryBytes == 0 {
		next.MemoryBytes = prev.MemoryBytes
	}
	if next.Manufacturer == "" {
		next.Manufacturer = prev.Manufacturer
	}
	if next.Model == "" {
		next.Model = prev.Model
	}
	if next.Serial == "" {
		next.Serial = prev.Serial
	}
	return next
}
