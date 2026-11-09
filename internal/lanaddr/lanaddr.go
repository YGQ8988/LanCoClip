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
//
// These are the BSD/Linux names (utun0, bridge100, vmnet8, docker0 …).
var virtualInterfacePrefixes = []string{
	"utun", "ipsec", "ppp", "tun", "tap", "awdl", "llw", "anpi", "ap",
	"bridge", "vmnet", "vboxnet", "docker", "veth", "gpd",
}

// virtualInterfaceMarkers are substring matches, needed because Windows hands
// Go the *friendly* adapter name ("vEthernet (WSL)", "VMware Network Adapter
// VMnet1", "Local Area Connection* 1", "以太网 2") instead of a device name, so
// the prefix table above matches none of them. Without this list a Hyper-V /
// VMware / Tailscale adapter can win the "best address" vote and the QR code
// points at an address the phone can never reach.
var virtualInterfaceMarkers = []string{
	"vethernet", "hyper-v", "hyperv", "virtualbox", "vbox", "vmware", "vmnet",
	"virtual adapter", "虚拟", "docker", "veth", "virbr", "br-", "bridge",
	"tap-windows", "wintun", "wireguard", "tailscale", "zerotier", "nordlynx",
	"openvpn", "bluetooth", "蓝牙", "pseudo", "loopback",
	// Windows 的 Wi-Fi Direct 虚拟网卡叫 "Local Area Connection* 1"（中文
	// 「本地连接* 1」）。注意只匹配带星号的这种：不带星号的 "Local Area
	// Connection" 才是真正的有线网卡。
	"local area connection*", "本地连接*",
}

// IsVirtualInterface reports whether name looks like a tunnel/bridge/virtual NIC.
func IsVirtualInterface(name string) bool {
	n := strings.ToLower(name)
	// "lo"/"lo0" 单独判断，不能放进前缀表：Windows 的有线网卡叫
	// "Local Area Connection"，任何 "lo" 前缀匹配都会把真实网卡判成 loopback。
	if n == "lo" || strings.HasPrefix(n, "lo0") {
		return true
	}
	for _, p := range virtualInterfacePrefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	for _, m := range virtualInterfaceMarkers {
		if strings.Contains(n, m) {
			return true
		}
	}
	return false
}

// LocalIPSet returns every non-loopback address of this machine, both IPv4 and
// IPv6, without brackets or zone suffixes (the form net/http puts in
// r.Host / r.RemoteAddr). Virtual NICs are included on purpose: a phone routed
// over a VPN / Hyper-V network is still "local" for the Host whitelist, and
// excluding those addresses would turn a working setup into a 403.
func LocalIPSet() map[string]bool {
	set := map[string]bool{}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return set
	}
	for _, addr := range addrs {
		ipnet, ok := addr.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.IsUnspecified() {
			continue
		}
		if v4 := ipnet.IP.To4(); v4 != nil {
			set[v4.String()] = true
			continue
		}
		if v6 := ipnet.IP.To16(); v6 != nil {
			set[v6.String()] = true
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

// FirstIPv6 returns the first routable IPv6 address of an interface. Link-local
// (fe80::/10) is skipped: it needs a zone id ("fe80::1%en0") that a QR code
// cannot carry, and no phone would be able to use it.
func FirstIPv6(ifi net.Interface) string {
	addrs, err := ifi.Addrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		ipnet, ok := addr.(*net.IPNet)
		if !ok || ipnet.IP.To4() != nil {
			continue
		}
		v6 := ipnet.IP.To16()
		if v6 == nil || !v6.IsGlobalUnicast() || v6.IsLinkLocalUnicast() {
			continue
		}
		return v6.String()
	}
	return ""
}

// ifaceScore ranks a candidate address. Higher wins. The bias towards private
// IPv4 matters on machines with several physical NICs (USB-Ethernet + Wi-Fi):
// the phone-reachable one is almost always the private-range one.
func ifaceScore(name string, ip net.IP) int {
	score := 40
	switch {
	case strings.EqualFold(name, "en0"):
		score += 60 // macOS 主网卡
	case looksWireless(name):
		score += 50
	case looksWired(name):
		score += 30
	}
	if ip != nil && ip.To4() != nil && ip.IsPrivate() {
		score += 25
	}
	return score
}

func looksWireless(name string) bool {
	n := strings.ToLower(name)
	if strings.HasPrefix(n, "wl") { // wlan0, wlp3s0
		return true
	}
	for _, m := range []string{"wi-fi", "wifi", "wlan", "airport", "无线"} {
		if strings.Contains(n, m) {
			return true
		}
	}
	return false
}

func looksWired(name string) bool {
	n := strings.ToLower(name)
	if strings.HasPrefix(n, "en") || strings.HasPrefix(n, "eth") { // en1, eth0
		return true
	}
	for _, m := range []string{"ethernet", "以太网", "local area connection"} {
		if strings.Contains(n, m) {
			return true
		}
	}
	return false
}

// usable reports whether an interface can carry LAN traffic at all.
func usable(ifi net.Interface) bool {
	if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
		return false
	}
	return true
}

// pick returns the best address from all interfaces. wantVirtual decides which
// pool to look at; scoring is identical so the fallback pass still prefers a
// private-range address over a link-local-ish one.
func pick(wantVirtual bool, addr func(net.Interface) string) (string, int) {
	best, bestScore := "", -1
	ifs, err := net.Interfaces()
	if err != nil {
		return "", -1
	}
	for _, ifi := range ifs {
		if !usable(ifi) || IsVirtualInterface(ifi.Name) != wantVirtual {
			continue
		}
		ip := addr(ifi)
		if ip == "" {
			continue
		}
		// 严格大于：网卡按 index 有序，同分时保留序号更小的那块，结果稳定。
		if s := ifaceScore(ifi.Name, net.ParseIP(ip)); s > bestScore {
			best, bestScore = ip, s
		}
	}
	return best, bestScore
}

// BestLocalIP picks the IPv4 address phones can reach.
//
// Priority: physical NIC (en0 / Wi-Fi / Ethernet, private range first) → any
// virtual NIC (last resort, e.g. a VPN-only machine) → loopback.
func BestLocalIP() string {
	if ip, _ := pick(false, FirstIPv4); ip != "" {
		return ip
	}
	if ip, _ := pick(true, FirstIPv4); ip != "" {
		return ip
	}
	return "127.0.0.1"
}

// BestLocalHost returns the host to embed in a URL. It is BestLocalIP for the
// normal IPv4 case, and falls back to a bracketed IPv6 address on machines
// that only have IPv6 connectivity — previously such a machine advertised
// 127.0.0.1, so the QR code could never work from a phone.
func BestLocalHost() string {
	if ip, _ := pick(false, FirstIPv4); ip != "" {
		return ip
	}
	if ip, _ := pick(true, FirstIPv4); ip != "" {
		return ip
	}
	if v6, _ := pick(false, FirstIPv6); v6 != "" {
		return "[" + v6 + "]"
	}
	return "127.0.0.1"
}
