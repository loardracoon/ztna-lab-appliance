// Package httpd — GeoIP lookup via APIs públicas (sem chave/cadastro).
//
// Estratégia:
//   1. Cache in-memory de 24h por IP (positivo) ou 5 min (erro/miss)
//   2. IPs privados/loopback retornam sem chamar API externa
//   3. Primário: ip-api.com (HTTP, 45 req/min free, sem chave)
//   4. Fallback: ipwho.is (HTTPS, sem chave, generoso)
//   5. Timeout curto (3s) por chamada pra não travar a renderização
//
// Cuidado: ip-api.com gratuita é HTTP-only. Em ambiente sensível,
// considere trocar pela ipwho.is como primária (HTTPS).
package httpd

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"ztna-lab/logger"
)

// GeoInfo é o resultado consolidado das duas APIs.
type GeoInfo struct {
	IP          string  `json:"ip"`
	Country     string  `json:"country,omitempty"`
	CountryCode string  `json:"country_code,omitempty"`
	Region      string  `json:"region,omitempty"`
	City        string  `json:"city,omitempty"`
	ISP         string  `json:"isp,omitempty"`
	Org         string  `json:"org,omitempty"`
	ASN         string  `json:"asn,omitempty"`
	Latitude    float64 `json:"latitude,omitempty"`
	Longitude   float64 `json:"longitude,omitempty"`
	Source      string  `json:"source,omitempty"`  // qual API respondeu
	Error       string  `json:"error,omitempty"`   // se nenhuma respondeu
	Private     bool    `json:"private,omitempty"` // RFC1918, loopback etc.
}

type geoCacheEntry struct {
	info    *GeoInfo
	fetched time.Time
	ok      bool
}

var (
	geoMu    sync.RWMutex
	geoCache = map[string]geoCacheEntry{}

	geoTTLOK  = 24 * time.Hour
	geoTTLBad = 5 * time.Minute

	geoHTTPClient = &http.Client{Timeout: 3 * time.Second}
)

// LookupGeo é a função pública usada pelo handler do inspector.
func LookupGeo(ip string) *GeoInfo {
	if ip == "" {
		return &GeoInfo{Error: "no IP"}
	}

	if isPrivateIP(ip) {
		return &GeoInfo{IP: ip, Private: true, Source: "local"}
	}

	geoMu.RLock()
	entry, hit := geoCache[ip]
	geoMu.RUnlock()
	if hit {
		ttl := geoTTLBad
		if entry.ok {
			ttl = geoTTLOK
		}
		if time.Since(entry.fetched) < ttl {
			return entry.info
		}
	}

	info := fetchIPAPI(ip)
	if info == nil || info.Error != "" {
		fb := fetchIPWho(ip)
		if fb != nil && fb.Error == "" {
			info = fb
		} else if info == nil {
			info = &GeoInfo{IP: ip, Error: "all geo APIs failed"}
		}
	}

	geoMu.Lock()
	geoCache[ip] = geoCacheEntry{
		info:    info,
		fetched: time.Now(),
		ok:      info.Error == "",
	}
	geoMu.Unlock()

	return info
}

// fetchIPAPI consulta a primária: ip-api.com.
func fetchIPAPI(ip string) *GeoInfo {
	url := fmt.Sprintf("http://ip-api.com/json/%s?fields=status,message,country,countryCode,regionName,city,isp,org,as,query,lat,lon", ip)
	resp, err := geoHTTPClient.Get(url)
	if err != nil {
		logger.Log("HTTP", fmt.Sprintf("geo ip-api error for %s: %v", ip, err))
		return nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
	var r struct {
		Status      string  `json:"status"`
		Message     string  `json:"message"`
		Country     string  `json:"country"`
		CountryCode string  `json:"countryCode"`
		RegionName  string  `json:"regionName"`
		City        string  `json:"city"`
		ISP         string  `json:"isp"`
		Org         string  `json:"org"`
		AS          string  `json:"as"`
		Query       string  `json:"query"`
		Lat         float64 `json:"lat"`
		Lon         float64 `json:"lon"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil
	}
	if r.Status != "success" {
		return &GeoInfo{IP: ip, Error: r.Message, Source: "ip-api.com"}
	}
	logger.Log("HTTP", fmt.Sprintf("geo %s -> %s, %s (ip-api)", ip, r.City, r.CountryCode))
	return &GeoInfo{
		IP:          r.Query,
		Country:     r.Country,
		CountryCode: r.CountryCode,
		Region:      r.RegionName,
		City:        r.City,
		ISP:         r.ISP,
		Org:         r.Org,
		ASN:         r.AS,
		Latitude:    r.Lat,
		Longitude:   r.Lon,
		Source:      "ip-api.com",
	}
}

// fetchIPWho consulta o fallback: ipwho.is (HTTPS, sem chave).
func fetchIPWho(ip string) *GeoInfo {
	url := fmt.Sprintf("https://ipwho.is/%s", ip)
	resp, err := geoHTTPClient.Get(url)
	if err != nil {
		logger.Log("HTTP", fmt.Sprintf("geo ipwho.is error for %s: %v", ip, err))
		return nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
	var r struct {
		Success     bool    `json:"success"`
		Message     string  `json:"message"`
		IP          string  `json:"ip"`
		Country     string  `json:"country"`
		CountryCode string  `json:"country_code"`
		Region      string  `json:"region"`
		City        string  `json:"city"`
		Latitude    float64 `json:"latitude"`
		Longitude   float64 `json:"longitude"`
		Connection  struct {
			ISP    string `json:"isp"`
			Org    string `json:"org"`
			Domain string `json:"domain"`
			ASN    int    `json:"asn"`
		} `json:"connection"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil
	}
	if !r.Success {
		return &GeoInfo{IP: ip, Error: r.Message, Source: "ipwho.is"}
	}
	asn := ""
	if r.Connection.ASN != 0 {
		asn = fmt.Sprintf("AS%d", r.Connection.ASN)
	}
	logger.Log("HTTP", fmt.Sprintf("geo %s -> %s, %s (ipwho)", ip, r.City, r.CountryCode))
	return &GeoInfo{
		IP:          r.IP,
		Country:     r.Country,
		CountryCode: r.CountryCode,
		Region:      r.Region,
		City:        r.City,
		ISP:         r.Connection.ISP,
		Org:         r.Connection.Org,
		ASN:         asn,
		Latitude:    r.Latitude,
		Longitude:   r.Longitude,
		Source:      "ipwho.is",
	}
}

// isPrivateIP detecta loopback, link-local e ranges RFC1918/ULA.
func isPrivateIP(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, cidr := range []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16",
		"fc00::/7",
		"100.64.0.0/10", // CGNAT (RFC6598)
	} {
		_, ipNet, _ := net.ParseCIDR(cidr)
		if ipNet.Contains(ip) {
			return true
		}
	}
	return false
}
