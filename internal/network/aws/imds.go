package aws

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/go-cleanhttp"
	hydra "github.com/norlis/hydra/internal"
	"github.com/norlis/hydra/internal/network"
	"github.com/norlis/hydra/internal/topology"
	"go.uber.org/zap"
)

const (
	metaBase    = "http://169.254.169.254/latest"
	imdsTimeout = 2 * time.Second
)

// IMDSProvider discovers ENIs of the current EC2 instance using IMDSv2.
// Satisfies network.Provider.
type IMDSProvider struct {
	basePort int
	log      *zap.Logger
	client   *http.Client
}

func NewIMDSProvider(cfg *hydra.Config, log *zap.Logger) *IMDSProvider {
	return &IMDSProvider{
		basePort: cfg.BasePort,
		log:      log,
		client:   cleanhttp.DefaultClient(),
	}
}

// Discover returns the ENIs attached to this instance, sorted by
// device-number. ServicePort follows the same convention as the local
// provider: basePort + positional index among valid interfaces.
func (p *IMDSProvider) Discover() ([]topology.NetworkInterface, error) {
	token, err := p.imdsToken()
	if err != nil {
		return nil, fmt.Errorf("imds token: %w", err)
	}

	macsRaw, err := p.imds("/network/interfaces/macs/", token)
	if err != nil {
		return nil, fmt.Errorf("listing macs: %w", err)
	}

	type entry struct {
		iface  topology.NetworkInterface
		devNum int
	}

	entries := make([]entry, 0)
	for line := range strings.Lines(macsRaw) {
		mac := strings.TrimSuffix(strings.TrimSpace(line), "/")
		if mac == "" {
			continue
		}
		base := "/network/interfaces/macs/" + mac

		privateIP, err := p.imds(base+"/local-ipv4s", token)
		if err != nil || privateIP == "" {
			p.log.Warn("skipping interface: missing private IP",
				zap.String("mac", mac), zap.Error(err))
			continue
		}

		deviceNumStr, err := p.imds(base+"/device-number", token)
		if err != nil {
			p.log.Warn("skipping interface: failed to get device number",
				zap.String("mac", mac), zap.Error(err))
			continue
		}
		devNum, err := strconv.Atoi(strings.TrimSpace(deviceNumStr))
		if err != nil {
			p.log.Warn("skipping interface: malformed device number",
				zap.String("mac", mac),
				zap.String("value", deviceNumStr),
				zap.Error(err))
			continue
		}

		// Optional metadata; missing is not fatal.
		publicIP, _ := p.imds(base+"/public-ipv4s", token)
		subnetCIDR, _ := p.imds(base+"/subnet-ipv4-cidr-block", token)

		entries = append(entries, entry{
			devNum: devNum,
			iface: topology.NetworkInterface{
				Name:       fmt.Sprintf("eth%d", devNum),
				MAC:        mac,
				PrivateIP:  privateIP,
				PublicIP:   publicIP,
				SubnetCIDR: subnetCIDR,
				Reachable:  true,
			},
		})
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].devNum < entries[j].devNum })

	ifaces := make([]topology.NetworkInterface, len(entries))
	for i, e := range entries {
		e.iface.ServicePort = network.BuildServicePort(p.basePort, i)
		ifaces[i] = e.iface
	}
	return ifaces, nil
}

func (p *IMDSProvider) imdsToken() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), imdsTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, metaBase+"/api/token", http.NoBody)
	if err != nil {
		return "", fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "21600")

	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("token http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return strings.TrimSpace(string(body)), nil
}

// imds fetches a single IMDS metadata field. Returns an empty string
// on 404 (optional fields absent from this instance) and an error on
// any other non-2xx.
func (p *IMDSProvider) imds(path, token string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), imdsTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metaBase+"/meta-data"+path, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("X-aws-ec2-metadata-token", token)

	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("metadata request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return "", nil
	case resp.StatusCode/100 != 2:
		return "", fmt.Errorf("imds http %d at %s: %s", resp.StatusCode, path, strings.TrimSpace(string(body)))
	}
	return strings.TrimSpace(string(body)), nil
}
