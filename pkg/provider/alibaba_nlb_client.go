// Package provider implements the AlibabaNLBClient — the concrete OpenAPI
// implementation of the NLBAPIClient interface using the Alibaba Cloud NLB SDK v4.
package provider

import (
	"context"
	"fmt"
	"strings"

	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	nlbsdk "github.com/alibabacloud-go/nlb-20220430/v4/client"
	"github.com/alibabacloud-go/tea/tea"
)

// AlibabaNLBClient implements NLBAPIClient by delegating to the Alibaba Cloud
// NLB SDK v4 client.
type AlibabaNLBClient struct {
	client *nlbsdk.Client
	region string
}

// Compile-time check that AlibabaNLBClient implements NLBAPIClient.
var _ NLBAPIClient = (*AlibabaNLBClient)(nil)

// NewAlibabaNLBClient creates a new AlibabaNLBClient using AccessKey credentials.
func NewAlibabaNLBClient(accessKeyId, accessKeySecret, region string) (*AlibabaNLBClient, error) {
	config := &openapi.Config{
		AccessKeyId:     tea.String(accessKeyId),
		AccessKeySecret: tea.String(accessKeySecret),
		RegionId:        tea.String(region),
		Endpoint:        tea.String("nlb." + region + ".aliyuncs.com"),
	}

	client, err := nlbsdk.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create NLB SDK client: %w", err)
	}

	return &AlibabaNLBClient{client: client, region: region}, nil
}

// CreateServerGroup creates an empty IP-type ServerGroup.
func (c *AlibabaNLBClient) CreateServerGroup(ctx context.Context, req *CreateServerGroupRequest) (*CreateServerGroupResponse, error) {
	request := &nlbsdk.CreateServerGroupRequest{
		VpcId:           tea.String(req.VpcId),
		ServerGroupName: tea.String(req.ServerGroupName),
		ServerGroupType: tea.String(req.ServerGroupType),
		Protocol:        tea.String(req.Protocol),
		Scheduler:       tea.String(req.Scheduler),
		ClientToken:     tea.String(req.ClientToken),
		RegionId:        tea.String(c.region),
	}

	resp, err := c.client.CreateServerGroup(request)
	if err != nil {
		return nil, fmt.Errorf("CreateServerGroup API error: %w", err)
	}
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("CreateServerGroup: nil response body")
	}

	return &CreateServerGroupResponse{
		ServerGroupId: tea.StringValue(resp.Body.ServerGroupId),
		JobId:         tea.StringValue(resp.Body.JobId),
	}, nil
}

// CreateListener creates a Listener on a specific NLB instance.
func (c *AlibabaNLBClient) CreateListener(ctx context.Context, req *CreateListenerRequest) (*CreateListenerResponse, error) {
	request := &nlbsdk.CreateListenerRequest{
		LoadBalancerId:   tea.String(req.LoadBalancerId),
		ListenerProtocol: tea.String(req.ListenerProtocol),
		ListenerPort:     tea.Int32(req.ListenerPort),
		ServerGroupId:    tea.String(req.ServerGroupId),
		ClientToken:      tea.String(req.ClientToken),
		RegionId:         tea.String(c.region),
	}

	resp, err := c.client.CreateListener(request)
	if err != nil {
		return nil, fmt.Errorf("CreateListener API error: %w", err)
	}
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("CreateListener: nil response body")
	}

	return &CreateListenerResponse{
		ListenerId: tea.StringValue(resp.Body.ListenerId),
		JobId:      tea.StringValue(resp.Body.JobId),
	}, nil
}

// AddServersToServerGroup attaches backend servers to the ServerGroup.
func (c *AlibabaNLBClient) AddServersToServerGroup(ctx context.Context, req *AddServersRequest) (*AddServersResponse, error) {
	servers := make([]*nlbsdk.AddServersToServerGroupRequestServers, 0, len(req.Servers))
	for _, s := range req.Servers {
		servers = append(servers, &nlbsdk.AddServersToServerGroupRequestServers{
			ServerType: tea.String(s.ServerType),
			ServerId:   tea.String(s.ServerId),
			ServerIp:   tea.String(s.ServerIp),
			Port:       tea.Int32(s.Port),
			Weight:     tea.Int32(s.Weight),
		})
	}

	request := &nlbsdk.AddServersToServerGroupRequest{
		ServerGroupId: tea.String(req.ServerGroupId),
		Servers:       servers,
		ClientToken:   tea.String(req.ClientToken),
		RegionId:      tea.String(c.region),
	}

	resp, err := c.client.AddServersToServerGroup(request)
	if err != nil {
		return nil, fmt.Errorf("AddServersToServerGroup API error: %w", err)
	}
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("AddServersToServerGroup: nil response body")
	}

	return &AddServersResponse{
		JobId: tea.StringValue(resp.Body.JobId),
	}, nil
}

