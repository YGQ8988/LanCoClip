// Package lanaddr picks the LAN address a phone has to use to reach the service.
//
// It is deliberately dependency-free: the service keeps only one implementation
// of "which of my addresses is reachable from the phone", and both the server
// (main) and its tests share it.
package lanaddr

import (
	"net"
	"strings"
)

// virtualInterfacePrefixes lists interface name prefixes that are NOT the
// physical LAN NIC: VPN tunnels, bridges, VM/container networks, AirDrop etc.
// Advertising one of these as the access address is useless for phones.
var virtualInterfacePrefixes = []string{
	"utun", "ipsec", "ppp", "tun", "tap", "awdl", "llw", "anpi", "ap",
	"bridge", "vmnet", "vboxnet", "docker", "veth", "gpd", "lo",
}

// IsVirtualInterface reports whether name looks like a tunnel/bridge/virtual NIC.
func IsVirtualInterface(name string) bool {
	n := strings.ToLower(name)
	for _, p := range virtualInterfacePrefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// LocalIPSet returns every non-loopback IPv4 address of this machine.
func LocalIPSet() map[string]bool {
	set := map[string]bool{}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return set
	}
	for _, addr := range addrs {
		ipnet, ok := addr.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		if v4 := ipnet.IP.To4(); v4 != nil {
			set[v4.String()] = true
		}
	}
	return set
}

// FirstIPv4 returns the first usable (non-link-local) IPv4 address of an interface.
func FirstIPv4(ifi net.Interface) string {
	addrs, err := ifi.Addrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		ipnet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		v4 := ipnet.IP.To4()
		if v4 == nil || v4.IsLinkLocalUnicast() {
			continue
		}
		return v4.String()
	}
	return ""
}

// BestLocalIP picks the address phones can reach.
//
// Priority: en0 (网卡1, the primary physical NIC) → any other physical NIC →
// any address at all, including virtual NICs (last resort, e.g. VPN-only).
func BestLocalIP() string {
	ifs, err := net.Interfaces()
	if err != nil {
		return "127.0.0.1"
	}

	var fallback string
	for _, ifi := range ifs {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		if IsVirtualInterface(ifi.Name) {
			continue
		}
		ip := FirstIPv4(ifi)
		if ip == "" {
			continue
		}
		if ifi.Name == "en0" {
			return ip
		}
		if fallback == "" {
			fallback = ip
		}
	}
	if fallback != "" {
		return fallback
	}

	// Last resort: accept virtual interfaces too.
	for _, ifi := range ifs {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		if ip := FirstIPv4(ifi); ip != "" {
			return ip
		}
	}
	return "127.0.0.1"
}
