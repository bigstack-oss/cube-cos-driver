package orchestrator

import (
	"bufio"
	"context"
	"os"
	"strings"
)

// LeaseObserver watches the pxeserver's dnsmasq leases (and optionally the
// lighttpd access log) to infer a node's install stage from its MACs.
type LeaseObserver struct {
	LeasesPath  string // e.g. /var/lib/misc/dnsmasq.leases
	AccessLog   string // e.g. /var/log/lighttpd/access.log (optional)
	MediaSuffix string // request substring meaning "fetching install media", e.g. ".pkg"
}

func DefaultObserver() LeaseObserver {
	return LeaseObserver{
		LeasesPath:  "/var/lib/misc/dnsmasq.leases",
		AccessLog:   "/var/log/lighttpd/access.log",
		MediaSuffix: ".pkg",
	}
}

func (o LeaseObserver) Stage(_ context.Context, macs []string) (Stage, error) {
	want := map[string]bool{}
	for _, m := range macs {
		want[strings.ToLower(strings.TrimSpace(m))] = true
	}
	leaseIP := o.leaseIP(want)
	if leaseIP == "" {
		return StageNone, nil
	}
	// A lease exists → at least DHCP. If the leased IP is fetching media in
	// the access log, it's imaging.
	if o.AccessLog != "" && o.ipFetchingMedia(leaseIP) {
		return StageImaging, nil
	}
	return StageDHCP, nil
}

// leaseIP returns the leased IP for any of the wanted MACs, or "".
func (o LeaseObserver) leaseIP(want map[string]bool) string {
	f, err := os.Open(o.LeasesPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		// dnsmasq.leases: <expiry> <mac> <ip> <hostname> <clientid>
		fields := strings.Fields(sc.Text())
		if len(fields) >= 3 && want[strings.ToLower(fields[1])] {
			return fields[2]
		}
	}
	return ""
}

func (o LeaseObserver) ipFetchingMedia(ip string) bool {
	f, err := os.Open(o.AccessLog)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, ip) && strings.Contains(line, o.MediaSuffix) {
			return true
		}
	}
	return false
}