// RemoveServersFromServerGroup detaches backend servers from the ServerGroup.
func (c *AlibabaNLBClient) RemoveServersFromServerGroup(ctx context.Context, req *RemoveServersRequest) (*RemoveServersResponse, error) {
	servers := make([]*nlbsdk.RemoveServersFromServerGroupRequestServers, 0, len(req.Servers))
	for _, s := range req.Servers {
		servers = append(servers, &nlbsdk.RemoveServersFromServerGroupRequestServers{
			ServerType: tea.String(s.ServerType),
			ServerId:   tea.String(s.ServerId),
			ServerIp:   tea.String(s.ServerIp),
			Port:       tea.Int32(s.Port),
		})
	}

	request := &nlbsdk.RemoveServersFromServerGroupRequest{
		ServerGroupId: tea.String(req.ServerGroupId),
		Servers:       servers,
		ClientToken:   tea.String(req.ClientToken),
		RegionId:      tea.String(c.region),
	}

	resp, err := c.client.RemoveServersFromServerGroup(request)
	if err != nil {
		return nil, fmt.Errorf("RemoveServersFromServerGroup API error: %w", err)
	}
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("RemoveServersFromServerGroup: nil response body")
	}

	return &RemoveServersResponse{
		JobId: tea.StringValue(resp.Body.JobId),
	}, nil
}

// ListServerGroupServers returns the backend servers registered in a ServerGroup.
func (c *AlibabaNLBClient) ListServerGroupServers(ctx context.Context, serverGroupId string) ([]BackendServer, error) {
	request := &nlbsdk.ListServerGroupServersRequest{
		ServerGroupId: tea.String(serverGroupId),
		MaxResults:    tea.Int32(100),
		RegionId:      tea.String(c.region),
	}

	resp, err := c.client.ListServerGroupServers(request)
	if err != nil {
		return nil, fmt.Errorf("ListServerGroupServers API error: %w", err)
	}
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("ListServerGroupServers: nil response body")
	}

	var servers []BackendServer
	for _, s := range resp.Body.Servers {
		servers = append(servers, BackendServer{
			ServerType: tea.StringValue(s.ServerType),
			ServerId:   tea.StringValue(s.ServerId),
			ServerIp:   tea.StringValue(s.ServerIp),
			Port:       tea.Int32Value(s.Port),
		})
	}
	return servers, nil
}

// GetJobStatus polls the asynchronous job status.
func (c *AlibabaNLBClient) GetJobStatus(ctx context.Context, jobId string) (string, error) {
	request := &nlbsdk.GetJobStatusRequest{
		JobId: tea.String(jobId),
	}

	resp, err := c.client.GetJobStatus(request)
	if err != nil {
		return "", fmt.Errorf("GetJobStatus API error: %w", err)
	}
	if resp == nil || resp.Body == nil {
		return "", fmt.Errorf("GetJobStatus: nil response body")
	}

	return tea.StringValue(resp.Body.Status), nil
}

// ListServerGroups queries existing ServerGroups by VpcId and optional names.
func (c *AlibabaNLBClient) ListServerGroups(ctx context.Context, req *ListServerGroupsRequest) (*ListServerGroupsResponse, error) {
	names := make([]*string, 0, len(req.ServerGroupNames))
	for _, n := range req.ServerGroupNames {
		names = append(names, tea.String(n))
	}
	request := &nlbsdk.ListServerGroupsRequest{
		RegionId: tea.String(c.region),
	}
	if req.VpcId != "" {
		request.VpcId = tea.String(req.VpcId)
	}
	if len(names) > 0 {
		request.ServerGroupNames = names
	}

	resp, err := c.client.ListServerGroups(request)
	if err != nil {
		return nil, fmt.Errorf("ListServerGroups API error: %w", err)
	}
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("ListServerGroups: nil response body")
	}

	out := &ListServerGroupsResponse{}
	for _, sg := range resp.Body.ServerGroups {
		if sg == nil {
			continue
		}
		out.ServerGroups = append(out.ServerGroups, ServerGroupSummary{
			ServerGroupId:   tea.StringValue(sg.ServerGroupId),
			ServerGroupName: tea.StringValue(sg.ServerGroupName),
		})
	}
	return out, nil
}

