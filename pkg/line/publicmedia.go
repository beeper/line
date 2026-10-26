package line

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"time"

	"golang.org/x/net/idna"
)

const (
	publicMediaMaxBytes = 100*1024*1024 + 32
	publicMediaTimeout  = 45 * time.Second
)

var (
	errPublicMediaPolicy   = errors.New("unsupported LINE public media URL")
	errPublicMediaTooLarge = errors.New("LINE public media exceeds the size limit")
)

type publicMediaTransport struct {
	base *http.Transport
}

func (transport publicMediaTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	endpoint := request.URL
	port := "443"
	if endpoint.Scheme == "http" {
		port = "80"
	}
	if endpoint.Scheme != "https" && endpoint.Scheme != "http" || endpoint.Hostname() == "" || endpoint.Port() != "" && endpoint.Port() != port || endpoint.User != nil || endpoint.Fragment != "" {
		return nil, errPublicMediaPolicy
	}
	host := endpoint.Hostname()
	if _, err := netip.ParseAddr(host); err != nil {
		host, err = idna.Lookup.ToASCII(host)
		if err != nil {
			return nil, errPublicMediaPolicy
		}
	}
	addresses, err := net.DefaultResolver.LookupNetIP(request.Context(), "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("could not resolve LINE public media host")
	}
	for _, address := range addresses {
		if !publicMediaAddress(address) {
			return nil, errPublicMediaPolicy
		}
	}
	client := transport.base.Clone()
	defer client.CloseIdleConnections()
	client.DisableKeepAlives = true
	if client.TLSClientConfig == nil {
		client.TLSClientConfig = &tls.Config{}
	} else {
		client.TLSClientConfig = client.TLSClientConfig.Clone()
	}
	client.TLSClientConfig.ServerName = host
	if client.Proxy != nil {
		proxy, err := client.Proxy(request)
		if err != nil {
			return nil, err
		}
		client.Proxy = http.ProxyURL(proxy)
		if proxy != nil && proxy.Scheme == "https" && client.DialTLSContext == nil {
			dial := client.DialContext
			if dial == nil {
				dial = (&net.Dialer{}).DialContext
			}
			client.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				conn, err := dial(ctx, network, address)
				if err != nil {
					return nil, err
				}
				config := client.TLSClientConfig.Clone()
				config.ServerName = proxy.Hostname()
				config.NextProtos = nil
				return tls.Client(conn, config), nil
			}
		}
	}
	for _, address := range addresses {
		pinned := request.Clone(request.Context())
		pinned.URL.Host = net.JoinHostPort(address.String(), port)
		pinned.Host = endpoint.Host
		var response *http.Response
		response, err = client.RoundTrip(pinned)
		if err == nil {
			response.Request = request
			return response, nil
		}
		if request.Context().Err() != nil {
			return nil, request.Context().Err()
		}
	}
	return nil, err
}

func publicMediaAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return false
	}
	if netip.MustParsePrefix("64:ff9b::/96").Contains(address) {
		bytes := address.As16()
		return publicMediaAddress(netip.AddrFrom4([4]byte(bytes[12:])))
	}
	if address.Is6() && !netip.MustParsePrefix("2000::/3").Contains(address) {
		return false
	}
	for _, prefix := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20"} {
		if netip.MustParsePrefix(prefix).Contains(address) {
			return false
		}
	}
	return true
}

func (c *Client) publicMediaHTTPClient() *http.Client {
	base, ok := http.DefaultTransport.(*http.Transport)
	if obsTransport, isHTTP := c.obsHTTPClient().Transport.(*http.Transport); isHTTP {
		base, ok = obsTransport, true
	}
	if !ok {
		base = &http.Transport{}
	}
	return &http.Client{
		Timeout:   publicMediaTimeout,
		Transport: publicMediaTransport{base: base},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errPublicMediaPolicy
			}
			return nil
		},
	}
}
