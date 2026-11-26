package executor

import (
	"net/http"

	"github.com/dev1-alexwei-net/guava/conversations"
)

// NewGatewayHTTPClient remains available from executor for CLI compatibility.
func NewGatewayHTTPClient(verifyTLS bool) *http.Client {
	return conversations.NewGatewayHTTPClient(verifyTLS)
}
