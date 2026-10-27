package ipcheck_test

import (
	"fmt"
	"net/netip"
	"testing"

	"github.com/norlis/hydra/internal/proxy/ipcheck"
)

func BenchmarkClassify(b *testing.B) {
	extra := make([]string, 10)
	for i := range extra {
		extra[i] = fmt.Sprintf("203.0.%d.0/24", 10+i)
	}
	c, err := ipcheck.New(extra, nil, []netip.Addr{netip.MustParseAddr("10.1.2.3")})
	if err != nil {
		b.Fatal(err)
	}
	cases := []struct {
		name string
		ip   netip.Addr
	}{
		{"ip pública permitida", netip.MustParseAddr("93.184.216.34")},
		{"último CIDR denegado configurado", netip.MustParseAddr("203.0.19.7")},
		{"ipv4 mapeada en ipv6", netip.MustParseAddr("::ffff:93.184.216.34")},
		{"privada (rechazo temprano)", netip.MustParseAddr("192.168.1.1")},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = c.Classify(tc.ip)
			}
		})
	}
}
