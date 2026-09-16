// Package sophos implements a firewall policy optimization tool for Sophos
// Firewall, on top of the Firewall Configuration REST API v1.
//
// The package is split in four layers:
//
//   - types.go    — the IPv4 firewall rule model, mirroring the API schema
//   - client.go   — REST client (list/create/update/delete/move)
//   - analyzer.go — the optimization engine, which turns an ordered rule
//     base into findings
//   - plan.go     — turns findings into API calls, with a dry run by default
//
// API reference: https://docs.sophos.com/nsg/sophos-firewall/rest-api/
// Base URL: https://<host>:<port>/api/firewall-config/v1
package sophos

// NamedRef is how the API references every other object — zones, address
// objects, services, policies, schedules. Only the name travels on the
// wire; the firewall resolves it against its own object registry.
type NamedRef struct {
	Name string `json:"name"`
}

// ZoneSet is a rule's zone match. The API models it as a oneOf: either
// {"any": true} or {"zones": [...]}. Both shapes decode into this struct,
// and omitempty reproduces them on the way out.
type ZoneSet struct {
	Any   bool       `json:"any,omitempty"`
	Zones []NamedRef `json:"zones,omitempty"`
}

// NetworkSet is a rule's source or destination network match. Same oneOf
// shape as ZoneSet, but the selected branch spreads across seven kinds of
// object, which the firewall unions together.
type NetworkSet struct {
	Any           bool       `json:"any,omitempty"`
	Countries     []NamedRef `json:"countries,omitempty"`
	CountryGroups []NamedRef `json:"countryGroups,omitempty"`
	FQDNAddresses []NamedRef `json:"fqdnAddresses,omitempty"`
	FQDNGroups    []NamedRef `json:"fqdnGroups,omitempty"`
	IPv4Addresses []NamedRef `json:"ipv4Addresses,omitempty"`
	IPv4Groups    []NamedRef `json:"ipv4Groups,omitempty"`
	MACAddresses  []NamedRef `json:"macAddresses,omitempty"`
}

// ServiceSet is a rule's service match.
type ServiceSet struct {
	Any           bool       `json:"any,omitempty"`
	Services      []NamedRef `json:"services,omitempty"`
	ServiceGroups []NamedRef `json:"serviceGroups,omitempty"`
}

// UsersOrGroups is the identity match inside UserAuthentication.
type UsersOrGroups struct {
	Any        bool       `json:"any,omitempty"`
	Users      []NamedRef `json:"users,omitempty"`
	UserGroups []NamedRef `json:"userGroups,omitempty"`
}

// UserAuthentication narrows a rule to specific users or groups.
type UserAuthentication struct {
	UsersOrGroups               *UsersOrGroups `json:"usersOrGroups,omitempty"`
	ExcludeUsersFromAccounting  bool           `json:"excludeUsersFromAccounting,omitempty"`
	WebAuthenticationForUnknown bool           `json:"webAuthenticationForUnknownUsers,omitempty"`
}

// Exclusions carve matches back out of a rule. Note that the exclusion
// sets have no "any" branch — an exclusion is always a selection.
type Exclusions struct {
	SourceZones         *ZoneSet    `json:"sourceZones,omitempty"`
	DestinationZones    *ZoneSet    `json:"destinationZones,omitempty"`
	SourceNetworks      *NetworkSet `json:"sourceNetworks,omitempty"`
	DestinationNetworks *NetworkSet `json:"destinationNetworks,omitempty"`
	ServicesOrGroups    *ServiceSet `json:"servicesOrGroups,omitempty"`
}

// HeartbeatEndpoint is one side of the Security Heartbeat match.
type HeartbeatEndpoint struct {
	BlockClientsWithNoHeartbeat bool   `json:"blockClientsWithNoHeartbeat,omitempty"`
	MinimumLevel                string `json:"minimumLevel,omitempty"` // green | yellow | noRestriction
}

// Heartbeat is the synchronizedSecurityHeartbeat object.
type Heartbeat struct {
	Source      *HeartbeatEndpoint `json:"source,omitempty"`
	Destination *HeartbeatEndpoint `json:"destination,omitempty"`
}

// SecurityFeatures is the inspection profile attached to an accept rule.
// The API drops this whole object when the action is drop or reject.
type SecurityFeatures struct {
	ApplicationBasedQosPolicy bool      `json:"applicationBasedQosPolicy,omitempty"`
	ApplicationPolicy         *NamedRef `json:"applicationPolicy,omitempty"`
	BlockQuicProtocol         bool      `json:"blockQuicProtocol,omitempty"`
	DecryptHTTPSWebProxyMode  bool      `json:"decryptHTTPSWebProxyMode,omitempty"`
	IPSPolicy                 *NamedRef `json:"ipsPolicy,omitempty"`
	ScanFTP                   bool      `json:"scanFtp,omitempty"`
	ScanHTTPAndDecryptedHTTPS bool      `json:"scanHttpAndDecryptedHttps,omitempty"`
	ScanWithNDR               bool      `json:"scanWithNdrActiveThreatIntelligence,omitempty"`
	WebCategoryBasedQosPolicy bool      `json:"webCategoryBasedQosPolicy,omitempty"`
	WebPolicy                 *NamedRef `json:"webPolicy,omitempty"`
	WebProxy                  bool      `json:"webProxy,omitempty"`
	ZeroDayProtection         bool      `json:"zeroDayProtection,omitempty"`
}

