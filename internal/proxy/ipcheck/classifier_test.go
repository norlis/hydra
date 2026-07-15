package ipcheck

import (
	"net/netip"
	"testing"
)

func TestClassifier_Classify(t *testing.T) {
	_ = t.Context()

	// Utilizamos netip.MustParseAddr en lugar de net.ParseIP
	localIP := netip.MustParseAddr("192.168.1.100")
	classifier, err := New(
		[]string{"200.200.200.0/24"}, // Extra deny
		[]string{"172.16.5.0/24"},    // Extra allow (override)
		[]netip.Addr{localIP},        // Ahora es []netip.Addr
	)
	// Validamos que la inicialización no falle
	if err != nil {
		t.Fatalf("Fallo crítico al inicializar el Classifier: %v", err)
	}

	tests := []struct {
		name string
		ip   string
		want Decision
	}{
		{"Allowed Public IP", "3.234.199.80", Allow},
		{"Allowed Public IP", "8.8.8.8", Allow},
		{"Allowed by Override (172.16.5.1)", "172.16.5.1", Allow},
		{"Denied Loopback", "127.0.0.1", DenyLoopback},
		{"Denied Private (10.0.0.1)", "10.0.0.1", DenyPrivateRange},
		{"Denied CGNAT (100.64.0.1)", "100.64.0.1", DenyCGNAT},
		{"Denied Configured (200.200.200.5)", "200.200.200.5", DenyConfigured},
		{"Denied Self", "192.168.1.100", DenySelf},
		{"Denied Multicast", "224.0.0.1", DenyMulticast},
		{"Denied IPv6 LinkLocal", "fe80::1", DenyLinkLocal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Parseamos usando netip
			ip, err := netip.ParseAddr(tt.ip)
			if err != nil {
				t.Fatalf("IP de prueba inválida %q: %v", tt.ip, err)
			}

			got := classifier.Classify(ip)
			if got != tt.want {
				t.Errorf("Classifier.Classify() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	_ = t.Context()

	c, err := New(nil, nil, nil)
	if err != nil {
		t.Fatalf("Fallo crítico al inicializar el Classifier: %v", err)
	}

	// Test con dirección inválida (no contiene IP:Puerto válidos)
	err = c.Validate("tcp", "invalid-address")
	if err == nil {
		t.Error("Se esperaba un error para una dirección inválida, pero se obtuvo nil")
	}

	// Test con IP bloqueada
	err = c.Validate("tcp", "127.0.0.1:80")
	if !IsDenied(err) {
		t.Errorf("Se esperaba que IsDenied fuera true (DeniedError), se obtuvo: %v", err)
	}
}
