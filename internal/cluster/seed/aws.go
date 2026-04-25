package seed

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	sd "github.com/aws/aws-sdk-go-v2/service/servicediscovery"
	sdtypes "github.com/aws/aws-sdk-go-v2/service/servicediscovery/types"
)

const (
	cloudMapAPITimeout  = 10 * time.Second
	cloudMapMaxAttempts = 8
)

// CloudMapConfig groups the runtime parameters for the Cloud Map provider.
// AdvertiseIP should be the private IP reachable by peer nodes; when empty
// the provider falls back to the first non-loopback IPv4 of the host.
type CloudMapConfig struct {
	Region      string
	Namespace   string
	Service     string
	AdvertiseIP string
	Port        int
	InstanceID  string
}

// CloudMap implements cluster.SeedProvider backed by AWS Cloud Map.
// The first Discover call resolves the service ID and registers this
// node; subsequent calls only refresh the peer list. Shutdown must be
// invoked externally (e.g. from fx lifecycle OnStop) to deregister.
type CloudMap struct {
	cfg    CloudMapConfig
	client *sd.Client

	mu         sync.Mutex
	serviceID  string
	registered bool
}

// NewCloudMap builds the SDK client and returns a ready-to-use provider.
// Loading aws config here (instead of injecting a shared client) keeps
// Cloud Map optional: it is only instantiated when the active
// environment selects it.
func NewCloudMap(ctx context.Context, cfg CloudMapConfig) (*CloudMap, error) {
	switch {
	case cfg.Namespace == "":
		return nil, errors.New("cloudmap: namespace is required")
	case cfg.Service == "":
		return nil, errors.New("cloudmap: service is required")
	case cfg.InstanceID == "":
		return nil, errors.New("cloudmap: instance id is required")
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithDefaultRegion(cfg.Region),
		awsconfig.WithRetryer(func() awssdk.Retryer {
			return retry.NewAdaptiveMode(func(o *retry.AdaptiveModeOptions) {
				o.StandardOptions = append(o.StandardOptions, func(so *retry.StandardOptions) {
					so.MaxAttempts = cloudMapMaxAttempts
				})
			})
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("cloudmap: load aws config: %w", err)
	}
	return &CloudMap{cfg: cfg, client: sd.NewFromConfig(awsCfg)}, nil
}

// Discover satisfies cluster.SeedProvider. It registers this node on
// first call and returns the peer list on every call.
func (c *CloudMap) Discover() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cloudMapAPITimeout)
	defer cancel()

	if err := c.ensureRegistered(ctx); err != nil {
		return nil, err
	}
	return c.discoverPeers(ctx)
}

// Shutdown deregisters the instance from Cloud Map. Safe to call even
// if registration never succeeded.
func (c *CloudMap) Shutdown(ctx context.Context) error {
	c.mu.Lock()
	serviceID := c.serviceID
	registered := c.registered
	c.mu.Unlock()

	if !registered || serviceID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, cloudMapAPITimeout)
	defer cancel()
	_, err := c.client.DeregisterInstance(ctx, &sd.DeregisterInstanceInput{
		InstanceId: awssdk.String(c.cfg.InstanceID),
		ServiceId:  awssdk.String(serviceID),
	})
	return err
}

// ensureRegistered resolves the service ID and registers this instance
// exactly once across the lifetime of the provider. If any step fails
// the flag stays false and the next Discover call retries.
func (c *CloudMap) ensureRegistered(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.registered {
		return nil
	}
	if c.serviceID == "" {
		nsID, err := c.namespaceID(ctx)
		if err != nil {
			return err
		}
		serviceID, err := c.serviceIDFor(ctx, nsID)
		if err != nil {
			return err
		}
		c.serviceID = serviceID
	}
	if err := c.register(ctx); err != nil {
		return err
	}
	c.registered = true
	return nil
}

// Note: ListNamespaces is not paginated. Accounts with more than 100
// namespaces may miss the target on the first page; revisit before
// production use.
func (c *CloudMap) namespaceID(ctx context.Context) (string, error) {
	out, err := c.client.ListNamespaces(ctx, &sd.ListNamespacesInput{})
	if err != nil {
		return "", fmt.Errorf("cloudmap: list namespaces: %w", err)
	}
	for _, n := range out.Namespaces {
		if awssdk.ToString(n.Name) == c.cfg.Namespace {
			return awssdk.ToString(n.Id), nil
		}
	}
	return "", fmt.Errorf("cloudmap: namespace %q not found", c.cfg.Namespace)
}

func (c *CloudMap) serviceIDFor(ctx context.Context, nsID string) (string, error) {
	out, err := c.client.ListServices(ctx, &sd.ListServicesInput{
		Filters: []sdtypes.ServiceFilter{{
			Name:      sdtypes.ServiceFilterNameNamespaceId,
			Values:    []string{nsID},
			Condition: sdtypes.FilterConditionEq,
		}},
	})
	if err != nil {
		return "", fmt.Errorf("cloudmap: list services: %w", err)
	}
	for _, s := range out.Services {
		if awssdk.ToString(s.Name) == c.cfg.Service {
			return awssdk.ToString(s.Id), nil
		}
	}
	return "", fmt.Errorf("cloudmap: service %q not found in namespace %q", c.cfg.Service, c.cfg.Namespace)
}

func (c *CloudMap) register(ctx context.Context) error {
	ip := c.cfg.AdvertiseIP
	if ip == "" {
		detected, err := localIPv4()
		if err != nil {
			return fmt.Errorf("cloudmap: detect local ip: %w", err)
		}
		ip = detected
	}
	_, err := c.client.RegisterInstance(ctx, &sd.RegisterInstanceInput{
		InstanceId: awssdk.String(c.cfg.InstanceID),
		ServiceId:  awssdk.String(c.serviceID),
		Attributes: map[string]string{
			"AWS_INSTANCE_IPV4": ip,
			"AWS_INSTANCE_PORT": strconv.Itoa(c.cfg.Port),
		},
	})
	if err != nil {
		return fmt.Errorf("cloudmap: register instance: %w", err)
	}
	return nil
}

func (c *CloudMap) discoverPeers(ctx context.Context) ([]string, error) {
	out, err := c.client.DiscoverInstances(ctx, &sd.DiscoverInstancesInput{
		NamespaceName: awssdk.String(c.cfg.Namespace),
		ServiceName:   awssdk.String(c.cfg.Service),
		HealthStatus:  sdtypes.HealthStatusFilterAll,
	})
	if err != nil {
		return nil, fmt.Errorf("cloudmap: discover instances: %w", err)
	}
	seeds := make([]string, 0, len(out.Instances))
	for _, inst := range out.Instances {
		if awssdk.ToString(inst.InstanceId) == c.cfg.InstanceID {
			continue
		}
		ip := inst.Attributes["AWS_INSTANCE_IPV4"]
		port := inst.Attributes["AWS_INSTANCE_PORT"]
		if ip == "" || port == "" {
			continue
		}
		seeds = append(seeds, net.JoinHostPort(ip, port))
	}
	return seeds, nil
}

// localIPv4 returns the first non-loopback IPv4 on the host. On
// multi-NIC machines (VPN, docker0, bridges) callers should set
// AdvertiseIP explicitly.
func localIPv4() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		if v4 := ipNet.IP.To4(); v4 != nil {
			return v4.String(), nil
		}
	}
	return "", errors.New("no non-loopback IPv4 found")
}