// EmailScanning is the per-protocol mail scanning toggle set.
type EmailScanning struct {
	IMAP  bool `json:"imap,omitempty"`
	IMAPS bool `json:"imaps,omitempty"`
	POP3  bool `json:"pop3,omitempty"`
	POP3S bool `json:"pop3s,omitempty"`
	SMTP  bool `json:"smtp,omitempty"`
	SMTPS bool `json:"smtps,omitempty"`
}

// QoS is the traffic shaping and DSCP marking attached to a rule.
type QoS struct {
	TrafficShapingPolicy *NamedRef `json:"trafficShapingPolicy,omitempty"`
	DSCPMarking          string    `json:"dscpMarking,omitempty"`
}

// WAFRuleRef references a web server protection policy.
type WAFRuleRef struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// Rule types and actions as the API spells them.
const (
	RuleTypeFirewall = "firewall"
	RuleTypeWAF      = "waf"

	ActionAccept = "accept"
	ActionDrop   = "drop"
	ActionReject = "reject"
)

// Rule is an IPv4 firewall rule as returned by GET /firewall/rules/ipv4.
//
// Enabled and LogTraffic are pointers on purpose: the analyzer has to tell
// "the firewall said false" from "the field was absent", and a PATCH body
// built from this struct must not resurrect defaults the caller never set.
type Rule struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	RuleType    string `json:"ruleType,omitempty"`
	Enabled     *bool  `json:"enabled,omitempty"`
	Action      string `json:"action,omitempty"`

	SourceZones         *ZoneSet    `json:"sourceZones,omitempty"`
	DestinationZones    *ZoneSet    `json:"destinationZones,omitempty"`
	SourceNetworks      *NetworkSet `json:"sourceNetworks,omitempty"`
	DestinationNetworks *NetworkSet `json:"destinationNetworks,omitempty"`
	ServicesOrGroups    *ServiceSet `json:"servicesOrGroups,omitempty"`

	UserAuthentication *UserAuthentication `json:"userAuthentication,omitempty"`
	Exclusions         *Exclusions         `json:"exclusions,omitempty"`
	Heartbeat          *Heartbeat          `json:"synchronizedSecurityHeartbeat,omitempty"`
	SecurityFeatures   *SecurityFeatures   `json:"securityFeatures,omitempty"`
	EmailScanning      *EmailScanning      `json:"emailScanning,omitempty"`
	QoS                *QoS                `json:"qos,omitempty"`
	LogTraffic         *bool               `json:"logTraffic,omitempty"`
	Schedule           *NamedRef           `json:"schedule,omitempty"`

	// WAF-type rules only.
	WAFService int         `json:"wafService,omitempty"`
	WAFRule    *WAFRuleRef `json:"wafRule,omitempty"`

	CreatedAt string `json:"createdAt,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`

	// Position and ReferenceItem are write-only: the create endpoint takes
	// them, the list endpoint never returns them.
	Position      string    `json:"position,omitempty"`
	ReferenceItem *NamedRef `json:"referenceItem,omitempty"`
}

// IsEnabled applies the API default (true) for an absent "enabled".
func (r *Rule) IsEnabled() bool {
	if r == nil {
		return false
	}
	if r.Enabled == nil {
		return true
	}
	return *r.Enabled
}

// IsLogging applies the API default (false) for an absent "logTraffic".
func (r *Rule) IsLogging() bool {
	if r == nil || r.LogTraffic == nil {
		return false
	}
	return *r.LogTraffic
}

// Ref returns the value to use as {idOrName} in a request path. The ID is
// stable across renames, so it wins when the firewall gave us one.
func (r *Rule) Ref() string {
	if r == nil {
		return ""
	}
	if r.ID != "" {
		return r.ID
	}
	return r.Name
}

// Bool returns a pointer to v, for building PATCH bodies inline.
func Bool(v bool) *bool { return &v }

// Pages is the pagination block the list endpoints return alongside items.
type Pages struct {
	Current int `json:"current"`
	Total   int `json:"total,omitempty"`
	Items   int `json:"items,omitempty"`
	Size    int `json:"size"`
	MaxSize int `json:"maxSize"`
}

// RuleList is the envelope of GET /firewall/rules/ipv4.
type RuleList struct {
	Items []Rule `json:"items"`
	Pages Pages  `json:"pages"`
}
