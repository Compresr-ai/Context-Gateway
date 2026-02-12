// Bedrock signing transport for external LLM API calls.
//
// Provides an http.RoundTripper that automatically signs requests with AWS SigV4
// for the bedrock-runtime service. Used by CallLLM when provider is "bedrock".
package external

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// BedrockSigningTransport is an http.RoundTripper that signs requests with AWS SigV4.
type BedrockSigningTransport struct {
	credentials aws.CredentialsProvider
	region      string
	signer      *v4.Signer
	base        http.RoundTripper
}

// NewBedrockSigningTransport creates a transport that signs requests for bedrock-runtime.
// It loads credentials from the standard AWS credential chain.
// The base transport is used for the actual HTTP call (nil uses http.DefaultTransport).
func NewBedrockSigningTransport(region string, base http.RoundTripper) (*BedrockSigningTransport, error) {
	if region == "" {
		region = "us-east-1"
	}
	if base == nil {
		base = http.DefaultTransport
	}

	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(region),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	// Verify credentials are retrievable
	if _, err := cfg.Credentials.Retrieve(context.Background()); err != nil {
		return nil, fmt.Errorf("failed to retrieve AWS credentials: %w", err)
	}

	return &BedrockSigningTransport{
		credentials: cfg.Credentials,
		region:      region,
		signer:      v4.NewSigner(),
		base:        base,
	}, nil
}

// RoundTrip implements http.RoundTripper. It signs the request with SigV4 before sending.
func (t *BedrockSigningTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Read body for signing
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read request body for signing: %w", err)
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
	}

	creds, err := t.credentials.Retrieve(req.Context())
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve AWS credentials: %w", err)
	}

	payloadHash := fmt.Sprintf("%x", sha256.Sum256(body))
	err = t.signer.SignHTTP(req.Context(), creds, req, payloadHash, "bedrock", t.region, time.Now())
	if err != nil {
		return nil, fmt.Errorf("failed to sign Bedrock request: %w", err)
	}

	// Reset body reader after signing
	req.Body = io.NopCloser(bytes.NewReader(body))

	return t.base.RoundTrip(req)
}
