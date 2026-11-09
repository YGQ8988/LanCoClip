package lanaddr

import (
	"net"
	"strings"
	"testing"
)

// Windows hands Go the *friendly* adapter name, so the historic prefix table
// (utun/bridge/docker …) matched none of the virtual adapters there and a
// Hyper-V / VMware / VPN adapter could win the "best address" vote. These are
// the names that actually show up in `ipconfig` / the adapter list.
func TestIsVirtualInterfaceWindowsNames(t *testing.T) {
	virtual := []string{
		"vEthernet (WSL)",
		"vEthernet (Default Switch)",
		"VMware Network Adapter VMnet1",
		"VirtualBox Host-Only Network",
		"Hyper-V Virtual Ethernet Adapter",
		"Tailscale",
		"ZeroTier One [1c:0d:…]",
		"TAP-Windows Adapter V9",
		"WireGuard Tunnel",
		"Local Area Connection* 1",
		"本地连接* 10",
		"蓝牙网络连接",
	}
	for _, n := range virtual {
		if !IsVirtualInterface(n) {
			t.Errorf("%q should be treated as virtual", n)
		}
	}
}

func TestIsVirtualInterfacePhysicalNames(t *testing.T) {
	physical := []string{
		"en0", "en1", "eth0", "wlan0", "wlp3s0",
		"WLAN", "以太网", "Ethernet", "Wi-Fi",
		"Local Area Connection", // 真正的有线网卡（不是带星号的 Wi-Fi Direct）
	}
	for _, n := range physical {
		if IsVirtualInterface(n) {
			t.Errorf("%q should not be treated as virtual", n)
		}
	}
}

// The scoring exists so a machine with several physical NICs advertises the one
// a phone can actually reach: primary macOS NIC first, then Wi-Fi, then wired,
// with private-range addresses preferred over public ones.
func TestIfaceScoreOrdering(t *testing.T) {
	private := net.ParseIP("192.168.1.20")
	public := net.ParseIP("203.0.113.9")

	if ifaceScore("en0", private) <= ifaceScore("wlan0", private) {
		t.Error("en0 should outrank wlan0")
	}
	if ifaceScore("wlan0", private) <= ifaceScore("eth0", private) {
		t.Error("Wi-Fi should outrank wired when both are physical")
	}
	if ifaceScore("eth0", private) <= ifaceScore("eth0", public) {
		t.Error("a private-range address should outrank a public one on the same NIC")
	}
}

func TestBestLocalHostShape(t *testing.T) {
	host := BestLocalHost()
	if host == "" {
		t.Fatal("BestLocalHost returned empty")
	}
	if strings.HasPrefix(host, "[") {
		if !strings.HasSuffix(host, "]") || net.ParseIP(strings.Trim(host, "[]")) == nil {
			t.Fatalf("bracketed IPv6 host is malformed: %q", host)
		}
		return
	}
	if net.ParseIP(host) == nil {
		t.Fatalf("host is not an IP address: %q", host)
	}
}

// LocalIPSet feeds the Host whitelist and the "is this request local" check, so
// it must never contain loopback (which would make every client look local).
func TestLocalIPSetExcludesLoopback(t *testing.T) {
	for ip := range LocalIPSet() {
		if net.ParseIP(ip) == nil {
			t.Errorf("LocalIPSet contains a non-IP entry: %q", ip)
		}
		if strings.HasPrefix(ip, "127.") || ip == "::1" {
			t.Errorf("LocalIPSet contains loopback: %q", ip)
		}
		if strings.Contains(ip, "%") || strings.HasPrefix(ip, "[") {
			t.Errorf("LocalIPSet entry must be bare host form: %q", ip)
		}
	}
}
