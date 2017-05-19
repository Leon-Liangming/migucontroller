package iamtoken

const (
	// KubeDomain represents domain of user configured to get admin token from Paas-IAM
	KubeDomain string = "op_svc_cfe"
	// KubeProject represents project corresponding to the system namespace
	KubeProject string = "kube-system"
	// NamespaceSystem is the system namespace where we place system components.
	NamespaceSystem string = "kube-system"

	authHeaderPrefix = "HWS-HMAC-SHA256"
	timeFormat       = "20060102T150405Z"
	shortTimeFormat  = "20060102"
	terminalString   = "hws_request"
	hwsName          = "HWS"
	hwsDate          = "X-Hws-Date"
)

type DomainScope struct {
	Domain PaaSIAMDomain `json:"domain"`
}

type PaaSIAMDomain struct {
	Name string `json:"name"`
}

type ProjectScope struct {
	Project PaaSIAMProject `json:"project"`
}

type PaaSIAMProject struct {
	Name string `json:"name"`
}

type PaaSIAMTokenData struct {
	Auth AuthDataWithAK `json:"auth"`
}

type AuthDataWithAK struct {
	Identity IdentityWithAK `json:"identity"`
	Scope    interface{}    `json:"scope"`
}

type IdentityWithAK struct {
	Methods     []string         `json:"methods"`
	HwAccessKey PaaSIAMAccessKey `json:"hw_access_key"`
}

type PaaSIAMAccessKey struct {
	Access AccessKey `json:"access"`
}

type AccessKey struct {
	Key string `json:"key"`
}

