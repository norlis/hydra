package aws

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
)

const (
	mac0 = "0e:4f:59:37:ef:4b"
	mac1 = "0e:8a:91:c3:44:39"
)

// notFoundErr mimics the SDK's 404 response error (implements HTTPStatusCode),
// used to model an optional metadata field that is absent from the instance.
type notFoundErr struct{}

func (notFoundErr) Error() string       { return "not found" }
func (notFoundErr) HTTPStatusCode() int { return 404 }

// fakeIMDSClient serves canned metadata. routes maps GetMetadataInput.Path to
// its body; an absent path returns a 404 error.
type fakeIMDSClient struct {
	routes map[string]string
}

func (f fakeIMDSClient) GetMetadata(_ context.Context, in *imds.GetMetadataInput, _ ...func(*imds.Options)) (*imds.GetMetadataOutput, error) {
	body, ok := f.routes[in.Path]
	if !ok {
		return nil, notFoundErr{}
	}
	return &imds.GetMetadataOutput{Content: io.NopCloser(strings.NewReader(body))}, nil
}

func twoNICRoutes() map[string]string {
	return map[string]string{
		"/network/interfaces/macs/":                           mac0 + "/\n" + mac1 + "/\n",
		"/network/interfaces/macs/" + mac0 + "/local-ipv4s":   "172.18.5.239",
		"/network/interfaces/macs/" + mac0 + "/device-number": "0",
		"/network/interfaces/macs/" + mac0 + "/interface-id":  "eni-aaa",
		"/network/interfaces/macs/" + mac1 + "/local-ipv4s":   "172.18.5.113",
		"/network/interfaces/macs/" + mac1 + "/device-number": "1",
		"/network/interfaces/macs/" + mac1 + "/interface-id":  "eni-bbb",
	}
}

func newTestProvider(routes map[string]string, links func() (map[string]string, error)) *IMDSProvider {
	return &IMDSProvider{
		basePort:       3128,
		log:            slog.New(slog.DiscardHandler),
		client:         fakeIMDSClient{routes: routes},
		linkNamesByMAC: links,
	}
}

func TestDiscover_KernelNameFromMAC(t *testing.T) {
	t.Parallel()
	links := func() (map[string]string, error) {
		return map[string]string{mac0: "ens5", mac1: "ens6"}, nil
	}
	ifaces, err := newTestProvider(twoNICRoutes(), links).Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(ifaces) != 2 {
		t.Fatalf("want 2 interfaces, got %d", len(ifaces))
	}
	if ifaces[0].Name != "ens5" || ifaces[0].PhysicalID != "eni-aaa" ||
		ifaces[0].PrivateIP != "172.18.5.239" || ifaces[0].ServicePort != 3128 {
		t.Errorf("iface[0] = %+v", ifaces[0])
	}
	if ifaces[1].Name != "ens6" || ifaces[1].PhysicalID != "eni-bbb" || ifaces[1].ServicePort != 3129 {
		t.Errorf("iface[1] = %+v", ifaces[1])
	}
}

func TestDiscover_FallbackWhenNoMACMatch(t *testing.T) {
	t.Parallel()
	empty := func() (map[string]string, error) { return map[string]string{}, nil }
	ifaces, err := newTestProvider(twoNICRoutes(), empty).Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if ifaces[0].Name != "eth0" || ifaces[1].Name != "eth1" {
		t.Errorf("want eth0/eth1 fallback, got %q/%q", ifaces[0].Name, ifaces[1].Name)
	}
}

func TestDiscover_ResolverErrorFallsBack(t *testing.T) {
	t.Parallel()
	boom := func() (map[string]string, error) { return nil, io.ErrUnexpectedEOF }
	ifaces, err := newTestProvider(twoNICRoutes(), boom).Discover()
	if err != nil {
		t.Fatalf("Discover should not fail on resolver error: %v", err)
	}
	if ifaces[0].Name != "eth0" || ifaces[1].Name != "eth1" {
		t.Errorf("want eth0/eth1 fallback, got %q/%q", ifaces[0].Name, ifaces[1].Name)
	}
}

func TestDiscover_MissingInterfaceIDIsEmpty(t *testing.T) {
	t.Parallel()
	routes := twoNICRoutes()
	delete(routes, "/network/interfaces/macs/"+mac0+"/interface-id")
	links := func() (map[string]string, error) {
		return map[string]string{mac0: "ens5", mac1: "ens6"}, nil
	}
	ifaces, err := newTestProvider(routes, links).Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if ifaces[0].PhysicalID != "" {
		t.Errorf("want empty PhysicalID, got %q", ifaces[0].PhysicalID)
	}
}
