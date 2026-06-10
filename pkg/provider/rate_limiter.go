package provider

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/time/rate"
)

// ErrLocalRateLimited is returned when a per-interface token-bucket rejects
// a call, before any cloud API invocation is attempted.
var ErrLocalRateLimited = fmt.Errorf("local rate limited")

// IsLocalRateLimited reports whether err is the local-token-bucket sentinel.
func IsLocalRateLimited(err error) bool {
	return errors.Is(err, ErrLocalRateLimited)
}

// RateLimitedClient wraps an NLBAPIClient with per-interface token-bucket
// rate limiters. Each API method has its own limiter configured independently.
// When a limiter rejects a call (Allow() returns false), ErrLocalRateLimited
// is returned immediately without making the cloud API call. A nil limiter
// means the corresponding interface is not rate-limited (pass-through).
type RateLimitedClient struct {
	client NLBAPIClient

	// Per-interface limiters. nil means no limit for that interface.
	AddServersLimiter     *rate.Limiter
	RemoveServersLimiter  *rate.Limiter
	CreateSGLimiter       *rate.Limiter
	CreateListenerLimiter *rate.Limiter
	ListSGLimiter         *rate.Limiter
	GetJobLimiter         *rate.Limiter
	ListListenersLimiter  *rate.Limiter
	GetEIPLimiter         *rate.Limiter
	GetSGAttrLimiter      *rate.Limiter
	DeleteSGLimiter       *rate.Limiter
	GetListenerLimiter    *rate.Limiter
	DeleteListenerLimiter *rate.Limiter
}

// NewRateLimitedClient builds a RateLimitedClient that, by default, does not
// throttle any interface. Callers populate the per-interface limiter fields
// explicitly for the APIs that require limiting.
func NewRateLimitedClient(client NLBAPIClient) *RateLimitedClient {
	return &RateLimitedClient{client: client}
}

// checkLimit returns ErrLocalRateLimited when a non-nil limiter rejects the
// call; nil limiters always pass through.
func checkLimit(limiter *rate.Limiter) error {
	if limiter != nil && !limiter.Allow() {
		return ErrLocalRateLimited
	}
	return nil
}

func (r *RateLimitedClient) AddServersToServerGroup(ctx context.Context, req *AddServersRequest) (*AddServersResponse, error) {
	if err := checkLimit(r.AddServersLimiter); err != nil {
		return nil, err
	}
	return r.client.AddServersToServerGroup(ctx, req)
}

func (r *RateLimitedClient) RemoveServersFromServerGroup(ctx context.Context, req *RemoveServersRequest) (*RemoveServersResponse, error) {
	if err := checkLimit(r.RemoveServersLimiter); err != nil {
		return nil, err
	}
	return r.client.RemoveServersFromServerGroup(ctx, req)
}

func (r *RateLimitedClient) CreateServerGroup(ctx context.Context, req *CreateServerGroupRequest) (*CreateServerGroupResponse, error) {
	if err := checkLimit(r.CreateSGLimiter); err != nil {
		return nil, err
	}
	return r.client.CreateServerGroup(ctx, req)
}

func (r *RateLimitedClient) CreateListener(ctx context.Context, req *CreateListenerRequest) (*CreateListenerResponse, error) {
	if err := checkLimit(r.CreateListenerLimiter); err != nil {
		return nil, err
	}
	return r.client.CreateListener(ctx, req)
}

func (r *RateLimitedClient) ListServerGroupServers(ctx context.Context, serverGroupId string) ([]BackendServer, error) {
	if err := checkLimit(r.ListSGLimiter); err != nil {
		return nil, err
	}
	return r.client.ListServerGroupServers(ctx, serverGroupId)
}

func (r *RateLimitedClient) ListServerGroups(ctx context.Context, req *ListServerGroupsRequest) (*ListServerGroupsResponse, error) {
	if err := checkLimit(r.ListSGLimiter); err != nil {
		return nil, err
	}
	return r.client.ListServerGroups(ctx, req)
}

func (r *RateLimitedClient) ListListeners(ctx context.Context, req *ListListenersRequest) (*ListListenersResponse, error) {
	if err := checkLimit(r.ListListenersLimiter); err != nil {
		return nil, err
	}
	return r.client.ListListeners(ctx, req)
}

func (r *RateLimitedClient) GetJobStatus(ctx context.Context, jobId string) (string, error) {
	if err := checkLimit(r.GetJobLimiter); err != nil {
		return "", err
	}
	return r.client.GetJobStatus(ctx, jobId)
}

func (r *RateLimitedClient) GetLoadBalancerEIP(ctx context.Context, loadBalancerId string) (string, error) {
	if err := checkLimit(r.GetEIPLimiter); err != nil {
		return "", err
	}
	return r.client.GetLoadBalancerEIP(ctx, loadBalancerId)
}

func (r *RateLimitedClient) GetServerGroupAttribute(ctx context.Context, serverGroupId string) (*ServerGroupAttribute, error) {
	if err := checkLimit(r.GetSGAttrLimiter); err != nil {
		return nil, err
	}
	return r.client.GetServerGroupAttribute(ctx, serverGroupId)
}

func (r *RateLimitedClient) DeleteServerGroup(ctx context.Context, serverGroupId string) error {
	if err := checkLimit(r.DeleteSGLimiter); err != nil {
		return err
	}
	return r.client.DeleteServerGroup(ctx, serverGroupId)
}

func (r *RateLimitedClient) GetListenerAttribute(ctx context.Context, listenerId string) (*ListenerAttribute, error) {
	if err := checkLimit(r.GetListenerLimiter); err != nil {
		return nil, err
	}
	return r.client.GetListenerAttribute(ctx, listenerId)
}

func (r *RateLimitedClient) DeleteListener(ctx context.Context, listenerId string) error {
	if err := checkLimit(r.DeleteListenerLimiter); err != nil {
		return err
	}
	return r.client.DeleteListener(ctx, listenerId)
}

func (r *RateLimitedClient) ListServerGroupsByName(ctx context.Context, vpcId, serverGroupName string) (string, error) {
	if err := checkLimit(r.ListSGLimiter); err != nil {
		return "", err
	}
	return r.client.ListServerGroupsByName(ctx, vpcId, serverGroupName)
}

func (r *RateLimitedClient) ListListenersByPort(ctx context.Context, loadBalancerId string, listenerPort int32) (string, error) {
	if err := checkLimit(r.ListListenersLimiter); err != nil {
		return "", err
	}
	return r.client.ListListenersByPort(ctx, loadBalancerId, listenerPort)
}

// Compile-time check that RateLimitedClient implements NLBAPIClient.
var _ NLBAPIClient = (*RateLimitedClient)(nil)
