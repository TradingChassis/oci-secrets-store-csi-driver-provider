/*
** OCI Secrets Store CSI Driver Provider
**
** Copyright (c) 2022 Oracle America, Inc. and its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */
package service

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/oracle-samples/oci-secrets-store-csi-driver-provider/internal/types"
	"github.com/oracle/oci-go-sdk/v65/common"
	ociauth "github.com/oracle/oci-go-sdk/v65/common/auth"
	"github.com/oracle/oci-go-sdk/v65/secrets"
	apiMachineryTypes "k8s.io/apimachinery/pkg/types"
)

type fakeServiceAccountTokenSource struct {
	mu        sync.Mutex
	calls     int
	expiresAt func(time.Duration) time.Time
	started   chan struct{}
	startOnce sync.Once
	block     chan struct{}
	namespace string
	saName    string
	ttl       time.Duration
}

func (source *fakeServiceAccountTokenSource) TokenForServiceAccount(
	ctx context.Context,
	namespace string,
	serviceAccountName string,
	ttl time.Duration) (*ServiceAccountToken, error) {

	source.mu.Lock()
	source.calls++
	callNumber := source.calls
	source.namespace = namespace
	source.saName = serviceAccountName
	source.ttl = ttl
	source.mu.Unlock()

	if source.started != nil {
		source.startOnce.Do(func() {
			close(source.started)
		})
	}
	if source.block != nil {
		select {
		case <-source.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	expiresAt := time.Now().Add(ttl)
	if source.expiresAt != nil {
		expiresAt = source.expiresAt(ttl)
	}

	return &ServiceAccountToken{
		Token:     fmt.Sprintf("token-%d", callNumber),
		ExpiresAt: expiresAt,
	}, nil
}

func (source *fakeServiceAccountTokenSource) Calls() int {
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.calls
}

func (source *fakeServiceAccountTokenSource) LastRequest() (string, string, time.Duration) {
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.namespace, source.saName, source.ttl
}

type fakeWorkloadIdentityFactory struct {
	mu                     sync.Mutex
	configProvidersCreated int
	secretClientsCreated   int
	lastTokenProvider      ociauth.ServiceAccountTokenProvider
	started                chan struct{}
	startOnce              sync.Once
	block                  chan struct{}
}

func (factory *fakeWorkloadIdentityFactory) createSecretClient(
	_ common.ConfigurationProvider) (OCISecretClient, error) {

	factory.mu.Lock()
	defer factory.mu.Unlock()

	factory.secretClientsCreated++
	return &fakeWorkloadIdentitySecretClient{id: factory.secretClientsCreated}, nil
}

func (factory *fakeWorkloadIdentityFactory) createConfigProvider(
	_ *types.Auth) (common.ConfigurationProvider, error) {

	return common.NewRawConfigurationProvider("tenancy", "user", "region", "fingerprint", "privatekey", nil), nil
}

func (factory *fakeWorkloadIdentityFactory) createWorkloadIdentityConfigProvider(
	tokenProvider ociauth.ServiceAccountTokenProvider) (common.ConfigurationProvider, error) {

	factory.mu.Lock()
	factory.configProvidersCreated++
	factory.lastTokenProvider = tokenProvider
	callNumber := factory.configProvidersCreated
	factory.mu.Unlock()

	if factory.started != nil {
		factory.startOnce.Do(func() {
			close(factory.started)
		})
	}
	if factory.block != nil {
		<-factory.block
	}

	return common.NewRawConfigurationProvider(
		"tenancy", "user", "region", "fingerprint", fmt.Sprintf("private-key-%d", callNumber), nil), nil
}

func (factory *fakeWorkloadIdentityFactory) ConfigProvidersCreated() int {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return factory.configProvidersCreated
}

func (factory *fakeWorkloadIdentityFactory) SecretClientsCreated() int {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return factory.secretClientsCreated
}

func (factory *fakeWorkloadIdentityFactory) LastTokenProvider() ociauth.ServiceAccountTokenProvider {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return factory.lastTokenProvider
}

type fakeWorkloadIdentitySecretClient struct {
	id int
}

func (*fakeWorkloadIdentitySecretClient) GetSecretBundleByName(
	context.Context, secrets.GetSecretBundleByNameRequest) (secrets.GetSecretBundleByNameResponse, error) {

	return secrets.GetSecretBundleByNameResponse{}, nil
}

func newTestWorkloadIdentityClientCache(
	now *time.Time,
	tokenSource *fakeServiceAccountTokenSource,
	factory *fakeWorkloadIdentityFactory) *workloadIdentityClientCache {

	cache := newWorkloadIdentityClientCache(tokenSource, factory)
	cache.now = func() time.Time {
		return *now
	}
	cache.maxEntries = 10
	return cache
}

func newWorkloadIdentityAuth(podUID string) *types.Auth {
	return newWorkloadIdentityAuthWith("test-namespace", "test-pod", podUID, "test-service-account")
}

func newWorkloadIdentityAuthWith(namespace string, podName string, podUID string, serviceAccount string) *types.Auth {
	return newWorkloadIdentityAuthWithServiceAccountUID(
		namespace, podName, podUID, serviceAccount, "test-service-account-uid")
}

func newWorkloadIdentityAuthWithServiceAccountUID(
	namespace string,
	podName string,
	podUID string,
	serviceAccount string,
	serviceAccountUID string) *types.Auth {

	return &types.Auth{
		Type: types.Workload,
		WorkloadIdentityCfg: types.WorkloadIdentityConfig{
			PodInfo: types.PodInfo{
				Namespace:          namespace,
				Name:               podName,
				UID:                apiMachineryTypes.UID(podUID),
				ServiceAccountName: serviceAccount,
				ServiceAccountUID:  apiMachineryTypes.UID(serviceAccountUID),
			},
		},
	}
}

func TestWorkloadIdentityClientCache_SamePodBeforeTTLReusesClient(t *testing.T) {
	now := time.Date(2026, time.June, 16, 12, 0, 0, 0, time.UTC)
	tokenSource := &fakeServiceAccountTokenSource{expiresAt: func(ttl time.Duration) time.Time {
		return now.Add(ttl)
	}}
	factory := &fakeWorkloadIdentityFactory{}
	cache := newTestWorkloadIdentityClientCache(&now, tokenSource, factory)
	auth := newWorkloadIdentityAuth("pod-uid-1")

	firstLease, err := cache.GetOrCreate(context.Background(), auth)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	firstClient := firstLease.SecretClient()
	firstLease.Release()

	secondLease, err := cache.GetOrCreate(context.Background(), auth)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	secondClient := secondLease.SecretClient()
	secondLease.Release()

	if firstClient != secondClient {
		t.Fatalf("Expected same cached client before TTL expiry")
	}
	if tokenSource.Calls() != 0 {
		t.Fatalf("Expected token requests to be deferred to the SDK token provider, got %d", tokenSource.Calls())
	}
	if factory.ConfigProvidersCreated() != 1 {
		t.Fatalf("Expected one config provider, got %d", factory.ConfigProvidersCreated())
	}
	if factory.SecretClientsCreated() != 1 {
		t.Fatalf("Expected one Secrets client, got %d", factory.SecretClientsCreated())
	}
}

func TestWorkloadIdentityClientCache_DifferentPodUIDSameServiceAccountReusesClient(t *testing.T) {
	now := time.Date(2026, time.June, 16, 12, 0, 0, 0, time.UTC)
	tokenSource := &fakeServiceAccountTokenSource{expiresAt: func(ttl time.Duration) time.Time {
		return now.Add(ttl)
	}}
	factory := &fakeWorkloadIdentityFactory{}
	cache := newTestWorkloadIdentityClientCache(&now, tokenSource, factory)

	firstLease, err := cache.GetOrCreate(context.Background(), newWorkloadIdentityAuth("pod-uid-1"))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	firstClient := firstLease.SecretClient()
	firstLease.Release()

	secondLease, err := cache.GetOrCreate(context.Background(), newWorkloadIdentityAuth("pod-uid-2"))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	secondClient := secondLease.SecretClient()
	secondLease.Release()

	if firstClient != secondClient {
		t.Fatalf("Expected same cached client for different pod UIDs using the same service account")
	}
	if tokenSource.Calls() != 0 {
		t.Fatalf("Expected token requests to be deferred to the SDK token provider, got %d", tokenSource.Calls())
	}
	if factory.ConfigProvidersCreated() != 1 {
		t.Fatalf("Expected one config provider, got %d", factory.ConfigProvidersCreated())
	}
}

func TestWorkloadIdentityClientCache_SameServiceAccountNameDifferentUIDCreatesDifferentClient(t *testing.T) {
	now := time.Date(2026, time.June, 16, 12, 0, 0, 0, time.UTC)
	tokenSource := &fakeServiceAccountTokenSource{expiresAt: func(ttl time.Duration) time.Time {
		return now.Add(ttl)
	}}
	factory := &fakeWorkloadIdentityFactory{}
	cache := newTestWorkloadIdentityClientCache(&now, tokenSource, factory)

	firstLease, err := cache.GetOrCreate(
		context.Background(),
		newWorkloadIdentityAuthWithServiceAccountUID(
			"test-namespace", "test-pod-1", "pod-uid-1", "test-service-account", "service-account-uid-1"))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	firstClient := firstLease.SecretClient()
	firstLease.Release()

	secondLease, err := cache.GetOrCreate(
		context.Background(),
		newWorkloadIdentityAuthWithServiceAccountUID(
			"test-namespace", "test-pod-2", "pod-uid-2", "test-service-account", "service-account-uid-2"))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	secondClient := secondLease.SecretClient()
	secondLease.Release()

	if firstClient == secondClient {
		t.Fatalf("Expected different clients for service accounts with same name and different UIDs")
	}
	if factory.ConfigProvidersCreated() != 2 {
		t.Fatalf("Expected two config providers, got %d", factory.ConfigProvidersCreated())
	}
}

func TestWorkloadIdentityClientCache_DifferentServiceAccountCreatesDifferentClient(t *testing.T) {
	now := time.Date(2026, time.June, 16, 12, 0, 0, 0, time.UTC)
	tokenSource := &fakeServiceAccountTokenSource{expiresAt: func(ttl time.Duration) time.Time {
		return now.Add(ttl)
	}}
	factory := &fakeWorkloadIdentityFactory{}
	cache := newTestWorkloadIdentityClientCache(&now, tokenSource, factory)

	firstLease, err := cache.GetOrCreate(
		context.Background(),
		newWorkloadIdentityAuthWith("test-namespace", "test-pod-1", "pod-uid-1", "test-service-account-1"))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	firstClient := firstLease.SecretClient()
	firstLease.Release()

	secondLease, err := cache.GetOrCreate(
		context.Background(),
		newWorkloadIdentityAuthWith("test-namespace", "test-pod-2", "pod-uid-2", "test-service-account-2"))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	secondClient := secondLease.SecretClient()
	secondLease.Release()

	if firstClient == secondClient {
		t.Fatalf("Expected different clients for different service accounts")
	}
	if factory.ConfigProvidersCreated() != 2 {
		t.Fatalf("Expected two config providers, got %d", factory.ConfigProvidersCreated())
	}
}

func TestWorkloadIdentityClientCache_DifferentNamespaceCreatesDifferentClient(t *testing.T) {
	now := time.Date(2026, time.June, 16, 12, 0, 0, 0, time.UTC)
	tokenSource := &fakeServiceAccountTokenSource{expiresAt: func(ttl time.Duration) time.Time {
		return now.Add(ttl)
	}}
	factory := &fakeWorkloadIdentityFactory{}
	cache := newTestWorkloadIdentityClientCache(&now, tokenSource, factory)

	firstLease, err := cache.GetOrCreate(
		context.Background(),
		newWorkloadIdentityAuthWith("test-namespace-1", "test-pod-1", "pod-uid-1", "test-service-account"))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	firstClient := firstLease.SecretClient()
	firstLease.Release()

	secondLease, err := cache.GetOrCreate(
		context.Background(),
		newWorkloadIdentityAuthWith("test-namespace-2", "test-pod-2", "pod-uid-2", "test-service-account"))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	secondClient := secondLease.SecretClient()
	secondLease.Release()

	if firstClient == secondClient {
		t.Fatalf("Expected different clients for different namespaces")
	}
	if factory.ConfigProvidersCreated() != 2 {
		t.Fatalf("Expected two config providers, got %d", factory.ConfigProvidersCreated())
	}
}

func TestWorkloadIdentityClientCache_IdleEntryIsReplaced(t *testing.T) {
	now := time.Date(2026, time.June, 16, 12, 0, 0, 0, time.UTC)
	tokenSource := &fakeServiceAccountTokenSource{expiresAt: func(ttl time.Duration) time.Time {
		return now.Add(ttl)
	}}
	factory := &fakeWorkloadIdentityFactory{}
	cache := newTestWorkloadIdentityClientCache(&now, tokenSource, factory)
	auth := newWorkloadIdentityAuth("pod-uid-1")

	firstLease, err := cache.GetOrCreate(context.Background(), auth)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	firstClient := firstLease.SecretClient()
	firstLease.Release()

	now = now.Add(workloadIdentityCacheIdleTTL)

	secondLease, err := cache.GetOrCreate(context.Background(), auth)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	secondClient := secondLease.SecretClient()
	secondLease.Release()

	if firstClient == secondClient {
		t.Fatalf("Expected a new client after cache idle expiry")
	}
	if factory.ConfigProvidersCreated() != 2 {
		t.Fatalf("Expected two config providers, got %d", factory.ConfigProvidersCreated())
	}
}

func TestWorkloadIdentityClientCache_ReaperRetiresExpiredEntry(t *testing.T) {
	now := time.Date(2026, time.June, 16, 12, 0, 0, 0, time.UTC)
	tokenSource := &fakeServiceAccountTokenSource{expiresAt: func(ttl time.Duration) time.Time {
		return now.Add(ttl)
	}}
	factory := &fakeWorkloadIdentityFactory{}
	cache := newTestWorkloadIdentityClientCache(&now, tokenSource, factory)
	auth := newWorkloadIdentityAuth("pod-uid-1")

	lease, err := cache.GetOrCreate(context.Background(), auth)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	lease.Release()

	now = now.Add(workloadIdentityCacheIdleTTL)
	cache.reapExpired(now)

	if len(cache.entries) != 0 {
		t.Fatalf("Expected expired entry to be removed by reaper, got %d entries", len(cache.entries))
	}
}

func TestWorkloadIdentityClientCache_ConcurrentMissesCreateOneClient(t *testing.T) {
	now := time.Date(2026, time.June, 16, 12, 0, 0, 0, time.UTC)
	tokenSource := &fakeServiceAccountTokenSource{
		expiresAt: func(ttl time.Duration) time.Time {
			return now.Add(ttl)
		},
	}
	factory := &fakeWorkloadIdentityFactory{
		started: make(chan struct{}),
		block:   make(chan struct{}),
	}
	cache := newTestWorkloadIdentityClientCache(&now, tokenSource, factory)
	auth := newWorkloadIdentityAuth("pod-uid-1")

	const goroutines = 20
	errCh := make(chan error, goroutines)
	startCh := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		go func() {
			<-startCh
			lease, err := cache.GetOrCreate(context.Background(), auth)
			if err != nil {
				errCh <- err
				return
			}
			lease.Release()
			errCh <- nil
		}()
	}

	close(startCh)
	select {
	case <-factory.started:
	case <-time.After(5 * time.Second):
		t.Fatalf("Timed out waiting for first config provider creation")
	}
	close(factory.block)

	for i := 0; i < goroutines; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
	}

	if tokenSource.Calls() != 0 {
		t.Fatalf("Expected token requests to be deferred to the SDK token provider, got %d", tokenSource.Calls())
	}
	if factory.ConfigProvidersCreated() != 1 {
		t.Fatalf("Expected one config provider for concurrent misses, got %d", factory.ConfigProvidersCreated())
	}
	if factory.SecretClientsCreated() != 1 {
		t.Fatalf("Expected one Secrets client for concurrent misses, got %d", factory.SecretClientsCreated())
	}
}

