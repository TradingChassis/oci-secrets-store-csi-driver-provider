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
	"time"

	"github.com/oracle-samples/oci-secrets-store-csi-driver-provider/internal/types"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/common/auth"
)

const (
	workloadIdentityTokenTTL              = 15 * time.Minute
	workloadIdentityCacheIdleTTL          = 60 * time.Minute
	workloadIdentityCacheMaxLifetime      = 12 * time.Hour
	workloadIdentityTokenRequestTimeout   = 10 * time.Second
	workloadIdentityCacheReaperInterval   = time.Minute
	workloadIdentityClientCacheMaxEntries = 1024
)

// ServiceAccountToken contains a Kubernetes service account token and its expiry.
type ServiceAccountToken struct {
	Token     string
	ExpiresAt time.Time
}

// ServiceAccountTokenSource creates Kubernetes service account tokens.
type ServiceAccountTokenSource interface {
	TokenForServiceAccount(
		ctx context.Context,
		namespace string,
		serviceAccountName string,
		ttl time.Duration) (*ServiceAccountToken, error)
}

type workloadIdentityClientCache struct {
	mu                  sync.Mutex
	entries             map[workloadIdentityClientCacheKey]*workloadIdentityClientCacheEntry
	inFlight            map[workloadIdentityClientCacheKey]*workloadIdentityClientCacheCall
	tokenSource         ServiceAccountTokenSource
	factory             SecretClientFactory
	tokenTTL            time.Duration
	idleTTL             time.Duration
	maxLifetime         time.Duration
	tokenRequestTimeout time.Duration
	maxEntries          int
	now                 func() time.Time
	stopCh              chan struct{}
	stopOnce            sync.Once
}

type workloadIdentityClientCacheKey struct {
	principalType           types.OCIPrincipalType
	serviceAccountNamespace string
	serviceAccountName      string
	serviceAccountUID       string
}

func (key workloadIdentityClientCacheKey) String() string {
	return fmt.Sprintf("%s/%s/%s/%s",
		key.principalType, key.serviceAccountNamespace, key.serviceAccountName, key.serviceAccountUID)
}

type workloadIdentityClientCacheEntry struct {
	key            workloadIdentityClientCacheKey
	configProvider common.ConfigurationProvider
	secretClient   OCISecretClient

	idleExpiresAt time.Time
	maxExpiresAt  time.Time
	createdAt     time.Time
	lastUsedAt    time.Time

	refCount int
	retired  bool
}

func (entry *workloadIdentityClientCacheEntry) isReusable(now time.Time) bool {
	if entry == nil || entry.retired || !now.Before(entry.idleExpiresAt) {
		return false
	}
	return entry.maxExpiresAt.IsZero() || now.Before(entry.maxExpiresAt)
}

type workloadIdentityClientCacheLease struct {
	cache *workloadIdentityClientCache
	entry *workloadIdentityClientCacheEntry
}

func (lease *workloadIdentityClientCacheLease) SecretClient() OCISecretClient {
	return lease.entry.secretClient
}

func (lease *workloadIdentityClientCacheLease) Release() {
	if lease == nil || lease.cache == nil || lease.entry == nil {
		return
	}
	lease.cache.release(lease.entry)
	lease.entry = nil
}

type workloadIdentityClientCacheCall struct {
	done chan struct{}
	err  error
}

func newWorkloadIdentityClientCache(
	tokenSource ServiceAccountTokenSource,
	factory SecretClientFactory) *workloadIdentityClientCache {

	return &workloadIdentityClientCache{
		entries:             make(map[workloadIdentityClientCacheKey]*workloadIdentityClientCacheEntry),
		inFlight:            make(map[workloadIdentityClientCacheKey]*workloadIdentityClientCacheCall),
		tokenSource:         tokenSource,
		factory:             factory,
		tokenTTL:            workloadIdentityTokenTTL,
		idleTTL:             workloadIdentityCacheIdleTTL,
		maxLifetime:         workloadIdentityCacheMaxLifetime,
		tokenRequestTimeout: workloadIdentityTokenRequestTimeout,
		maxEntries:          workloadIdentityClientCacheMaxEntries,
		now:                 time.Now,
		stopCh:              make(chan struct{}),
	}
}

