package iamtoken

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/golang/glog"
)

type Signer struct {
	signTime time.Time
}

var (
	region    = ""
	service   = "cfe"
	tokenPath = `cfe_token`
)

//getToken gets user's token. If namespace is "", indicates get cfe_admin token
func GetToken(iamAddress, iamAccessKey, iamSecretKey, namespace string) (string, error) {
	// check if PaaS-IAM address is configured first
	if iamAddress == "" {
		glog.Errorf("iam address is not provided")
		return "", errors.New("iam address is not provided")
	}

	// get adminToken of project scope if in kube-system namespace; else get it of domain scope
	var scope interface{}
	if namespace == NamespaceSystem {
		scope = ProjectScope{
			Project: PaaSIAMProject{
				Name: KubeProject,
			},
		}
	} else {
		scope = DomainScope{
			Domain: PaaSIAMDomain{
				Name: KubeDomain,
			},
		}
	}

	// generate admin token first with AK/SK info configured in controller-manager
	adminTokenReq := PaaSIAMTokenData{
		Auth: AuthDataWithAK{
			Identity: IdentityWithAK{
				Methods: []string{"hw_access_key"},
				HwAccessKey: PaaSIAMAccessKey{
					Access: AccessKey{
						Key: iamAccessKey,
					},
				},
			},
			Scope: scope,
		},
	}
	data, err := json.Marshal(adminTokenReq)
	if err != nil {
		glog.Errorf("Error generating admin token: %v", err)
		return "", err
	}
	reqBody := bytes.NewReader(data)
	reqURL := iamAddress + "/v3/auth/tokens"
	// generate a http request for signature
	req, err := BuildRequest(http.MethodPost, reqURL, reqBody, "")
	if err != nil {
		glog.Errorf("Error generate http request to get token: %v", err)
		return "", err
	}
	signer := NewSigner()
	signedHeader := signer.Signature(req, iamAccessKey, iamSecretKey, data)

	httpClient := NewHttpClient()
	header, _, statusCode, err := httpClient.SendRequest(req, signedHeader)
	if err != nil {
		glog.Errorf("Error get admin token: %v", err)
		return "", err
	} else if statusCode != http.StatusCreated {
		glog.Errorf("Error get admin token, statusCode from PaaS-IAM is not 201 but: %d", statusCode)
		return "", errors.New(fmt.Sprintf("error statusCode from PaaS-IAM: %d", statusCode))
	}
	adminTokenList := header["X-Subject-Token"]
	if len(adminTokenList) == 0 {
		glog.Errorf("Error get admin token: token list is empty")
		return "", errors.New("token list empty")
	}
	adminToken := adminTokenList[0]
	return adminToken, nil
}

func BuildRequest(method string, urlStr string, body io.Reader, token string) (*http.Request, error) {
	req, err := http.NewRequest(method, urlStr, body)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Add("X-Auth-Token", token)
	}
	req.Header.Add("Content-Type", "application/json")
	return req, nil
}

// NewSigner init a new signer
func NewSigner() *Signer {
	return &Signer{signTime: time.Now()}
}

// httpClient
type HttpClient struct {
	client *http.Client
}

func NewHttpClient() *HttpClient {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	httpClient := HttpClient{&http.Client{Transport: tr}}
	return &httpClient
}

// SendRequest send a http request and return the resp info
func (c *HttpClient) SendRequest(req *http.Request, signature string) (header map[string][]string, data []byte, statusCode int, err error) {
	if signature != "" {
		req.Header.Add("X-Identity-Sign", signature)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, nil, 500, err
	}

	defer resp.Body.Close()

	data, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, 500, err
	}
	return resp.Header, data, resp.StatusCode, nil
}

// Signature generate a http header consist of signature and other info
func (s *Signer) Signature(request *http.Request, akey string, skey string, body []byte) string {
	request.Header.Add(hwsDate, s.signTime.UTC().Format(timeFormat))
	canonicalString := s.buildCanonicalRequest(request, body)
	stringtoSign := s.buildStringtoSign(canonicalString)
	signatureStr := s.buildSignature(skey, stringtoSign)
	credentialString := s.buildCredentialString()
	signedHeaders := s.buildsignedHeadersString(request)

	parts := []string{
		authHeaderPrefix + " Credential=" + akey + "/" + credentialString,
		"SignedHeaders=" + signedHeaders,
		"Signature=" + signatureStr,
	}

	return strings.Join(parts, ", ")
}

// buildSignature generate a signature with request and secret key
func (s *Signer) buildSignature(skey string, stringtoSign string) string {
	secret := skey
	date := makeHmac([]byte(hwsName+secret), []byte(s.signTime.UTC().Format(shortTimeFormat)))
	region := makeHmac(date, []byte(region))
	service := makeHmac(region, []byte(service))
	credentials := makeHmac(service, []byte(terminalString))
	toSignature := makeHmac(credentials, []byte(stringtoSign))
	signature := hex.EncodeToString(toSignature)
	return signature
}

// buildStringtoSign prepare data for building signature
func (s *Signer) buildStringtoSign(canonicalString string) string {
	stringToSign := strings.Join([]string{
		authHeaderPrefix,
		s.signTime.UTC().Format(timeFormat),
		s.buildCredentialString(),
		hex.EncodeToString(makeSha256([]byte(canonicalString))),
	}, "\n")
	return stringToSign
}

// buildCanonicalRequest convert request info into canonical format
func (s *Signer) buildCanonicalRequest(request *http.Request, body []byte) string {
	canonicalHeadersOut := s.buildcanonicalHeaders(request)
	signedHeaders := s.buildsignedHeadersString(request)
	// Generate SHA256 of body
	hash := sha256.New()
	hash.Write(body)
	md := hash.Sum(nil)
	hexbody := hex.EncodeToString(md)
	canonicalRequestStr := strings.Join([]string{
		request.Method,
		request.URL.Path + "/",
		request.URL.RawQuery,
		canonicalHeadersOut,
		signedHeaders,
		hexbody,
	}, "\n")
	return canonicalRequestStr
}

// buildcanonicalHeaders generate canonical headers
func (s *Signer) buildcanonicalHeaders(request *http.Request) string {
	var headers []string

	for header := range request.Header {
		standardized := strings.ToLower(strings.TrimSpace(header))
		headers = append(headers, standardized)
	}

	sort.Strings(headers)

	for i, header := range headers {
		headers[i] = header + ":" + strings.Replace(request.Header.Get(header), "\n", " ", -1)
	}

	if len(headers) > 0 {
		return strings.Join(headers, "\n") + "\n"
	} else {
		return ""
	}
}

// buildsignedHeadersString convert the header in request to certain format
func (s *Signer) buildsignedHeadersString(request *http.Request) string {
	var headers []string
	for header := range request.Header {
		headers = append(headers, strings.ToLower(header))
	}
	sort.Strings(headers)
	return strings.Join(headers, ";")
}

// buildCredentialString add date and several other information to signature header
func (s *Signer) buildCredentialString() string {
	credentialString := strings.Join([]string{
		s.signTime.UTC().Format(shortTimeFormat),
		region,
		service,
		terminalString,
	}, "/")
	return credentialString
}

// makeHmac convert data into sha256 format with certain key
func makeHmac(key []byte, data []byte) []byte {
	hash := hmac.New(sha256.New, key)
	hash.Write(data)
	return hash.Sum(nil)
}

// makeHmac convert data into sha256 format
func makeSha256(data []byte) []byte {
	hash := sha256.New()
	hash.Write(data)
	return hash.Sum(nil)
}