func TestWorkloadIdentityServiceAccountTokenProvider_FetchesTokenForServiceAccount(t *testing.T) {
	now := time.Date(2026, time.June, 16, 12, 0, 0, 0, time.UTC)
	tokenSource := &fakeServiceAccountTokenSource{expiresAt: func(ttl time.Duration) time.Time {
		return now.Add(ttl)
	}}
	factory := &fakeWorkloadIdentityFactory{}
	cache := newTestWorkloadIdentityClientCache(&now, tokenSource, factory)

	lease, err := cache.GetOrCreate(
		context.Background(),
		newWorkloadIdentityAuthWith("test-namespace", "test-pod", "pod-uid-1", "test-service-account"))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	lease.Release()

	tokenProvider := factory.LastTokenProvider()
	if tokenProvider == nil {
		t.Fatalf("Expected workload identity token provider to be passed to factory")
	}

	token, err := tokenProvider.ServiceAccountToken()
	if err != nil {
		t.Fatalf("Unexpected token provider error: %v", err)
	}
	if token != "token-1" {
		t.Fatalf("Expected token-1, got %s", token)
	}

	namespace, serviceAccount, ttl := tokenSource.LastRequest()
	if namespace != "test-namespace" {
		t.Fatalf("Expected namespace test-namespace, got %s", namespace)
	}
	if serviceAccount != "test-service-account" {
		t.Fatalf("Expected service account test-service-account, got %s", serviceAccount)
	}
	if ttl != workloadIdentityTokenTTL {
		t.Fatalf("Expected token TTL %s, got %s", workloadIdentityTokenTTL, ttl)
	}
}