func (cache *workloadIdentityClientCache) StartReaper(interval time.Duration) {
	if cache == nil || interval <= 0 {
		return
	}

	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				cache.reapExpired(cache.now())
			case <-cache.stopCh:
				return
			}
		}
	}()
}

func (cache *workloadIdentityClientCache) StopReaper() {
	if cache == nil {
		return
	}
	cache.stopOnce.Do(func() {
		close(cache.stopCh)
	})
}

func (cache *workloadIdentityClientCache) GetOrCreate(
	ctx context.Context, auth *types.Auth) (*workloadIdentityClientCacheLease, error) {

	key, err := cacheKeyFromAuth(auth)
	if err != nil {
		return nil, err
	}

	for {
		now := cache.now()
		cache.mu.Lock()
		if entry := cache.entries[key]; entry.isReusable(now) {
			entry.refCount++
			entry.lastUsedAt = now
			entry.idleExpiresAt = now.Add(cache.idleTTL)
			cache.mu.Unlock()
			return &workloadIdentityClientCacheLease{cache: cache, entry: entry}, nil
		}

		if entry := cache.entries[key]; entry != nil {
			delete(cache.entries, key)
			cache.retireLocked(entry)
		}

		if call := cache.inFlight[key]; call != nil {
			cache.mu.Unlock()
			if err := waitForWorkloadIdentityClientCacheCall(ctx, call); err != nil {
				return nil, err
			}
			continue
		}

		if err := cache.makeRoomForEntryLocked(now); err != nil {
			cache.mu.Unlock()
			return nil, err
		}

		call := &workloadIdentityClientCacheCall{done: make(chan struct{})}
		cache.inFlight[key] = call
		cache.mu.Unlock()

		call.err = cache.createAndStoreEntry(key)

		cache.mu.Lock()
		delete(cache.inFlight, key)
		close(call.done)
		cache.mu.Unlock()

		if call.err != nil {
			return nil, call.err
		}
	}
}

