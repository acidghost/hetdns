// Package source discovers and validates public IP addresses.
package source

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/acidghost/hetdns/internal/config"
)

// Family is an address family.
type Family string

const (
	IPv4 Family = "ipv4"
	IPv6 Family = "ipv6"
)

// Source obtains an address from one configured source.
type Source interface {
	ID() string
	Family() Family
	Fetch(context.Context) (netip.Addr, error)
}

// ParseAddress accepts exactly one address, checks its family, and applies
// the public-address policy.
func ParseAddress(output []byte, family Family, allowNonPublic bool) (netip.Addr, error) {
	value := strings.Trim(string(output), " \t\r\n\v\f")
	if value == "" {
		return netip.Addr{}, errors.New("source returned an empty result")
	}
	address, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Addr{}, errors.New(
			"source result is not exactly one IP address",
		)
	}

	address = address.Unmap()
	wrongFamily := family == IPv4 && !address.Is4() ||
		family == IPv6 && !address.Is6()
	if wrongFamily {
		return netip.Addr{}, fmt.Errorf(
			"source returned the wrong address family (wanted %s)",
			family,
		)
	}
	if !allowNonPublic && !isPublic(address) {
		return netip.Addr{}, errors.New("source returned a non-public address")
	}

	return address, nil
}

var nonPublicSpecialUse = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // current network
	netip.MustParsePrefix("100.64.0.0/10"),   // shared address space
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // documentation
	netip.MustParsePrefix("192.88.99.0/24"),  // deprecated relay anycast
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // documentation
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved
	netip.MustParsePrefix("100::/64"),        // discard-only
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
}

func isPublic(address netip.Addr) bool {
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() ||
		address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() ||
		address.IsUnspecified() {
		return false
	}
	for _, prefix := range nonPublicSpecialUse {
		if prefix.Contains(address) {
			return false
		}
	}

	return true
}

// New constructs all configured sources in stable ID order.
func New(sources map[string]config.Source, userAgent string) ([]Source, error) {
	ids := make([]string, 0, len(sources))
	for id := range sources {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	result := make([]Source, 0, len(ids))
	for _, id := range ids {
		spec := sources[id]
		var item Source
		var err error
		switch spec.Type {
		case "http":
			item, err = NewHTTP(id, spec, userAgent)
		case "command":
			item, err = NewCommand(id, spec)
		default:
			err = fmt.Errorf("unsupported source type %q", spec.Type)
		}
		if err != nil {
			return nil, fmt.Errorf("source %s: %w", id, err)
		}

		result = append(result, item)
	}

	return result, nil
}

// CloseIdleConnections releases pooled HTTP source connections during shutdown.
func CloseIdleConnections(sources []Source) {
	for _, item := range sources {
		if closer, ok := item.(interface{ CloseIdleConnections() }); ok {
			closer.CloseIdleConnections()
		}
	}
}

func familyOf(value string) Family {
	if value == string(IPv6) {
		return IPv6
	}
	return IPv4
}
