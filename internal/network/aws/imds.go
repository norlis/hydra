package aws

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	hydra "github.com/norlis/hydra/internal"
	"github.com/norlis/hydra/internal/network"
	"github.com/norlis/hydra/internal/topology"
	"github.com/norlis/hydra/pkg/logger"
)

const imdsTimeout = 5 * time.Second

// metadataClient is the subset of the AWS IMDS client the provider uses.
// Narrowed to one method so tests can supply a fake without a live IMDS
// endpoint or HTTP server.
type metadataClient interface {
	GetMetadata(ctx context.Context, params *imds.GetMetadataInput, optFns ...func(*imds.Options)) (*imds.GetMetadataOutput, error)
}

// IMDSProvider discovers ENIs of the current EC2 instance using IMDSv2.
// Satisfies network.Provider.
type IMDSProvider struct {
	basePort int
	log      *slog.Logger
	client   metadataClient
	// linkNamesByMAC maps host NIC hardware address (lowercase) to kernel
	// interface name. Injectable for tests; defaults to kernelLinkNamesByMAC.
	linkNamesByMAC func() (map[string]string, error)
}

func NewIMDSProvider(cfg *hydra.Config, log *slog.Logger) *IMDSProvider {
	return &IMDSProvider{
		basePort:       cfg.BasePort,
		log:            log,
		client:         imds.New(imds.Options{}),
		linkNamesByMAC: kernelLinkNamesByMAC,
	}
}

// kernelLinkNamesByMAC correlates the host's NICs by MAC so IMDS-discovered
// interfaces can report the kernel name (e.g. "ens5") the OS actually uses,
// instead of a synthetic "eth<device-number>". IMDS does not expose it.
func kernelLinkNamesByMAC() (map[string]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("listing host interfaces: %w", err)
	}
	m := make(map[string]string, len(ifaces))
	for _, ifi := range ifaces {
		if ifi.HardwareAddr == nil {
			continue
		}
		m[strings.ToLower(ifi.HardwareAddr.String())] = ifi.Name
	}
	return m, nil
}

// interfaceName returns the kernel interface name for mac (matching the OS),
// falling back to the synthetic "eth<devNum>" when the MAC has no local match.
func (p *IMDSProvider) interfaceName(mac string, devNum int, linkNames map[string]string) string {
	if name, ok := linkNames[strings.ToLower(mac)]; ok && name != "" {
		return name
	}
	synthetic := fmt.Sprintf("eth%d", devNum)
	p.log.Warn("no kernel interface name for MAC; using synthetic name",
		slog.String("mac", mac), slog.String("name", synthetic))
	return synthetic
}

// Discover returns the ENIs attached to this instance, sorted by
// device-number. ServicePort follows the same convention as the local
// provider: basePort + positional index among valid interfaces.
func (p *IMDSProvider) Discover() ([]topology.NetworkInterface, error) {
	ctx, cancel := context.WithTimeout(context.Background(), imdsTimeout)
	defer cancel()

	macsRaw, err := p.metadata(ctx, "/network/interfaces/macs/")
	if err != nil {
		return nil, fmt.Errorf("listing macs: %w", err)
	}

	linkNames, err := p.linkNamesByMAC()
	if err != nil {
		p.log.Warn("failed to resolve kernel interface names; falling back to eth<device-number>",
			logger.Err(err))
		linkNames = map[string]string{}
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

		privateIP, err := p.metadata(ctx, base+"/local-ipv4s")
		if err != nil || privateIP == "" {
			p.log.Warn("skipping interface: missing private IP",
				slog.String("mac", mac), logger.Err(err))
			continue
		}

		deviceNumStr, err := p.metadata(ctx, base+"/device-number")
		if err != nil {
			p.log.Warn("skipping interface: failed to get device number",
				slog.String("mac", mac), logger.Err(err))
			continue
		}
		devNum, err := strconv.Atoi(strings.TrimSpace(deviceNumStr))
		if err != nil {
			p.log.Warn("skipping interface: malformed device number",
				slog.String("mac", mac),
				slog.String("value", deviceNumStr),
				logger.Err(err))
			continue
		}

		// Optional metadata; missing is not fatal.
		publicIP, _ := p.metadata(ctx, base+"/public-ipv4s")
		subnetCIDR, _ := p.metadata(ctx, base+"/subnet-ipv4-cidr-block")
		interfaceID, _ := p.metadata(ctx, base+"/interface-id")

		entries = append(entries, entry{
			devNum: devNum,
			iface: topology.NetworkInterface{
				Name:       p.interfaceName(mac, devNum, linkNames),
				MAC:        mac,
				PrivateIP:  privateIP,
				PublicIP:   publicIP,
				SubnetCIDR: subnetCIDR,
				PhysicalID: interfaceID,
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

// metadata fetches a single IMDS metadata field. A 404 (optional field
// absent from this instance) yields an empty string; any other error is
// returned wrapped.
func (p *IMDSProvider) metadata(ctx context.Context, path string) (string, error) {
	out, err := p.client.GetMetadata(ctx, &imds.GetMetadataInput{Path: path})
	if err != nil {
		var statusErr interface{ HTTPStatusCode() int }
		if errors.As(err, &statusErr) && statusErr.HTTPStatusCode() == http.StatusNotFound {
			return "", nil
		}
		return "", fmt.Errorf("imds get %q: %w", path, err)
	}
	defer func() { _ = out.Content.Close() }()

	body, err := io.ReadAll(out.Content)
	if err != nil {
		return "", fmt.Errorf("imds read %q: %w", path, err)
	}
	return strings.TrimSpace(string(body)), nil
}
