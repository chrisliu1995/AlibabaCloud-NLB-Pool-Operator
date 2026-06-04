// Package provider defines the abstract interface for the underlying cloud
// (Alibaba Cloud NLB) API. The concrete OpenAPI implementation is intentionally
// kept out of this file - the controllers depend only on this interface.
package provider

import (
	"context"
	"strings"
)

// JobStatus enumerates the possible states of an asynchronous NLB API job.
const (
	JobStatusProcessing = "Processing"
	JobStatusSucceeded  = "Succeeded"
	JobStatusFailed     = "Failed"
)

// NLBAPIClient is the abstract interface that wraps the Alibaba Cloud NLB
// OpenAPI calls used by the controllers.
//
// All write APIs return a JobId; callers MUST poll GetJobStatus until the
// result is terminal (Succeeded / Failed). The interface is intentionally
// minimal — only the operations required by the v4 reconciliation flow are
// exposed.
type NLBAPIClient interface {
	// CreateServerGroup creates an empty IP-type ServerGroup that will be
	// shared by every NLB Listener of the same logical port across all ISPs.
	CreateServerGroup(ctx context.Context, req *CreateServerGroupRequest) (*CreateServerGroupResponse, error)

	// CreateListener creates a Listener on a specific NLB instance and binds
	// it to an existing ServerGroup.
	CreateListener(ctx context.Context, req *CreateListenerRequest) (*CreateListenerResponse, error)

	// AddServersToServerGroup attaches one or more backend servers (Pod IP +
	// container port) to the ServerGroup.
	AddServersToServerGroup(ctx context.Context, req *AddServersRequest) (*AddServersResponse, error)

	// RemoveServersFromServerGroup detaches one or more backend servers from
	// the ServerGroup.
	RemoveServersFromServerGroup(ctx context.Context, req *RemoveServersRequest) (*RemoveServersResponse, error)

	// GetJobStatus polls the asynchronous job status. Possible return values:
	// JobStatusProcessing / JobStatusSucceeded / JobStatusFailed.
	GetJobStatus(ctx context.Context, jobId string) (string, error)

	// ListServerGroups queries existing ServerGroups (used for idempotent
	// recovery when CreateServerGroup returns a duplicate-name error).
	ListServerGroups(ctx context.Context, req *ListServerGroupsRequest) (*ListServerGroupsResponse, error)

	// ListListeners queries existing Listeners on an NLB instance (used for
	// idempotent recovery when CreateListener returns Conflict.Port).
	ListListeners(ctx context.Context, req *ListListenersRequest) (*ListListenersResponse, error)

	// GetServerGroupAttribute fetches a server group's current attributes by ID.
	// Returns (nil, nil) when the server group does not exist on the cloud.
	GetServerGroupAttribute(ctx context.Context, serverGroupId string) (*ServerGroupAttribute, error)

	// DeleteServerGroup deletes a server group by ID.
	// Idempotent: returns nil if the server group does not exist (already deleted).
	DeleteServerGroup(ctx context.Context, serverGroupId string) error

	// GetListenerAttribute fetches a listener's current attributes by ID.
	// Returns (nil, nil) when the listener does not exist on the cloud.
	GetListenerAttribute(ctx context.Context, listenerId string) (*ListenerAttribute, error)

	// DeleteListener deletes a listener by ID.
	// Idempotent: returns nil if the listener does not exist (already deleted).
	DeleteListener(ctx context.Context, listenerId string) error

	// ListServerGroupsByName looks up a server group ID by VPC and name.
	// Returns the matching ServerGroupId for idempotent recovery (check before create).
	// Returns "" when no matching server group exists.
	ListServerGroupsByName(ctx context.Context, vpcId, serverGroupName string) (string, error)

	// ListListenersByPort looks up a listener ID by NLB ID and port.
	// Returns the matching ListenerId for idempotent recovery (check before create).
	// Returns "" when no matching listener exists.
	ListListenersByPort(ctx context.Context, loadBalancerId string, listenerPort int32) (string, error)

	// GetLoadBalancerEIP returns the public IPv4 address (EIP) of the given
	// NLB instance. It is used to populate PortAllocation.spec.NLBEndpoints[].EIP
	// so consumers can read the public address from status.externalAddresses.
	// Returns the first non-empty PublicIPv4Address across all zone mappings;
	// returns "" with no error when the address is not yet allocated.
	GetLoadBalancerEIP(ctx context.Context, loadBalancerId string) (string, error)
}

