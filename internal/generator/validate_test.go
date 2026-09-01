package generator

import (
	"strings"
	"testing"

	"github.com/bigstack-oss/cube-cos-driver/internal/model"
)

func TestValidateNodeNetwork(t *testing.T) {
	mk := func(ip, mask string) model.NodeConfig {
		return model.NodeConfig{InitIFs: []model.IF{{Name: "IF.1", Enabled: true, IPAddr: ip, IPMask: mask}}}
	}
	cases := []struct {
		name    string
		n       model.NodeConfig
		wantErr string
	}{
		{"valid", mk("10.32.41.1", "255.255.0.0"), ""},
		{"empty mask", mk("10.32.41.1", ""), "netmask"},
		{"zero mask", mk("10.32.41.1", "0.0.0.0"), "netmask"},
		{"noncontiguous mask", mk("10.32.41.1", "255.0.255.0"), "netmask"},
		{"mask but no addr", mk("", "255.255.0.0"), "address"},
		{"bad addr", mk("not-an-ip", "255.255.0.0"), "address"},
		{"disabled iface ignored", model.NodeConfig{InitIFs: []model.IF{{Name: "IF.9", Enabled: false, IPAddr: "10.0.0.1"}}}, ""},
		{"L2 iface no ip ignored", model.NodeConfig{InitIFs: []model.IF{{Name: "IF.2", Enabled: true}}}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateNodeNetwork(c.n)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("want ok, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}
