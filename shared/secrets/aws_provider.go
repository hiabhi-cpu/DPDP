package secrets

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

const (
	// awsRegion is non-negotiable — DPDP data localisation requires ap-south-1 Mumbai.
	awsRegion = "ap-south-1"

	// cacheTTL is how long a fetched secret is kept in memory before re-fetching.
	// AWS Secrets Manager has API rate limits — caching avoids throttling.
	cacheTTL = 5 * time.Minute

	// Secret path patterns in AWS Secrets Manager
	hospitalKeyPath = "/consentmanager/hospitals/%s/key"
	systemSaltPath  = "/consentmanager/system/salt"
)

type cachedSecret struct {
	value     string
	expiresAt time.Time
}

// AWSProvider fetches secrets from AWS Secrets Manager (ap-south-1 only).
// Includes an in-memory cache to avoid per-request API calls.
// Used in production (AWS_SECRETS_MOCK=false).
type AWSProvider struct {
	client *secretsmanager.Client

	mu    sync.RWMutex
	cache map[string]cachedSecret // secretPath → cached value
}

// NewAWSProvider creates an AWSProvider using the default AWS SDK credential chain
// (environment variables, ~/.aws/credentials, ECS task role, EC2 instance profile).
func NewAWSProvider(ctx context.Context) (*AWSProvider, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(awsRegion))
	if err != nil {
		return nil, fmt.Errorf("secrets.AWSProvider: failed to load AWS config: %w", err)
	}

	return &AWSProvider{
		client: secretsmanager.NewFromConfig(cfg),
		cache:  make(map[string]cachedSecret),
	}, nil
}

func (p *AWSProvider) getSecret(ctx context.Context, path string) (string, error) {
	// Check cache first
	p.mu.RLock()
	cached, ok := p.cache[path]
	p.mu.RUnlock()

	if ok && time.Now().Before(cached.expiresAt) {
		return cached.value, nil
	}

	// Fetch from AWS Secrets Manager
	out, err := p.client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(path),
	})
	if err != nil {
		return "", fmt.Errorf("secrets.AWSProvider: failed to get secret %q: %w", path, err)
	}

	value := aws.ToString(out.SecretString)

	// Store in cache
	p.mu.Lock()
	p.cache[path] = cachedSecret{
		value:     value,
		expiresAt: time.Now().Add(cacheTTL),
	}
	p.mu.Unlock()

	return value, nil
}

// GetHospitalKey fetches the hospital-specific HMAC key from Secrets Manager.
func (p *AWSProvider) GetHospitalKey(ctx context.Context, hospitalID string) (string, error) {
	path := fmt.Sprintf(hospitalKeyPath, hospitalID)
	return p.getSecret(ctx, path)
}

// GetSystemSalt fetches the global SYSTEM_SALT from Secrets Manager.
func (p *AWSProvider) GetSystemSalt(ctx context.Context) (string, error) {
	return p.getSecret(ctx, systemSaltPath)
}