// ListListeners queries existing Listeners on a specific NLB.
func (c *AlibabaNLBClient) ListListeners(ctx context.Context, req *ListListenersRequest) (*ListListenersResponse, error) {
	request := &nlbsdk.ListListenersRequest{
		RegionId: tea.String(c.region),
	}
	if req.LoadBalancerId != "" {
		request.LoadBalancerIds = []*string{tea.String(req.LoadBalancerId)}
	}
	if req.ListenerProtocol != "" {
		request.ListenerProtocol = tea.String(req.ListenerProtocol)
	}

	resp, err := c.client.ListListeners(request)
	if err != nil {
		return nil, fmt.Errorf("ListListeners API error: %w", err)
	}
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("ListListeners: nil response body")
	}

	out := &ListListenersResponse{}
	for _, l := range resp.Body.Listeners {
		if l == nil {
			continue
		}
		out.Listeners = append(out.Listeners, ListenerSummary{
			ListenerId:       tea.StringValue(l.ListenerId),
			ListenerPort:     tea.Int32Value(l.ListenerPort),
			ListenerProtocol: tea.StringValue(l.ListenerProtocol),
			LoadBalancerId:   tea.StringValue(l.LoadBalancerId),
		})
	}
	return out, nil
}

// GetServerGroupAttribute fetches a server group's current attributes by ID.
// Returns (nil, nil) when the server group does not exist on the cloud.
func (c *AlibabaNLBClient) GetServerGroupAttribute(ctx context.Context, serverGroupId string) (*ServerGroupAttribute, error) {
	if serverGroupId == "" {
		return nil, nil
	}

	request := &nlbsdk.ListServerGroupsRequest{
		ServerGroupIds: []*string{tea.String(serverGroupId)},
		RegionId:       tea.String(c.region),
	}
	resp, err := c.client.ListServerGroups(request)
	if err != nil {
		if IsNotFoundError(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("GetServerGroupAttribute API error (id=%s): %w", serverGroupId, err)
	}
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("GetServerGroupAttribute: nil response body")
	}
	for _, sg := range resp.Body.ServerGroups {
		if sg == nil {
			continue
		}
		if tea.StringValue(sg.ServerGroupId) == serverGroupId {
			return &ServerGroupAttribute{
				ServerGroupId:     tea.StringValue(sg.ServerGroupId),
				ServerGroupName:   tea.StringValue(sg.ServerGroupName),
				ServerGroupStatus: tea.StringValue(sg.ServerGroupStatus),
				VpcId:             tea.StringValue(sg.VpcId),
			}, nil
		}
	}
	return nil, nil
}

// DeleteServerGroup deletes a backend server group by ID.
// Returns nil if the server group does not exist (already deleted).
func (c *AlibabaNLBClient) DeleteServerGroup(ctx context.Context, serverGroupId string) error {
	if serverGroupId == "" {
		return nil
	}
	request := &nlbsdk.DeleteServerGroupRequest{
		ServerGroupId: tea.String(serverGroupId),
		RegionId:      tea.String(c.region),
	}
	_, err := c.client.DeleteServerGroup(request)
	if err != nil {
		if IsNotFoundError(err) {
			return nil
		}
		return fmt.Errorf("DeleteServerGroup API error (id=%s): %w", serverGroupId, err)
	}
	return nil
}

// GetListenerAttribute fetches a listener's current attributes by ID.
// Returns (nil, nil) when the listener does not exist on the cloud.
func (c *AlibabaNLBClient) GetListenerAttribute(ctx context.Context, listenerId string) (*ListenerAttribute, error) {
	if listenerId == "" {
		return nil, nil
	}
	request := &nlbsdk.GetListenerAttributeRequest{
		ListenerId: tea.String(listenerId),
	}
	resp, err := c.client.GetListenerAttribute(request)
	if err != nil {
		if IsNotFoundError(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("GetListenerAttribute API error (id=%s): %w", listenerId, err)
	}
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("GetListenerAttribute: nil response body")
	}
	body := resp.Body
	return &ListenerAttribute{
		ListenerId:       tea.StringValue(body.ListenerId),
		ListenerStatus:   tea.StringValue(body.ListenerStatus),
		ListenerPort:     tea.Int32Value(body.ListenerPort),
		ListenerProtocol: tea.StringValue(body.ListenerProtocol),
		LoadBalancerId:   tea.StringValue(body.LoadBalancerId),
		ServerGroupId:    tea.StringValue(body.ServerGroupId),
	}, nil
}

// DeleteListener deletes a listener by ID.
// Returns nil if the listener does not exist (already deleted).
func (c *AlibabaNLBClient) DeleteListener(ctx context.Context, listenerId string) error {
	if listenerId == "" {
		return nil
	}
	request := &nlbsdk.DeleteListenerRequest{
		ListenerId: tea.String(listenerId),
	}
	_, err := c.client.DeleteListener(request)
	if err != nil {
		if IsNotFoundError(err) {
			return nil
		}
		return fmt.Errorf("DeleteListener API error (id=%s): %w", listenerId, err)
	}
	return nil
}