func waitForWorkloadIdentityClientCacheCall(
	ctx context.Context, call *workloadIdentityClientCacheCall) error {

	select {
	case <-call.done:
		return call.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (cache *workloadIdentityClientCache) createAndStoreEntry(key workloadIdentityClientCacheKey) error {

	entry, err := cache.createEntry(key)
	if err != nil {
		return err
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()

	now := cache.now()
	if err := cache.makeRoomForEntryLocked(now); err != nil {
		return err
	}
	cache.entries[key] = entry
	return nil
}

func (cache *workloadIdentityClientCache) createEntry(
	key workloadIdentityClientCacheKey) (*workloadIdentityClientCacheEntry, error) {

	if cache.tokenSource == nil {
		return nil, fmt.Errorf("workload identity token source is not configured")
	}

	tokenProvider := &workloadIdentityServiceAccountTokenProvider{
		tokenSource:        cache.tokenSource,
		namespace:          key.serviceAccountNamespace,
		serviceAccountName: key.serviceAccountName,
		ttl:                cache.tokenTTL,
		requestTimeout:     cache.tokenRequestTimeout,
	}

	configProvider, err := cache.factory.createWorkloadIdentityConfigProvider(tokenProvider)
	if err != nil {
		return nil, err
	}

	secretClient, err := cache.factory.createSecretClient(configProvider)
	if err != nil {
		return nil, err
	}

	now := cache.now()
	maxExpiresAt := time.Time{}
	if cache.maxLifetime > 0 {
		maxExpiresAt = now.Add(cache.maxLifetime)
	}
	return &workloadIdentityClientCacheEntry{
		key:            key,
		configProvider: configProvider,
		secretClient:   secretClient,
		idleExpiresAt:  now.Add(cache.idleTTL),
		maxExpiresAt:   maxExpiresAt,
		createdAt:      now,
		lastUsedAt:     now,
	}, nil
}

func (cache *workloadIdentityClientCache) release(entry *workloadIdentityClientCacheEntry) {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	if entry.refCount > 0 {
		entry.refCount--
	}
	if entry.refCount == 0 && entry.retired {
		entry.configProvider = nil
		entry.secretClient = nil
	}
}

func (cache *workloadIdentityClientCache) reapExpired(now time.Time) {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	for key, entry := range cache.entries {
		if !entry.isReusable(now) {
			delete(cache.entries, key)
			cache.retireLocked(entry)
		}
	}
}

func (cache *workloadIdentityClientCache) makeRoomForEntryLocked(now time.Time) error {
	if cache.maxEntries <= 0 || len(cache.entries) < cache.maxEntries {
		return nil
	}

	for key, entry := range cache.entries {
		if !entry.isReusable(now) {
			delete(cache.entries, key)
			cache.retireLocked(entry)
			return nil
		}
	}

	var (
		lruKey   workloadIdentityClientCacheKey
		lruEntry *workloadIdentityClientCacheEntry
		found    bool
	)
	for key, entry := range cache.entries {
		if entry.refCount != 0 {
			continue
		}
		if !found || entry.lastUsedAt.Before(lruEntry.lastUsedAt) {
			lruKey = key
			lruEntry = entry
			found = true
		}
	}
	if found {
		delete(cache.entries, lruKey)
		cache.retireLocked(lruEntry)
		return nil
	}

	return fmt.Errorf("workload identity client cache is full")
}

func (cache *workloadIdentityClientCache) retireLocked(entry *workloadIdentityClientCacheEntry) {
	entry.retired = true
	if entry.refCount == 0 {
		entry.configProvider = nil
		entry.secretClient = nil
	}
}

func cacheKeyFromAuth(auth *types.Auth) (workloadIdentityClientCacheKey, error) {
	if auth == nil {
		return workloadIdentityClientCacheKey{}, fmt.Errorf("auth config is not provided")
	}
	if auth.Type != types.Workload {
		return workloadIdentityClientCacheKey{}, fmt.Errorf("principal type is not workload identity")
	}

	podInfo := auth.WorkloadIdentityCfg.PodInfo
	if podInfo.Namespace == "" || podInfo.ServiceAccountName == "" || podInfo.ServiceAccountUID == "" {
		return workloadIdentityClientCacheKey{}, fmt.Errorf("workload identity service account metadata is incomplete")
	}

	return workloadIdentityClientCacheKey{
		principalType:           auth.Type,
		serviceAccountNamespace: podInfo.Namespace,
		serviceAccountName:      podInfo.ServiceAccountName,
		serviceAccountUID:       string(podInfo.ServiceAccountUID),
	}, nil
}

type workloadIdentityServiceAccountTokenProvider struct {
	tokenSource        ServiceAccountTokenSource
	namespace          string
	serviceAccountName string
	ttl                time.Duration
	requestTimeout     time.Duration
}

var _ auth.ServiceAccountTokenProvider = (*workloadIdentityServiceAccountTokenProvider)(nil)

func (provider *workloadIdentityServiceAccountTokenProvider) ServiceAccountToken() (string, error) {
	if provider == nil || provider.tokenSource == nil {
		return "", fmt.Errorf("workload identity token source is not configured")
	}

	ctx := context.Background()
	if provider.requestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, provider.requestTimeout)
		defer cancel()
	}

	token, err := provider.tokenSource.TokenForServiceAccount(
		ctx, provider.namespace, provider.serviceAccountName, provider.ttl)
	if err != nil {
		return "", err
	}
	if token == nil || token.Token == "" {
		return "", fmt.Errorf("workload identity token source returned an empty token")
	}
	return token.Token, nil
}
