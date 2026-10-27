package hydra_test

import (
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	hydra "github.com/norlis/hydra/internal"
	"github.com/norlis/hydra/internal/testlog"
)

const (
	envAuthMode = "HYDRA_PROXY_AUTH_MODE"
	envAuthUser = "HYDRA_PROXY_AUTH_USER"
	envAuthPass = "HYDRA_PROXY_AUTH_PASS"
	envSecret   = "HYDRA_GOSSIP_SECRET"
	envPorts    = "HYDRA_PROXY_ALLOWED_PORTS"
	envSeeds    = "HYDRA_GOSSIP_SEEDS"
)

// clearEnv unsets every variable Config reads so defaults are observable.
// t.Setenv registers the restore; os.Unsetenv then makes the variable truly
// absent (not just empty) for the duration of the test.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"ENVIRONMENT", "BASE_PORT", "CONTROL_PORT", "HYDRA_DRAIN_DELAY", "HYDRA_NODE_NAME", "HYDRA_GOSSIP_ADDR", "HYDRA_GOSSIP_PORT", envSeeds, "HYDRA_GOSSIP_REJOIN_INTERVAL", envSecret, "HYDRA_CLUSTER_TAG", "HYDRA_STRIP_HEADERS", envPorts, "HYDRA_PROXY_DENY_CIDR", "HYDRA_PROXY_ALLOW_CIDR", "HYDRA_PROXY_MAX_TUNNELS", "HYDRA_PROXY_IDLE_TIMEOUT", envAuthMode, envAuthUser, envAuthPass, "HYDRA_CLOUDMAP_REGION", "HYDRA_CLOUDMAP_NAMESPACE", "HYDRA_CLOUDMAP_SERVICE", "HYDRA_INTERFACES"} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNewConfigFromEnv(t *testing.T) {
	t.Run("defaults apply when no variable is set", func(t *testing.T) { //nolint:paralleltest // t.Setenv
		clearEnv(t)
		cfg, err := hydra.NewConfigFromEnv(slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Environment != "local" || cfg.ControlPort != "9192" || cfg.BasePort != 3128 || cfg.ProxyAuthMode != "none" || cfg.GossIPBindPort != 7946 {
			t.Errorf("defaults = env:%q control:%q base:%d auth:%q gossip:%d", cfg.Environment, cfg.ControlPort, cfg.BasePort, cfg.ProxyAuthMode, cfg.GossIPBindPort)
		}
		if !slices.Equal(cfg.ProxyAllowedPorts, []int{443, 80}) {
			t.Errorf("allowed ports = %v, want [443 80]", cfg.ProxyAllowedPorts)
		}
	})

	errCases := []struct {
		name string
		env  map[string]string
		want string // substring of the error
	}{
		{"auth mode with uppercase is rejected", map[string]string{envAuthMode: "Basic", envAuthUser: "u", envAuthPass: "p"}, "must be one of"},
		{"auth mode with a typo is rejected", map[string]string{envAuthMode: "basci"}, "must be one of"},
		{"basic without user is rejected", map[string]string{envAuthMode: "basic", envAuthPass: "p"}, "requires"},
		{"basic without password is rejected", map[string]string{envAuthMode: "basic", envAuthUser: "u"}, "requires"},
		{"non-hex secret is rejected", map[string]string{envSecret: "zz"}, "hex"},
		{"15-byte secret is rejected", map[string]string{envSecret: strings.Repeat("ab", 15)}, "16/24/32"},
	}
	for _, tc := range errCases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, err := hydra.NewConfigFromEnv(slog.New(slog.DiscardHandler))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want containing %q", err, tc.want)
			}
		})
	}

	for _, n := range []int{16, 24, 32} {
		t.Run("valid "+strconv.Itoa(n)+"-byte secret decodes", func(t *testing.T) {
			clearEnv(t)
			t.Setenv(envSecret, strings.Repeat("ab", n))
			cfg, err := hydra.NewConfigFromEnv(slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatal(err)
			}
			key, err := cfg.GossipSecret()
			if err != nil || len(key) != n {
				t.Errorf("GossipSecret = %d bytes, %v; want %d", len(key), err, n)
			}
		})
	}

	t.Run("missing secret yields nil key and one warning", func(t *testing.T) { //nolint:paralleltest // t.Setenv
		clearEnv(t)
		rec := testlog.New()
		cfg, err := hydra.NewConfigFromEnv(rec.Logger())
		if err != nil {
			t.Fatal(err)
		}
		if key, err := cfg.GossipSecret(); key != nil || err != nil {
			t.Errorf("GossipSecret = %v, %v; want nil, nil", key, err)
		}
		warns := 0
		for _, e := range rec.Entries() {
			if e.Level == slog.LevelWarn && strings.Contains(e.Message, "gossip encryption disabled") {
				warns++
			}
		}
		if warns != 1 {
			t.Errorf("warn count = %d, want 1", warns)
		}
	})

	t.Run("explicitly empty allowed ports keeps the default", func(t *testing.T) {
		clearEnv(t)
		t.Setenv(envPorts, "")
		cfg, err := hydra.NewConfigFromEnv(slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(cfg.ProxyAllowedPorts, []int{443, 80}) {
			t.Errorf("allowed ports = %v: an empty value does NOT mean unrestricted, the default applies", cfg.ProxyAllowedPorts)
		}
	})

	t.Run("seeds with surrounding spaces are trimmed", func(t *testing.T) {
		clearEnv(t)
		t.Setenv(envSeeds, " a:1 , b:2 ")
		cfg, err := hydra.NewConfigFromEnv(slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(cfg.GossIPSeeds, []string{"a:1", "b:2"}) {
			t.Errorf("seeds = %q", cfg.GossIPSeeds)
		}
	})

	t.Run("node name defaults to the hostname so nodes started without it stay distinct", func(t *testing.T) { //nolint:paralleltest // t.Setenv
		clearEnv(t)
		host, err := os.Hostname()
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := hydra.NewConfigFromEnv(slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.GossIPNodeName != host {
			t.Errorf("node name = %q, want hostname %q", cfg.GossIPNodeName, host)
		}
	})

	t.Run("an explicit node name wins over the hostname", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("HYDRA_NODE_NAME", "edge-1")
		cfg, err := hydra.NewConfigFromEnv(slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.GossIPNodeName != "edge-1" {
			t.Errorf("node name = %q, want edge-1", cfg.GossIPNodeName)
		}
	})

	t.Run("interface allow list keeps the given order", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("HYDRA_INTERFACES", "utun32, en0")
		cfg, err := hydra.NewConfigFromEnv(slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(cfg.Interfaces, []string{"utun32", "en0"}) {
			t.Errorf("interfaces = %q", cfg.Interfaces)
		}
	})

	t.Run("empty elements from trailing or doubled commas are dropped", func(t *testing.T) {
		clearEnv(t)
		t.Setenv(envSeeds, "a:1,, b:2 ,")
		cfg, err := hydra.NewConfigFromEnv(slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(cfg.GossIPSeeds, []string{"a:1", "b:2"}) {
			t.Errorf("seeds = %q, want only the non-empty elements", cfg.GossIPSeeds)
		}
	})
}
