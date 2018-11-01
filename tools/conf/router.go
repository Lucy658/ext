package conf

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"v2ray.com/core/app/router"
	"v2ray.com/core/common/net"
	"v2ray.com/ext/sysio"

	"github.com/golang/protobuf/proto"
)

type RouterRulesConfig struct {
	RuleList       []json.RawMessage `json:"rules"`
	DomainStrategy string            `json:"domainStrategy"`
}

type RouterConfig struct {
	Settings *RouterRulesConfig `json:"settings"`
}

func (c *RouterConfig) Build() (*router.Config, error) {
	if c.Settings == nil {
		return nil, newError("Router settings is not specified.")
	}
	config := new(router.Config)

	settings := c.Settings
	config.DomainStrategy = router.Config_AsIs
	config.Rule = make([]*router.RoutingRule, len(settings.RuleList))
	domainStrategy := strings.ToLower(settings.DomainStrategy)
	switch domainStrategy {
	case "alwaysip":
		config.DomainStrategy = router.Config_UseIp
	case "ipifnonmatch":
		config.DomainStrategy = router.Config_IpIfNonMatch
	case "ipondemand":
		config.DomainStrategy = router.Config_IpOnDemand
	}
	for idx, rawRule := range settings.RuleList {
		rule, err := ParseRule(rawRule)
		if err != nil {
			return nil, err
		}
		config.Rule[idx] = rule
	}
	return config, nil
}

type RouterRule struct {
	Type        string `json:"type"`
	OutboundTag string `json:"outboundTag"`
}

func ParseIP(s string) (*router.CIDR, error) {
	var addr, mask string
	i := strings.Index(s, "/")
	if i < 0 {
		addr = s
	} else {
		addr = s[:i]
		mask = s[i+1:]
	}
	ip := net.ParseAddress(addr)
	switch ip.Family() {
	case net.AddressFamilyIPv4:
		bits := uint32(32)
		if len(mask) > 0 {
			bits64, err := strconv.ParseUint(mask, 10, 32)
			if err != nil {
				return nil, newError("invalid network mask for router: ", mask).Base(err)
			}
			bits = uint32(bits64)
		}
		if bits > 32 {
			return nil, newError("invalid network mask for router: ", bits)
		}
		return &router.CIDR{
			Ip:     []byte(ip.IP()),
			Prefix: bits,
		}, nil
	case net.AddressFamilyIPv6:
		bits := uint32(128)
		if len(mask) > 0 {
			bits64, err := strconv.ParseUint(mask, 10, 32)
			if err != nil {
				return nil, newError("invalid network mask for router: ", mask).Base(err)
			}
			bits = uint32(bits64)
		}
		if bits > 128 {
			return nil, newError("invalid network mask for router: ", bits)
		}
		return &router.CIDR{
			Ip:     []byte(ip.IP()),
			Prefix: bits,
		}, nil
	default:
		return nil, newError("unsupported address for router: ", s)
	}
}

func loadGeoIP(country string) ([]*router.CIDR, error) {
	return loadIP("geoip.dat", country)
}

func loadIP(filename, country string) ([]*router.CIDR, error) {
	geoipBytes, err := sysio.ReadAsset(filename)
	if err != nil {
		return nil, newError("failed to open file: ", filename).Base(err)
	}
	var geoipList router.GeoIPList
	if err := proto.Unmarshal(geoipBytes, &geoipList); err != nil {
		return nil, err
	}

	for _, geoip := range geoipList.Entry {
		if geoip.CountryCode == country {
			return geoip.Cidr, nil
		}
	}

	return nil, newError("country not found: " + country)
}

func loadGeoSite(country string) ([]*router.Domain, error) {
	return loadSite("geosite.dat", country)
}

