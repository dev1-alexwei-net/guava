package executor

import (
	"crypto/tls"
	"net/http"
)

// NewGatewayHTTPClient returns an HTTP client configured for Gateway TLS.
// Certificate and hostname verification are skipped unless verifyTLS is true.
func NewGatewayHTTPClient(verifyTLS bool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	tlsConfig := transport.TLSClientConfig
	if tlsConfig == nil {
		tlsConfig = &tls.Config{}
	} else {
		tlsConfig = tlsConfig.Clone()
	}
	tlsConfig.InsecureSkipVerify = !verifyTLS // #nosec G402 -- verification is an explicit opt-in.
	transport.TLSClientConfig = tlsConfig
	return &http.Client{Transport: transport}
}