// ListServerGroupsByName looks up a server group ID by VPC and name.
// Returns "" when no matching server group exists.
func (c *AlibabaNLBClient) ListServerGroupsByName(ctx context.Context, vpcId, serverGroupName string) (string, error) {
	if serverGroupName == "" {
		return "", nil
	}
	request := &nlbsdk.ListServerGroupsRequest{
		ServerGroupNames: []*string{tea.String(serverGroupName)},
		RegionId:         tea.String(c.region),
	}
	if vpcId != "" {
		request.VpcId = tea.String(vpcId)
	}

	for {
		resp, err := c.client.ListServerGroups(request)
		if err != nil {
			if IsNotFoundError(err) {
				return "", nil
			}
			return "", fmt.Errorf("ListServerGroupsByName API error (name=%s): %w", serverGroupName, err)
		}
		if resp == nil || resp.Body == nil {
			return "", fmt.Errorf("ListServerGroupsByName: nil response body")
		}
		for _, sg := range resp.Body.ServerGroups {
			if sg == nil {
				continue
			}
			if tea.StringValue(sg.ServerGroupName) != serverGroupName {
				continue
			}
			if vpcId != "" && tea.StringValue(sg.VpcId) != vpcId {
				continue
			}
			return tea.StringValue(sg.ServerGroupId), nil
		}
		next := tea.StringValue(resp.Body.NextToken)
		if next == "" {
			return "", nil
		}
		request.NextToken = tea.String(next)
	}
}

// ListListenersByPort looks up a listener ID by NLB ID and port.
// Returns "" when no matching listener exists.
func (c *AlibabaNLBClient) ListListenersByPort(ctx context.Context, loadBalancerId string, listenerPort int32) (string, error) {
	if loadBalancerId == "" {
		return "", nil
	}
	request := &nlbsdk.ListListenersRequest{
		LoadBalancerIds: []*string{tea.String(loadBalancerId)},
		RegionId:        tea.String(c.region),
	}

	for {
		resp, err := c.client.ListListeners(request)
		if err != nil {
			if IsNotFoundError(err) {
				return "", nil
			}
			return "", fmt.Errorf("ListListenersByPort API error (nlb=%s, port=%d): %w", loadBalancerId, listenerPort, err)
		}
		if resp == nil || resp.Body == nil {
			return "", fmt.Errorf("ListListenersByPort: nil response body")
		}
		for _, lsn := range resp.Body.Listeners {
			if lsn == nil {
				continue
			}
			if tea.StringValue(lsn.LoadBalancerId) != loadBalancerId {
				continue
			}
			if tea.Int32Value(lsn.ListenerPort) == listenerPort {
				return tea.StringValue(lsn.ListenerId), nil
			}
		}
		next := tea.StringValue(resp.Body.NextToken)
		if next == "" {
			return "", nil
		}
		request.NextToken = tea.String(next)
	}
}

// GetLoadBalancerEIP returns the first non-empty public IPv4 address (EIP)
// across all zone mappings of the given NLB instance.
func (c *AlibabaNLBClient) GetLoadBalancerEIP(ctx context.Context, loadBalancerId string) (string, error) {
	if loadBalancerId == "" {
		return "", fmt.Errorf("GetLoadBalancerEIP: loadBalancerId is empty")
	}
	request := &nlbsdk.GetLoadBalancerAttributeRequest{
		LoadBalancerId: tea.String(loadBalancerId),
		RegionId:       tea.String(c.region),
	}
	resp, err := c.client.GetLoadBalancerAttribute(request)
	if err != nil {
		return "", fmt.Errorf("GetLoadBalancerAttribute API error: %w", err)
	}
	if resp == nil || resp.Body == nil {
		return "", fmt.Errorf("GetLoadBalancerAttribute: nil response body")
	}
	for _, zm := range resp.Body.ZoneMappings {
		if zm == nil {
			continue
		}
		for _, addr := range zm.LoadBalancerAddresses {
			if addr == nil {
				continue
			}
			if ip := tea.StringValue(addr.PublicIPv4Address); ip != "" {
				return ip, nil
			}
		}
	}
	return "", nil
}

// LoadBalancerExists checks whether a cloud NLB instance still exists by
// calling GetLoadBalancerAttribute. Returns false when the API returns
// ResourceNotFound, true when the NLB is found (including Deleting state).
func (c *AlibabaNLBClient) LoadBalancerExists(ctx context.Context, loadBalancerId string) (bool, error) {
	if loadBalancerId == "" {
		return false, nil
	}
	request := &nlbsdk.GetLoadBalancerAttributeRequest{
		LoadBalancerId: tea.String(loadBalancerId),
		RegionId:       tea.String(c.region),
	}
	_, err := c.client.GetLoadBalancerAttribute(request)
	if err != nil {
		errMsg := err.Error()
		if strings.Contains(errMsg, "ResourceNotFound") || strings.Contains(errMsg, "InvalidResourceId") {
			return false, nil
		}
		return false, fmt.Errorf("GetLoadBalancerAttribute API error: %w", err)
	}
	return true, nil
}
