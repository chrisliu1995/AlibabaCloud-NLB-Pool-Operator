package provider

import (
	"context"
	"testing"

	"golang.org/x/time/rate"
)

func TestRateLimiter_RejectsWhenBucketExhausted(t *testing.T) {
	mock := NewMockNLBClient()
	rl := NewRateLimitedClient(mock)
	// QPS ~0 with burst 2: exactly 2 calls pass, the 3rd is rejected locally.
	rl.AddServersLimiter = rate.NewLimiter(rate.Limit(0.001), 2)

	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := rl.AddServersToServerGroup(ctx, &AddServersRequest{ServerGroupId: "sg-1"}); err != nil {
			t.Fatalf("call %d within burst should pass, got: %v", i, err)
		}
	}
	_, err := rl.AddServersToServerGroup(ctx, &AddServersRequest{ServerGroupId: "sg-1"})
	if err == nil {
		t.Fatal("expected local rate limit rejection on 3rd call")
	}
	if !IsLocalRateLimited(err) {
		t.Errorf("expected ErrLocalRateLimited sentinel, got: %v", err)
	}
	// Rejected call must never reach the cloud client.
	if len(mock.AddServersToServerGroupCalls) != 2 {
		t.Errorf("expected exactly 2 calls to reach mock, got %d", len(mock.AddServersToServerGroupCalls))
	}
}

func TestRateLimiter_ZeroBurstRejectsAllCalls(t *testing.T) {
	mock := NewMockNLBClient()
	rl := NewRateLimitedClient(mock)
	rl.AddServersLimiter = rate.NewLimiter(rate.Limit(30), 0)

	_, err := rl.AddServersToServerGroup(context.Background(), &AddServersRequest{ServerGroupId: "sg-1"})
	if !IsLocalRateLimited(err) {
		t.Fatalf("zero burst must reject the call locally, got: %v", err)
	}
	if len(mock.AddServersToServerGroupCalls) != 0 {
		t.Fatalf("zero burst must not reach the cloud client, got %d calls", len(mock.AddServersToServerGroupCalls))
	}
}

func TestRateLimiter_NilLimiterPassesThrough(t *testing.T) {
	mock := NewMockNLBClient()
	rl := NewRateLimitedClient(mock)
	// No limiter configured for any interface: all calls pass through.

	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if _, err := rl.CreateServerGroup(ctx, &CreateServerGroupRequest{ServerGroupName: "sg"}); err != nil {
			t.Fatalf("nil limiter must pass through, call %d got: %v", i, err)
		}
	}
	if len(mock.CreateServerGroupCalls) != 10 {
		t.Errorf("expected 10 calls to reach mock, got %d", len(mock.CreateServerGroupCalls))
	}
}

func TestRateLimiter_PerInterfaceIsolation(t *testing.T) {
	mock := NewMockNLBClient()
	rl := NewRateLimitedClient(mock)
	// Exhaust only the AddServers limiter; RemoveServers must stay unaffected.
	rl.AddServersLimiter = rate.NewLimiter(rate.Limit(0.001), 1)
	rl.RemoveServersLimiter = rate.NewLimiter(rate.Limit(0.001), 5)

	ctx := context.Background()
	if _, err := rl.AddServersToServerGroup(ctx, &AddServersRequest{}); err != nil {
		t.Fatalf("first AddServers should pass: %v", err)
	}
	if _, err := rl.AddServersToServerGroup(ctx, &AddServersRequest{}); !IsLocalRateLimited(err) {
		t.Errorf("second AddServers should be locally rate limited, got: %v", err)
	}
	// RemoveServers uses its own bucket and must still pass.
	if _, err := rl.RemoveServersFromServerGroup(ctx, &RemoveServersRequest{}); err != nil {
		t.Errorf("RemoveServers must be unaffected by AddServers bucket: %v", err)
	}
}

func TestIsLocalRateLimited_OnlyMatchesSentinel(t *testing.T) {
	if !IsLocalRateLimited(ErrLocalRateLimited) {
		t.Error("sentinel must match itself")
	}
	if IsLocalRateLimited(context.Canceled) {
		t.Error("unrelated error must not match sentinel")
	}
	if IsLocalRateLimited(nil) {
		t.Error("nil must not match sentinel")
	}
}