// ----------------------------------------------------------------------------
// Request / Response DTOs
// ----------------------------------------------------------------------------

// CreateServerGroupRequest is the request payload for CreateServerGroup.
type CreateServerGroupRequest struct {
	VpcId           string
	ServerGroupName string
	ServerGroupType string // "Ip"
	Protocol        string // "TCP_UDP"
	Scheduler       string // "Hash"
	ClientToken     string
}

// CreateServerGroupResponse is the response payload for CreateServerGroup.
type CreateServerGroupResponse struct {
	ServerGroupId string
	JobId         string
}

// CreateListenerRequest is the request payload for CreateListener.
type CreateListenerRequest struct {
	LoadBalancerId   string
	ListenerProtocol string // "TCP" / "UDP"
	ListenerPort     int32
	ServerGroupId    string
	ClientToken      string
}

// CreateListenerResponse is the response payload for CreateListener.
type CreateListenerResponse struct {
	ListenerId string
	JobId      string
}

// BackendServer describes a single backend (Pod IP + container port) attached
// to a ServerGroup.
type BackendServer struct {
	ServerType string // "Ip"
	ServerId   string // Pod IP (used as identifier when ServerType=Ip)
	ServerIp   string // Pod IP
	Port       int32  // container port
	Weight     int32  // 100
}

// AddServersRequest is the request payload for AddServersToServerGroup.
type AddServersRequest struct {
	ServerGroupId string
	Servers       []BackendServer
	ClientToken   string
}

// AddServersResponse is the response payload for AddServersToServerGroup.
type AddServersResponse struct {
	JobId string
}

// RemoveServersRequest is the request payload for RemoveServersFromServerGroup.
type RemoveServersRequest struct {
	ServerGroupId string
	Servers       []BackendServer
	ClientToken   string
}

// RemoveServersResponse is the response payload for RemoveServersFromServerGroup.
type RemoveServersResponse struct {
	JobId string
}

// ListServerGroupsRequest is the request payload for ListServerGroups.
type ListServerGroupsRequest struct {
	VpcId            string
	ServerGroupNames []string
}

// ServerGroupSummary is a minimal server group descriptor.
type ServerGroupSummary struct {
	ServerGroupId   string
	ServerGroupName string
}

// ListServerGroupsResponse is the response payload for ListServerGroups.
type ListServerGroupsResponse struct {
	ServerGroups []ServerGroupSummary
}

// ListListenersRequest is the request payload for ListListeners.
type ListListenersRequest struct {
	LoadBalancerId   string
	ListenerProtocol string
}

// ListenerSummary is a minimal listener descriptor.
type ListenerSummary struct {
	ListenerId       string
	ListenerPort     int32
	ListenerProtocol string
	LoadBalancerId   string
}

// ListListenersResponse is the response payload for ListListeners.
type ListListenersResponse struct {
	Listeners []ListenerSummary
}

// ServerGroupAttribute holds the relevant cloud attributes of a server group.
type ServerGroupAttribute struct {
	ServerGroupId     string
	ServerGroupName   string
	ServerGroupStatus string // "Available" means active/ready
	VpcId             string
}

// ListenerAttribute holds the relevant cloud attributes of a listener.
type ListenerAttribute struct {
	ListenerId       string
	ListenerStatus   string // "Running" means active/ready
	ListenerPort     int32
	ListenerProtocol string
	LoadBalancerId   string
	ServerGroupId    string
}

// IsNotFoundError returns true when the underlying Aliyun OpenAPI error indicates
// that the requested resource does not exist.
func IsNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "ResourceNotFound") ||
		strings.Contains(msg, "ServerNotFound") ||
		strings.Contains(msg, "InvalidListenerId.NotFound") ||
		strings.Contains(msg, "InvalidServerGroupId.NotFound") ||
		strings.Contains(msg, "NotFound")
}
