package local

import (
	"net"
	"slices"
	"testing"

	"github.com/norlis/hydra/internal/topology"
)

func candidates() []candidate {
	up := net.FlagUp
	return []candidate{
		{name: "lo0", flags: up | net.FlagLoopback, ip: "127.0.0.1", cidr: "127.0.0.1/8"},
		{name: "en0", flags: up, ip: "192.168.1.10", cidr: "192.168.1.10/24"},
		{name: "bridge100", flags: up, ip: "192.168.139.3", cidr: "192.168.139.3/23"},
		{name: "utun32", flags: up, ip: "100.114.144.54", cidr: "100.114.144.54/32"},
		{name: "en7", flags: 0, ip: "10.0.0.7", cidr: "10.0.0.7/24"}, // down
		{name: "awdl0", flags: up, ip: "", cidr: ""},                 // no IPv4
	}
}

func names(ifs []topology.NetworkInterface) []string {
	out := make([]string, len(ifs))
	for i, f := range ifs {
		out[i] = f.Name
	}
	return out
}

func ports(ifs []topology.NetworkInterface) []int {
	out := make([]int, len(ifs))
	for i, f := range ifs {
		out[i] = f.ServicePort
	}
	return out
}

func TestSelectInterfaces(t *testing.T) {
	t.Parallel()

	t.Run("without an allow list every up non-loopback IPv4 interface gets consecutive ports", func(t *testing.T) {
		t.Parallel()
		got := selectInterfaces(candidates(), nil, 3128)
		if !slices.Equal(names(got), []string{"en0", "bridge100", "utun32"}) || !slices.Equal(ports(got), []int{3128, 3129, 3130}) {
			t.Errorf("got %v %v", names(got), ports(got))
		}
	})

	t.Run("an allow list keeps only the listed interfaces and numbers ports in list order", func(t *testing.T) {
		t.Parallel()
		got := selectInterfaces(candidates(), []string{"utun32", "en0"}, 3128)
		if !slices.Equal(names(got), []string{"utun32", "en0"}) || !slices.Equal(ports(got), []int{3128, 3129}) {
			t.Errorf("got %v %v", names(got), ports(got))
		}
	})

	t.Run("a listed interface that is down or absent keeps its port reserved", func(t *testing.T) {
		t.Parallel()
		got := selectInterfaces(candidates(), []string{"en0", "en7", "missing", "utun32"}, 3128)
		if !slices.Equal(names(got), []string{"en0", "utun32"}) || !slices.Equal(ports(got), []int{3128, 3131}) {
			t.Errorf("got %v %v, want utun32 to keep the port of its list position", names(got), ports(got))
		}
	})

	t.Run("loopback and interfaces without IPv4 are never selected even when listed", func(t *testing.T) {
		t.Parallel()
		if got := selectInterfaces(candidates(), []string{"lo0", "awdl0"}, 3128); len(got) != 0 {
			t.Errorf("got %v, want none", names(got))
		}
	})

	t.Run("the selected interface carries its address and subnet", func(t *testing.T) {
		t.Parallel()
		got := selectInterfaces(candidates(), []string{"en0"}, 4000)
		want := topology.NetworkInterface{Name: "en0", PrivateIP: "192.168.1.10", SubnetCIDR: "192.168.1.10/24", ServicePort: 4000}
		if len(got) != 1 || got[0] != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
}
