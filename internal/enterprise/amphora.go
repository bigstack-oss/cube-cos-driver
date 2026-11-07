package enterprise

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/bigstack-oss/cube-cos-driver/internal/clusterssh"
)

// amphoraCapacityProbe reads Octavia's amphora flavor and topology, this
// release's load balancers ("lb <name> <status>") and how many more amphorae
// of that flavor fit in placement, summed per compute host.
const amphoraCapacityProbe = `source /etc/admin-openrc.sh && python3 -c '
import json,subprocess,sys,configparser
def osc(*a):
    return json.loads(subprocess.check_output(["openstack"]+list(a)+["-f","json"],stderr=subprocess.DEVNULL))
c=configparser.ConfigParser(strict=False,interpolation=None)
c.read("/etc/octavia/octavia.conf")
fid=c.get("controller_worker","amp_flavor_id",fallback="")
print("topology",c.get("controller_worker","loadbalancer_topology",fallback="SINGLE"))
try:
    for lb in osc("loadbalancer","list"):
        if lb["name"].startswith(sys.argv[1]):
            print("lb",lb["name"],lb["provisioning_status"])
except Exception:
    pass
if not fid:
    sys.exit(0)
f=osc("flavor","show",fid)
need={"VCPU":int(f["vcpus"]),"MEMORY_MB":int(f["ram"]),"DISK_GB":int(f["disk"])}
print("flavor",f["name"],need["VCPU"],need["MEMORY_MB"],need["DISK_GB"])
fit=0
for rp in osc("resource","provider","list"):
    inv={r["resource_class"]:r for r in osc("resource","provider","inventory","list",rp["uuid"])}
    if "VCPU" not in inv:
        continue
    use={r["resource_class"]:r["usage"] for r in osc("resource","provider","usage","show",rp["uuid"])}
    n=None
    for rc,q in need.items():
        if q<=0 or rc not in inv:
            continue
        i=inv[rc]
        if q>i.get("max_unit",q):
            n=0
            break
        free=int((i["total"]-i["reserved"])*i["allocation_ratio"])-use.get(rc,0)
        k=max(free,0)//q
        n=k if n is None else min(n,k)
    fit+=n or 0
print("fit",fit)
' %s`

type amphoraCapacity struct {
	flavor              string
	vcpus, ramMB, diskG int
	standby             bool              // ACTIVE_STANDBY: two amphorae per LB
	fit                 int               // more amphorae placement can host
	known               bool              // flavor and fit were both read
	lbs                 map[string]string // this release's LBs: name -> status
}

func parseAmphoraCapacity(lines []string) amphoraCapacity {
	c := amphoraCapacity{lbs: map[string]string{}}
	var gotFlavor, gotFit bool
	for _, l := range lines {
		f := strings.Fields(l)
		switch {
		case len(f) == 2 && f[0] == "topology":
			c.standby = strings.EqualFold(f[1], "ACTIVE_STANDBY")
		case len(f) == 3 && f[0] == "lb":
			c.lbs[f[1]] = f[2]
		case len(f) == 5 && f[0] == "flavor":
			v, e1 := strconv.Atoi(f[2])
			r, e2 := strconv.Atoi(f[3])
			d, e3 := strconv.Atoi(f[4])
			if e1 == nil && e2 == nil && e3 == nil {
				c.flavor, c.vcpus, c.ramMB, c.diskG, gotFlavor = f[1], v, r, d, true
			}
		case len(f) == 2 && f[0] == "fit":
			if n, err := strconv.Atoi(f[1]); err == nil {
				c.fit, gotFit = n, true
			}
		}
	}
	c.known = gotFlavor && gotFit
	return c
}

// advisorLBPrefix is the Octavia name prefix of every LB the advisor release
// on framework fw creates.
func advisorLBPrefix(fw string) string { return "kube_service_" + fw + "_cube-advisor_" }

// advisorLBsToCreate lists the load balancers an install will create: the
// advisor's own and one per wired console origin (the first two pool
// addresses), less those already up. One in ERROR still needs its amphora.
func advisorLBsToCreate(fw string, p InstallParams, lbs map[string]string) []string {
	prefix := advisorLBPrefix(fw)
	want := []string{prefix + "cube-advisor"}
	var pool []string
	for _, a := range p.AdvisorPool {
		if a != "" {
			pool = append(pool, a)
		}
	}
	if len(pool) >= 2 {
		for _, a := range pool[:2] {
			want = append(want, prefix+"cube-advisor-web-"+strings.NewReplacer(".", "-", ":", "-").Replace(a))
		}
	}
	var out []string
	for _, n := range want {
		if st, ok := lbs[n]; !ok || st == "ERROR" {
			out = append(out, n)
		}
	}
	return out
}

// checkAmphoraCapacity refuses an advisor install whose load balancers' amphorae
// the cluster cannot host: each console origin is an Octavia LB, so an origin
// costs a VM, not just an address.
func checkAmphoraCapacity(ctx context.Context, client clusterssh.Client, fw string, p InstallParams, onLine func(string)) error {
	var lines []string
	_ = client.Run(ctx, fmt.Sprintf(amphoraCapacityProbe, shellQuote(advisorLBPrefix(fw))), func(l string) { lines = append(lines, l) })
	c := parseAmphoraCapacity(lines)
	create := advisorLBsToCreate(fw, p, c.lbs)
	if len(create) == 0 {
		onLine("Every Advisor load balancer is already up — no new amphorae needed.")
		return nil
	}
	if !c.known {
		onLine("⚠ could not read the amphora flavor or free compute capacity; not checked that the Advisor's load balancers fit")
		return nil
	}
	per := 1
	if c.standby {
		per = 2
	}
	need := len(create) * per
	what := fmt.Sprintf("%d new load balancer(s) × %d amphora VM(s) of flavor %s (%d vCPU, %d MB RAM, %d GB disk) = %d amphorae",
		len(create), per, c.flavor, c.vcpus, c.ramMB, c.diskG, need)
	if c.fit < need {
		return fmt.Errorf("the Advisor needs %s, but the cluster's free compute fits %d — each web-console address is its own load balancer. "+
			"Free capacity (delete or stop VMs, or raise the overcommit ratios in tuning), or leave the web-console addresses empty to install without the console", what, c.fit)
	}
	onLine(fmt.Sprintf("  ✓ %s; free compute fits %d", what, c.fit))
	return nil
}
