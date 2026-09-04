package app

import (
	"testing"
	"time"
)

func TestAccountBalanceCacheKeyReusesCanonicalURLAndCredentialWithinOwner(t *testing.T) {
	firstURL := "HTTPS://Usage.Example.com/v1/usage"
	secondURL := "https://usage.example.com/v1"
	first := accountBalanceWork{ID: "a", OwnerID: "owner-1", SiteID: "site-1", RemoteID: 1, SourceType: "sub2api", ObservedSourceBaseURL: &firstURL, ObservedSourceCredentialFingerprint: "same-key"}
	second := accountBalanceWork{ID: "b", OwnerID: "owner-1", SiteID: "site-2", RemoteID: 2, SourceType: "sub2api", ObservedSourceBaseURL: &secondURL, ObservedSourceCredentialFingerprint: "same-key"}
	otherOwner := second
	otherOwner.ID = "c"
	otherOwner.OwnerID = "owner-2"

	if accountBalanceCacheKey(first) != accountBalanceCacheKey(second) {
		t.Fatalf("same owner and URL must share a cache key: %q != %q", accountBalanceCacheKey(first), accountBalanceCacheKey(second))
	}
	if accountBalanceCacheKey(first) == accountBalanceCacheKey(otherOwner) {
		t.Fatal("different owners must not share balance snapshots")
	}
}

func TestAccountBalanceCacheKeySeparatesSameURLWithDifferentCredentials(t *testing.T) {
	sharedURL := "https://usage.example/v1"
	first := accountBalanceWork{ID: "a", OwnerID: "owner", SourceType: "sub2api", ObservedSourceBaseURL: &sharedURL, ObservedSourceCredentialFingerprint: "key-a"}
	second := accountBalanceWork{ID: "b", OwnerID: "owner", SourceType: "sub2api", ObservedSourceBaseURL: &sharedURL, ObservedSourceCredentialFingerprint: "key-b"}
	if accountBalanceCacheKey(first) == accountBalanceCacheKey(second) {
		t.Fatal("same URL with different API keys must not share balance snapshots")
	}
}

func TestAccountBalanceCacheKeyWithoutCredentialFingerprintFallsBackToAccount(t *testing.T) {
	sharedURL := "https://usage.example/v1"
	first := accountBalanceWork{ID: "a", OwnerID: "owner", SiteID: "site-a", RemoteID: 1, SourceType: "sub2api", ObservedSourceBaseURL: &sharedURL}
	second := accountBalanceWork{ID: "b", OwnerID: "owner", SiteID: "site-b", RemoteID: 2, SourceType: "sub2api", ObservedSourceBaseURL: &sharedURL}
	if accountBalanceCacheKey(first) == accountBalanceCacheKey(second) {
		t.Fatal("accounts without a key fingerprint must remain isolated")
	}
}

func TestDueAccountBalanceRefreshGroupsQueriesSharedURLOnce(t *testing.T) {
	now := time.Now().UTC()
	sharedURL := "https://usage.example/v1"
	otherURL := "https://other.example/v1"
	works := []accountBalanceWork{
		{ID: "a", OwnerID: "owner", SourceType: "sub2api", ObservedSourceBaseURL: &sharedURL, ObservedSourceCredentialFingerprint: "shared-key"},
		{ID: "b", OwnerID: "owner", SourceType: "sub2api", ObservedSourceBaseURL: &sharedURL, ObservedSourceCredentialFingerprint: "shared-key"},
		{ID: "c", OwnerID: "owner", SourceType: "sub2api", ObservedSourceBaseURL: &otherURL, ObservedSourceCredentialFingerprint: "other-key"},
	}
	sharedKey := accountBalanceCacheKey(works[0])
	otherKey := accountBalanceCacheKey(works[2])
	snapshots := map[string]accountBalanceSnapshot{
		"a": {CacheKey: sharedKey, CheckedAt: now.Add(-time.Minute)},
		// b is intentionally missing, so the complete shared group is refreshed.
		"c": {CacheKey: otherKey, CheckedAt: now.Add(-time.Minute)},
	}

	groups := dueAccountBalanceRefreshGroups(works, snapshots, now)
	if len(groups) != 1 || groups[0].Key != sharedKey || len(groups[0].Works) != 2 {
		t.Fatalf("groups = %#v", groups)
	}

	snapshots["b"] = accountBalanceSnapshot{CacheKey: sharedKey, CheckedAt: now.Add(-time.Minute)}
	if groups := dueAccountBalanceRefreshGroups(works, snapshots, now); len(groups) != 0 {
		t.Fatalf("fresh groups = %#v", groups)
	}

	snapshots["a"] = accountBalanceSnapshot{CacheKey: sharedKey, CheckedAt: now.Add(-balanceSnapshotMaxAge)}
	if groups := dueAccountBalanceRefreshGroups(works, snapshots, now); len(groups) != 1 || groups[0].Key != sharedKey || len(groups[0].Works) != 2 {
		t.Fatalf("stale groups = %#v", groups)
	}
}

func TestDueAccountBalanceRefreshGroupsSeparatesDifferentCredentials(t *testing.T) {
	now := time.Now().UTC()
	sharedURL := "https://usage.example/v1"
	works := []accountBalanceWork{
		{ID: "a", OwnerID: "owner", SourceType: "sub2api", ObservedSourceBaseURL: &sharedURL, ObservedSourceCredentialFingerprint: "key-a"},
		{ID: "b", OwnerID: "owner", SourceType: "sub2api", ObservedSourceBaseURL: &sharedURL, ObservedSourceCredentialFingerprint: "key-b"},
	}
	groups := dueAccountBalanceRefreshGroups(works, map[string]accountBalanceSnapshot{}, now)
	if len(groups) != 2 || len(groups[0].Works) != 1 || len(groups[1].Works) != 1 {
		t.Fatalf("groups = %#v", groups)
	}
}

func TestAccountBalanceCacheKeyKeepsProvidersSeparate(t *testing.T) {
	sharedURL := "https://api.example/v1"
	usage := accountBalanceWork{ID: "a", OwnerID: "owner", SourceType: "sub2api", ObservedSourceBaseURL: &sharedURL, ObservedSourceCredentialFingerprint: "same-key"}
	newAPI := accountBalanceWork{ID: "b", OwnerID: "owner", SourceType: "newapi", SourceBaseURL: &sharedURL, SourceCredentialFingerprint: "same-key"}
	if accountBalanceCacheKey(usage) == accountBalanceCacheKey(newAPI) {
		t.Fatal("different balance protocols must not share a cache key")
	}
}

func TestAccountBalanceCacheKeyReusesNewAPIURLAndPlaintextCredential(t *testing.T) {
	sharedURL := "https://api.example/v1"
	first := accountBalanceWork{ID: "a", OwnerID: "owner", SourceType: "newapi", SourceBaseURL: &sharedURL, SourceCredentialFingerprint: balanceCredentialFingerprint("token")}
	second := accountBalanceWork{ID: "b", OwnerID: "owner", SourceType: "newapi", SourceBaseURL: &sharedURL, SourceCredentialFingerprint: balanceCredentialFingerprint(" token ")}
	if accountBalanceCacheKey(first) != accountBalanceCacheKey(second) {
		t.Fatal("same NewAPI URL and credential must share a cache key")
	}
	second.SourceCredentialFingerprint = balanceCredentialFingerprint("other-token")
	if accountBalanceCacheKey(first) == accountBalanceCacheKey(second) {
		t.Fatal("same NewAPI URL with different credentials must not share a cache key")
	}
}
