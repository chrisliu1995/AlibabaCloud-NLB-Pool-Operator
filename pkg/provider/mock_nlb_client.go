package provider

import (
	"context"
	"sync"
)

// MockNLBClient is a mock implementation of NLBAPIClient for unit testing.
type MockNLBClient struct {
	mu sync.Mutex

	CreateServerGroupFunc            func(ctx context.Context, req *CreateServerGroupRequest) (*CreateServerGroupResponse, error)
	CreateListenerFunc               func(ctx context.Context, req *CreateListenerRequest) (*CreateListenerResponse, error)
	AddServersToServerGroupFunc      func(ctx context.Context, req *AddServersRequest) (*AddServersResponse, error)
	RemoveServersFromServerGroupFunc func(ctx context.Context, req *RemoveServersRequest) (*RemoveServersResponse, error)
	GetJobStatusFunc                 func(ctx context.Context, jobId string) (string, error)
	ListServerGroupsFunc             func(ctx context.Context, req *ListServerGroupsRequest) (*ListServerGroupsResponse, error)
	ListListenersFunc                func(ctx context.Context, req *ListListenersRequest) (*ListListenersResponse, error)
	GetLoadBalancerEIPFunc           func(ctx context.Context, loadBalancerId string) (string, error)
	GetServerGroupAttributeFunc      func(ctx context.Context, serverGroupId string) (*ServerGroupAttribute, error)
	DeleteServerGroupFunc            func(ctx context.Context, serverGroupId string) error
	GetListenerAttributeFunc         func(ctx context.Context, listenerId string) (*ListenerAttribute, error)
	DeleteListenerFunc               func(ctx context.Context, listenerId string) error
	ListServerGroupsByNameFunc       func(ctx context.Context, vpcId, serverGroupName string) (string, error)
	ListListenersByPortFunc          func(ctx context.Context, loadBalancerId string, listenerPort int32) (string, error)
	LoadBalancerExistsFunc           func(ctx context.Context, loadBalancerId string) (bool, error)

	CreateServerGroupCalls            []CreateServerGroupRequest
	CreateListenerCalls               []CreateListenerRequest
	AddServersToServerGroupCalls      []AddServersRequest
	RemoveServersFromServerGroupCalls []RemoveServersRequest
	GetJobStatusCalls                 []string
}

func NewMockNLBClient() *MockNLBClient {
	return &MockNLBClient{}
}

func NewMockNLBClientWithAsyncJobs(succeedAfter int) *MockNLBClient {
	jobCounter := map[string]int{}
	var jmu sync.Mutex

	m := &MockNLBClient{}
	m.CreateServerGroupFunc = func(ctx context.Context, req *CreateServerGroupRequest) (*CreateServerGroupResponse, error) {
		id := "sg-mock-" + req.ServerGroupName
		return &CreateServerGroupResponse{
			ServerGroupId: id,
			JobId:         "job-create-sg-" + req.ServerGroupName,
		}, nil
	}
	m.CreateListenerFunc = func(ctx context.Context, req *CreateListenerRequest) (*CreateListenerResponse, error) {
		id := "lsn-mock-" + req.ClientToken
		return &CreateListenerResponse{
			ListenerId: id,
			JobId:      "job-create-listener-" + req.ClientToken,
		}, nil
	}
	m.AddServersToServerGroupFunc = func(ctx context.Context, req *AddServersRequest) (*AddServersResponse, error) {
		return &AddServersResponse{
			JobId: "job-add-servers-" + req.ServerGroupId,
		}, nil
	}
	m.RemoveServersFromServerGroupFunc = func(ctx context.Context, req *RemoveServersRequest) (*RemoveServersResponse, error) {
		return &RemoveServersResponse{
			JobId: "job-remove-servers-" + req.ServerGroupId,
		}, nil
	}
	m.GetJobStatusFunc = func(ctx context.Context, jobId string) (string, error) {
		jmu.Lock()
		defer jmu.Unlock()
		jobCounter[jobId]++
		if jobCounter[jobId] >= succeedAfter {
			return JobStatusSucceeded, nil
		}
		return JobStatusProcessing, nil
	}
	return m
}

func (m *MockNLBClient) CreateServerGroup(ctx context.Context, req *CreateServerGroupRequest) (*CreateServerGroupResponse, error) {
	m.mu.Lock()
	m.CreateServerGroupCalls = append(m.CreateServerGroupCalls, *req)
	m.mu.Unlock()
	if m.CreateServerGroupFunc != nil {
		return m.CreateServerGroupFunc(ctx, req)
	}
	return &CreateServerGroupResponse{ServerGroupId: "sg-default-" + req.ServerGroupName, JobId: ""}, nil
}

func (m *MockNLBClient) CreateListener(ctx context.Context, req *CreateListenerRequest) (*CreateListenerResponse, error) {
	m.mu.Lock()
	m.CreateListenerCalls = append(m.CreateListenerCalls, *req)
	m.mu.Unlock()
	if m.CreateListenerFunc != nil {
		return m.CreateListenerFunc(ctx, req)
	}
	return &CreateListenerResponse{ListenerId: "lsn-default-" + req.ClientToken, JobId: ""}, nil
}

