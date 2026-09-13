package connectivity

import (
	"errors"
	"net"
	"sort"
	"strconv"

	"mesh.local/agent/internal/control"
)

const maxDirectCandidates = 8

// DiscoverLAN returns only private IPv4 addresses for the selected listener.
// Loopback, public, link-local, multicast, and unspecified addresses are never
// published through the controller as LAN candidates.
func DiscoverLAN(listen string) ([]control.DirectCandidate, error) {
	host, portText, err := net.SplitHostPort(listen)
	if err != nil {
		return nil, errors.New("direct LAN listener must be a host:port address")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1024 || port > 65535 {
		return nil, errors.New("direct LAN listener port must be between 1024 and 65535")
	}
	if host != "" && host != "0.0.0.0" {
		ip := net.ParseIP(host)
		if !privateIPv4(ip) {
			return nil, errors.New("direct LAN listener must use a private IPv4 address or 0.0.0.0")
		}
		return []control.DirectCandidate{{Transport: "tcp", Host: ip.String(), Port: port}}, nil
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, errors.New("cannot inspect LAN interfaces")
	}
	var ips []net.IP
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, addressErr := iface.Addrs()
		if addressErr != nil {
			continue
		}
		for _, address := range addresses {
			ip, _, parseErr := net.ParseCIDR(address.String())
			if parseErr == nil {
				ips = append(ips, ip)
			}
		}
	}
	return candidatesForIPs(ips, port), nil
}

func candidatesForIPs(ips []net.IP, port int) []control.DirectCandidate {
	unique := make(map[string]struct{})
	for _, ip := range ips {
		if privateIPv4(ip) {
			unique[ip.String()] = struct{}{}
		}
	}
	hosts := make([]string, 0, len(unique))
	for host := range unique {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	if len(hosts) > maxDirectCandidates {
		hosts = hosts[:maxDirectCandidates]
	}
	candidates := make([]control.DirectCandidate, 0, len(hosts))
	for _, host := range hosts {
		candidates = append(candidates, control.DirectCandidate{Transport: "tcp", Host: host, Port: port})
	}
	return candidates
}

func privateIPv4(ip net.IP) bool {
	v4 := ip.To4()
	return v4 != nil && (v4[0] == 10 || v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31 || v4[0] == 192 && v4[1] == 168)
}