func loadSite(filename, country string) ([]*router.Domain, error) {
	geositeBytes, err := sysio.ReadAsset(filename)
	if err != nil {
		return nil, newError("failed to open file: ", filename).Base(err)
	}
	var geositeList router.GeoSiteList
	if err := proto.Unmarshal(geositeBytes, &geositeList); err != nil {
		return nil, err
	}

	for _, site := range geositeList.Entry {
		if site.CountryCode == country {
			return site.Domain, nil
		}
	}

	return nil, newError("country not found: " + country)
}

func parseDomainRule(domain string) ([]*router.Domain, error) {
	if strings.HasPrefix(domain, "geosite:") {
		country := strings.ToUpper(domain[8:])
		domains, err := loadGeoSite(country)
		if err != nil {
			return nil, newError("failed to load geosite: ", country).Base(err)
		}
		return domains, nil
	}

	if strings.HasPrefix(domain, "ext:") {
		kv := strings.Split(domain[4:], ":")
		if len(kv) != 2 {
			return nil, newError("invalid external resource: ", domain)
		}
		filename := kv[0]
		country := strings.ToUpper(kv[1])
		domains, err := loadSite(filename, country)
		if err != nil {
			return nil, newError("failed to load external sites: ", country, " from ", filename).Base(err)
		}
		return domains, nil
	}

	domainRule := new(router.Domain)
	switch {
	case strings.HasPrefix(domain, "regexp:"):
		domainRule.Type = router.Domain_Regex
		domainRule.Value = domain[7:]
	case strings.HasPrefix(domain, "domain:"):
		domainRule.Type = router.Domain_Domain
		domainRule.Value = domain[7:]
	case strings.HasPrefix(domain, "full:"):
		domainRule.Type = router.Domain_Full
		domainRule.Value = domain[5:]
	default:
		domainRule.Type = router.Domain_Plain
		domainRule.Value = domain
	}
	return []*router.Domain{domainRule}, nil
}

func toCidrList(ips StringList) ([]*router.GeoIP, error) {
	var geoipList []*router.GeoIP
	var customCidrs router.CIDRList

	for _, ip := range ips {
		if strings.HasPrefix(ip, "geoip:") {
			country := ip[6:]
			geoip, err := loadGeoIP(strings.ToUpper(country))
			if err != nil {
				return nil, newError("failed to load GeoIP: ", country).Base(err)
			}

			cidrList := router.CIDRList(geoip)
			sort.Sort(&cidrList)
			geoipList = append(geoipList, &router.GeoIP{
				CountryCode: strings.ToUpper(country),
				Cidr:        cidrList,
			})
			continue
		}

		if strings.HasPrefix(ip, "ext:") {
			kv := strings.Split(ip[4:], ":")
			if len(kv) != 2 {
				return nil, newError("invalid external resource: ", ip)
			}

			filename := kv[0]
			country := kv[1]
			geoip, err := loadGeoIP(strings.ToUpper(country))
			if err != nil {
				return nil, newError("failed to load IPs: ", country, " from ", filename).Base(err)
			}

			cidrList := router.CIDRList(geoip)
			sort.Sort(&cidrList)
			geoipList = append(geoipList, &router.GeoIP{
				CountryCode: strings.ToUpper(filename + "_" + country),
				Cidr:        cidrList,
			})

			continue
		}

		ipRule, err := ParseIP(ip)
		if err != nil {
			return nil, newError("invalid IP: ", ip).Base(err)
		}
		customCidrs = append(customCidrs, ipRule)
	}

	if len(customCidrs) > 0 {
		sort.Sort(&customCidrs)
		geoipList = append(geoipList, &router.GeoIP{
			Cidr: customCidrs,
		})
	}

	return geoipList, nil
}

