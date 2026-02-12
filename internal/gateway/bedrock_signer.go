package gateway

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/rs/zerolog/log"
)

const (
	bedrockRuntimeService = "bedrock"
	bedrockHostPattern    = "bedrock-runtime.%s.amazonaws.com"
)

// BedrockSigner handles AWS SigV4 request signing for Bedrock Runtime API calls.
// It loads credentials from the standard AWS credential chain (environment variables,
// shared credentials file, IAM roles, etc.) via aws-sdk-go-v2/config.
type BedrockSigner struct {
	credentials aws.CredentialsProvider
	region      string
	signer      *v4.Signer
	configured  bool
}

// NewBedrockSigner creates a BedrockSigner by loading AWS credentials from the
// default credential chain. Returns a non-nil signer even if credentials are
// unavailable (IsConfigured() will return false).
func NewBedrockSigner() *BedrockSigner {
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = os.Getenv("AWS_DEFAULT_REGION")
	}
	if region == "" {
		region = "us-east-1"
	}

	bs := &BedrockSigner{
		region: region,
		signer: v4.NewSigner(),
	}

	// Try to load credentials from the default chain
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(region),
	)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to load AWS config for Bedrock signer")
		return bs
	}

	// Verify credentials are available by retrieving them
	creds, err := cfg.Credentials.Retrieve(context.Background())
	if err != nil {
		log.Debug().Err(err).Msg("No AWS credentials available for Bedrock signer")
		return bs
	}

	if creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
		log.Debug().Msg("AWS credentials are empty, Bedrock signer not configured")
		return bs
	}

	bs.credentials = cfg.Credentials
	bs.configured = true

	log.Info().
		Str("region", region).
		Str("access_key_prefix", creds.AccessKeyID[:min(4, len(creds.AccessKeyID))]+"...").
		Msg("Bedrock signer initialized")

	return bs
}

// IsConfigured returns true if AWS credentials are available for signing.
func (bs *BedrockSigner) IsConfigured() bool {
	return bs.configured
}

// Region returns the configured AWS region.
func (bs *BedrockSigner) Region() string {
	return bs.region
}

// SignRequest signs an HTTP request with AWS SigV4 for the bedrock-runtime service.
// The request's Host header and URL must already be set to the target Bedrock endpoint.
// The body parameter is the request body bytes (needed for payload hash).
func (bs *BedrockSigner) SignRequest(ctx context.Context, req *http.Request, body []byte) error {
	if !bs.configured {
		return fmt.Errorf("bedrock signer not configured: no AWS credentials available")
	}

	creds, err := bs.credentials.Retrieve(ctx)
	if err != nil {
		return fmt.Errorf("failed to retrieve AWS credentials: %w", err)
	}

	// Compute SHA256 hash of the request body
	payloadHash := fmt.Sprintf("%x", sha256.Sum256(body))

	err = bs.signer.SignHTTP(ctx, creds, req, payloadHash, bedrockRuntimeService, bs.region, time.Now())
	if err != nil {
		return fmt.Errorf("failed to sign request: %w", err)
	}

	return nil
}

// BuildTargetURL constructs the real Bedrock Runtime endpoint URL from a request path.
// Example: /model/anthropic.claude-3-5-sonnet-20241022-v2:0/invoke
// becomes: https://bedrock-runtime.us-east-1.amazonaws.com/model/anthropic.claude-3-5-sonnet-20241022-v2:0/invoke
func (bs *BedrockSigner) BuildTargetURL(path string) string {
	host := fmt.Sprintf(bedrockHostPattern, bs.region)
	return fmt.Sprintf("https://%s%s", host, path)
}

// IsBedrockHost checks if a hostname is a Bedrock Runtime endpoint.
func IsBedrockHost(host string) bool {
	host = strings.ToLower(host)
	return strings.HasPrefix(host, "bedrock-runtime.") &&
		strings.HasSuffix(host, ".amazonaws.com")
}
