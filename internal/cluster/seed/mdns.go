package seed

import (
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/mdns"
)

const (
	serviceType = "_memberlist._tcp"
	queryDomain = "local"
	queryWindow = 3 * time.Second
)

type MDNSProvider struct {
	port        int
	nodeName    string
	clusterTag  string
	advertiseIP string
	server      *mdns.Server
}

func NewMDNSProvider(port int, nodeName, clusterTag, advertiseIP string) *MDNSProvider {
	return &MDNSProvider{
		port:        port,
		nodeName:    nodeName,
		clusterTag:  clusterTag,
		advertiseIP: advertiseIP,
	}
}

func (p *MDNSProvider) Discover() ([]string, error) {
	if err := p.publishOnce(); err != nil {
		return nil, err
	}
	return p.query()
}

func (p *MDNSProvider) publishOnce() error {
	if p.server != nil {
		return nil
	}
	info := []string{p.clusterTag}
	var ips []net.IP
	if p.advertiseIP != "" {
		if ip := net.ParseIP(p.advertiseIP); ip != nil {
			ips = []net.IP{ip}
		}
	}

	service, err := mdns.NewMDNSService(p.nodeName, serviceType, "", "", p.port, ips, info)
	if err != nil {
		return fmt.Errorf("creating mDNS service: %w", err)
	}
	server, err := mdns.NewServer(&mdns.Config{Zone: service})
	if err != nil {
		return fmt.Errorf("creating mDNS server: %w", err)
	}
	p.server = server
	return nil
}

func (p *MDNSProvider) query() ([]string, error) {
	entries := make(chan *mdns.ServiceEntry, 10)
	go func() {
		_ = mdns.Query(&mdns.QueryParam{
			Service:     serviceType,
			Domain:      queryDomain,
			Timeout:     queryWindow,
			Entries:     entries,
			DisableIPv6: true,
		})
		close(entries)
	}()

	selfPrefix := p.nodeName + "."
	var found []string
	for entry := range entries {
		if strings.HasPrefix(entry.Name, selfPrefix) {
			continue
		}
		if entry.AddrV4 == nil {
			continue
		}
		if !p.hasTag(entry.InfoFields) {
			continue
		}
		found = append(found, fmt.Sprintf("%s:%d", entry.AddrV4.String(), entry.Port))
	}
	return found, nil
}

func (p *MDNSProvider) hasTag(fields []string) bool {
	return slices.Contains(fields, p.clusterTag)
}

func (p *MDNSProvider) Shutdown() error {
	if p.server == nil {
		return nil
	}
	return p.server.Shutdown()
}