func parseFieldRule(msg json.RawMessage) (*router.RoutingRule, error) {
	type RawFieldRule struct {
		RouterRule
		Domain     *StringList  `json:"domain"`
		IP         *StringList  `json:"ip"`
		Port       *PortRange   `json:"port"`
		Network    *NetworkList `json:"network"`
		SourceIP   *StringList  `json:"source"`
		User       *StringList  `json:"user"`
		InboundTag *StringList  `json:"inboundTag"`
		Protocols  *StringList  `json:"protocol"`
	}
	rawFieldRule := new(RawFieldRule)
	err := json.Unmarshal(msg, rawFieldRule)
	if err != nil {
		return nil, err
	}

	rule := new(router.RoutingRule)
	rule.Tag = rawFieldRule.OutboundTag

	if rawFieldRule.Domain != nil {
		for _, domain := range *rawFieldRule.Domain {
			rules, err := parseDomainRule(domain)
			if err != nil {
				return nil, newError("failed to parse domain rule: ", domain).Base(err)
			}
			rule.Domain = append(rule.Domain, rules...)
		}
	}

	if rawFieldRule.IP != nil {
		geoipList, err := toCidrList(*rawFieldRule.IP)
		if err != nil {
			return nil, err
		}
		rule.Geoip = geoipList
	}

	if rawFieldRule.Port != nil {
		rule.PortRange = rawFieldRule.Port.Build()
	}

	if rawFieldRule.Network != nil {
		rule.NetworkList = rawFieldRule.Network.Build()
	}

	if rawFieldRule.SourceIP != nil {
		geoipList, err := toCidrList(*rawFieldRule.SourceIP)
		if err != nil {
			return nil, err
		}
		rule.SourceGeoip = geoipList
	}

	if rawFieldRule.User != nil {
		for _, s := range *rawFieldRule.User {
			rule.UserEmail = append(rule.UserEmail, s)
		}
	}

	if rawFieldRule.InboundTag != nil {
		for _, s := range *rawFieldRule.InboundTag {
			rule.InboundTag = append(rule.InboundTag, s)
		}
	}

	if rawFieldRule.Protocols != nil {
		for _, s := range *rawFieldRule.Protocols {
			rule.Protocol = append(rule.Protocol, s)
		}
	}

	return rule, nil
}

func ParseRule(msg json.RawMessage) (*router.RoutingRule, error) {
	rawRule := new(RouterRule)
	err := json.Unmarshal(msg, rawRule)
	if err != nil {
		return nil, newError("invalid router rule").Base(err)
	}
	if rawRule.Type == "field" {
		fieldrule, err := parseFieldRule(msg)
		if err != nil {
			return nil, newError("invalid field rule").Base(err)
		}
		return fieldrule, nil
	}
	if rawRule.Type == "chinaip" {
		chinaiprule, err := parseChinaIPRule(msg)
		if err != nil {
			return nil, newError("invalid chinaip rule").Base(err)
		}
		return chinaiprule, nil
	}
	if rawRule.Type == "chinasites" {
		chinasitesrule, err := parseChinaSitesRule(msg)
		if err != nil {
			return nil, newError("invalid chinasites rule").Base(err)
		}
		return chinasitesrule, nil
	}
	return nil, newError("unknown router rule type: ", rawRule.Type)
}

func parseChinaIPRule(data []byte) (*router.RoutingRule, error) {
	rawRule := new(RouterRule)
	err := json.Unmarshal(data, rawRule)
	if err != nil {
		return nil, newError("invalid router rule").Base(err)
	}
	chinaIPs, err := loadGeoIP("CN")
	if err != nil {
		return nil, newError("failed to load geoip:cn").Base(err)
	}
	return &router.RoutingRule{
		Tag:  rawRule.OutboundTag,
		Cidr: chinaIPs,
	}, nil
}

func parseChinaSitesRule(data []byte) (*router.RoutingRule, error) {
	rawRule := new(RouterRule)
	err := json.Unmarshal(data, rawRule)
	if err != nil {
		return nil, newError("invalid router rule").Base(err).AtError()
	}
	domains, err := loadGeoSite("CN")
	if err != nil {
		return nil, newError("failed to load geosite:cn.").Base(err)
	}
	return &router.RoutingRule{
		Tag:    rawRule.OutboundTag,
		Domain: domains,
	}, nil
}