func (m *MockNLBClient) AddServersToServerGroup(ctx context.Context, req *AddServersRequest) (*AddServersResponse, error) {
	m.mu.Lock()
	m.AddServersToServerGroupCalls = append(m.AddServersToServerGroupCalls, *req)
	m.mu.Unlock()
	if m.AddServersToServerGroupFunc != nil {
		return m.AddServersToServerGroupFunc(ctx, req)
	}
	return &AddServersResponse{JobId: ""}, nil
}

func (m *MockNLBClient) RemoveServersFromServerGroup(ctx context.Context, req *RemoveServersRequest) (*RemoveServersResponse, error) {
	m.mu.Lock()
	m.RemoveServersFromServerGroupCalls = append(m.RemoveServersFromServerGroupCalls, *req)
	m.mu.Unlock()
	if m.RemoveServersFromServerGroupFunc != nil {
		return m.RemoveServersFromServerGroupFunc(ctx, req)
	}
	return &RemoveServersResponse{JobId: ""}, nil
}

func (m *MockNLBClient) GetJobStatus(ctx context.Context, jobId string) (string, error) {
	m.mu.Lock()
	m.GetJobStatusCalls = append(m.GetJobStatusCalls, jobId)
	m.mu.Unlock()
	if m.GetJobStatusFunc != nil {
		return m.GetJobStatusFunc(ctx, jobId)
	}
	return JobStatusSucceeded, nil
}

func (m *MockNLBClient) ListServerGroupServers(ctx context.Context, serverGroupId string) ([]BackendServer, error) {
	return nil, nil
}

func (m *MockNLBClient) ListServerGroups(ctx context.Context, req *ListServerGroupsRequest) (*ListServerGroupsResponse, error) {
	if m.ListServerGroupsFunc != nil {
		return m.ListServerGroupsFunc(ctx, req)
	}
	return &ListServerGroupsResponse{}, nil
}

func (m *MockNLBClient) ListListeners(ctx context.Context, req *ListListenersRequest) (*ListListenersResponse, error) {
	if m.ListListenersFunc != nil {
		return m.ListListenersFunc(ctx, req)
	}
	return &ListListenersResponse{}, nil
}

func (m *MockNLBClient) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.CreateServerGroupCalls = nil
	m.CreateListenerCalls = nil
	m.AddServersToServerGroupCalls = nil
	m.RemoveServersFromServerGroupCalls = nil
	m.GetJobStatusCalls = nil
}

func (m *MockNLBClient) GetLoadBalancerEIP(ctx context.Context, loadBalancerId string) (string, error) {
	if m.GetLoadBalancerEIPFunc != nil {
		return m.GetLoadBalancerEIPFunc(ctx, loadBalancerId)
	}
	return "", nil
}

func (m *MockNLBClient) GetServerGroupAttribute(ctx context.Context, serverGroupId string) (*ServerGroupAttribute, error) {
	if m.GetServerGroupAttributeFunc != nil {
		return m.GetServerGroupAttributeFunc(ctx, serverGroupId)
	}
	return &ServerGroupAttribute{
		ServerGroupId:     serverGroupId,
		ServerGroupStatus: "Available",
	}, nil
}

func (m *MockNLBClient) DeleteServerGroup(ctx context.Context, serverGroupId string) error {
	if m.DeleteServerGroupFunc != nil {
		return m.DeleteServerGroupFunc(ctx, serverGroupId)
	}
	return nil
}

func (m *MockNLBClient) GetListenerAttribute(ctx context.Context, listenerId string) (*ListenerAttribute, error) {
	if m.GetListenerAttributeFunc != nil {
		return m.GetListenerAttributeFunc(ctx, listenerId)
	}
	return &ListenerAttribute{
		ListenerId:     listenerId,
		ListenerStatus: "Running",
	}, nil
}

func (m *MockNLBClient) DeleteListener(ctx context.Context, listenerId string) error {
	if m.DeleteListenerFunc != nil {
		return m.DeleteListenerFunc(ctx, listenerId)
	}
	return nil
}

func (m *MockNLBClient) ListServerGroupsByName(ctx context.Context, vpcId, serverGroupName string) (string, error) {
	if m.ListServerGroupsByNameFunc != nil {
		return m.ListServerGroupsByNameFunc(ctx, vpcId, serverGroupName)
	}
	return "", nil
}

func (m *MockNLBClient) ListListenersByPort(ctx context.Context, loadBalancerId string, listenerPort int32) (string, error) {
	if m.ListListenersByPortFunc != nil {
		return m.ListListenersByPortFunc(ctx, loadBalancerId, listenerPort)
	}
	return "", nil
}

func (m *MockNLBClient) LoadBalancerExists(ctx context.Context, loadBalancerId string) (bool, error) {
	if m.LoadBalancerExistsFunc != nil {
		return m.LoadBalancerExistsFunc(ctx, loadBalancerId)
	}
	return false, nil
}

var _ NLBAPIClient = (*MockNLBClient)(nil)
