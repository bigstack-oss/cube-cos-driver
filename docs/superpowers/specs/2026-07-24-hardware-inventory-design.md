# Hardware Inventory — Design

Date: 2026-07-24
Status: Approved (Travis, 2026-07-24, via decision questions)

## Goal

Let users maintain a **global inventory of machines**, each carrying its BMC
connection info (address / username / password), and have the backend use
that BMC info to **fetch hardware facts** — CPU, memory, NICs (name/MAC/
speed), disks, PCIe cards, and serial/manufacturer/model. This is the
BMC-inventory + discovery foundation for the zero-touch orchestration
roadmap (`docs/roadmap.md`): registered hardware is later appointed to
cluster nodes and driven over IPMI.

## Decisions (approved)

| Decision | Choice |
| --- | --- |
| Scope | **Global machine pool** (independent of clusters; appoint later) |
| Credentials at rest | **Encrypted** (AES-GCM; key from env/file, auto-generated key file fallback) |
| Discovery transport | **Redfish-first (gofish) + IPMI FRU fallback (go-ipmi)** |
| Dependencies | **Vendored Go libs** (`go mod vendor`; stays a single static air-gapped binary) |

## Data model (`internal/inventory`)

```go
type BMC struct {
    Address  string `json:"address"`   // host or host:port
    Username string `json:"username"`
    // password stored encrypted on disk; never serialized to the API
}

type NIC struct {
    Name       string `json:"name,omitempty"`
    MAC        string `json:"mac,omitempty"`
    SpeedMbps  int    `json:"speedMbps,omitempty"`
    Up         bool   `json:"up,omitempty"`
}
type Disk struct {
    Name      string `json:"name,omitempty"`
    Model     string `json:"model,omitempty"`
    SizeBytes int64  `json:"sizeBytes,omitempty"`
    Type      string `json:"type,omitempty"` // HDD/SSD/NVMe
}
type Card struct {
    Slot string `json:"slot,omitempty"`
    Name string `json:"name,omitempty"`
    Type string `json:"type,omitempty"`
}
type Inventory struct {
    FetchedAt    string `json:"fetchedAt"`
    Source       string `json:"source"`        // redfish | ipmi
    Manufacturer string `json:"manufacturer,omitempty"`
    Model        string `json:"model,omitempty"`
    Serial       string `json:"serial,omitempty"`
    CPUModel     string `json:"cpuModel,omitempty"`
    CPUCount     int    `json:"cpuCount,omitempty"`
    CPUCores     int    `json:"cpuCores,omitempty"`  // total cores
    MemoryBytes  int64  `json:"memoryBytes,omitempty"`
    NICs         []NIC  `json:"nics,omitempty"`
    Disks        []Disk `json:"disks,omitempty"`
    Cards        []Card `json:"cards,omitempty"`
}

type FetchState string // idle | fetching | ok | error

type Machine struct {
    ID          string     `json:"id"`
    Label       string     `json:"label"`
    BMC         BMC        `json:"bmc"`
    HasPassword bool       `json:"hasPassword"`     // computed; password never returned
    Inventory   *Inventory `json:"inventory,omitempty"`
    FetchState  FetchState `json:"fetchState"`
    FetchError  string     `json:"fetchError,omitempty"`
}
```

## Storage

- `<data-dir>/machines/<id>.json` — one machine per file; atomic write
  (temp + rename). The on-disk record holds the **encrypted** password
  (base64 AES-GCM ciphertext); it is stripped from every API response, which
  instead exposes `hasPassword`.
- Passwords are encrypted via `internal/secret`:
  - Key source order: `--secret-key-file` flag / `SNAPSHOT_SECRET_KEY` env
    (32-byte key, hex or base64), else auto-generate `<data-dir>/.secret-key`
    (0600) on first use. Auto-gen is the turnkey path for a single-host
    pxeserver; env/file override is for stronger key management.
  - AES-256-GCM, random nonce per value, nonce prepended to ciphertext.
- If encryption is somehow unavailable, the store refuses to persist a
  password rather than writing plaintext.

## Discovery (`internal/discovery`)

```go
type Target struct { Address, Username, Password string }

type Discoverer interface {
    Discover(ctx context.Context, t Target) (inventory.Inventory, error)
}
```

- `redfishDiscoverer` (gofish): Systems → processors, memory, NIC MACs
  (EthernetInterfaces / NetworkAdapters), storage drives, PCIe devices,
  serial/model/manufacturer.
- `ipmiDiscoverer` (go-ipmi): FRU (serial, board/product), basic memory —
  fallback only.
- `combined` runs Redfish first; on error (or empty core fields) falls back
  to IPMI and tags `Source` accordingly.
- The interface is the seam for tests: real BMCs are never contacted in CI;
  handlers/store are tested against a `fakeDiscoverer`. The gofish/go-ipmi
  adapters compile and are exercised only against lab hardware (spike).

## REST API (adds to `/api/v1`)

| Method & path | Purpose |
| --- | --- |
| `GET /api/v1/machines` | List machines (no secrets; includes last inventory + fetch state) |
| `POST /api/v1/machines` | Create `{label, bmc:{address,username,password}}` |
| `GET /api/v1/machines/{id}` | One machine |
| `PUT /api/v1/machines/{id}` | Update; `bmc.password` omitted/empty ⇒ keep existing |
| `DELETE /api/v1/machines/{id}` | Remove |
| `POST /api/v1/machines/{id}/fetch` | Trigger async discovery; `202`; progress via `fetchState` on GET |

Fetch runs in a goroutine; `fetchState` transitions idle→fetching→ok/error,
persisted so the UI (polling) reflects it. Concurrent fetch on an already
-fetching machine is a no-op `202`.

## Frontend (`web/src/pages/hardware`)

- New route `/hardware` + header nav link.
- `CosBasicTable` of machines: label, BMC address, serial, CPU (model ×count),
  memory (GiB), NIC count, disk count, fetch status (`CosStatus`/tag).
- Add/Edit modal (`CosModal`): label, address, username, password
  (placeholder "unchanged" on edit).
- Row actions: **Fetch** (calls fetch endpoint, then polls), **Details**
  (expand/modal showing full NIC/disk/card lists), **Edit**, **Delete**.
- Client `web/src/api/machines.ts`; types in `web/src/model/machine.ts`.

## Testing

- Go: `secret` encrypt/decrypt roundtrip + key gen; `inventory` store CRUD
  with password encryption + strip; `discovery` combiner with fake adapters
  (Redfish-fail → IPMI fallback); `api` machines CRUD + async fetch via
  `fakeDiscoverer`.
- Web: machines client + Hardware table/modal render & validation.
- CI never touches real BMCs; vendored deps keep the build offline.

## Out of scope (later roadmap stories)

Power/bootdev control, per-MAC PXE binding, snapshot appointment to a
machine, the zero-touch agent, job engine. This PR is inventory + discovery
only.
