# AlibabaCloud NLB Pool Operator

A Kubernetes operator that manages Alibaba Cloud NLB (Network Load Balancer) resource pools, enabling pre-warming and on-demand scaling of NLB instances and their associated Services for game server workloads.

## Overview

**AlibabaCloud NLB Pool Operator** addresses a critical challenge in game server infrastructure: the long provisioning time of cloud load balancers. By maintaining a warm pool of pre-created NLB instances and Services, it eliminates the cold-start latency when new game servers need load balancer endpoints. The operator works in concert with [OpenKruiseGame](https://github.com/openkruise/kruise-game)'s AutoNLBsV3 plugin, which consumes Services from the pool via label selectors.

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                    NLB Pool Operator                     │
│  (watches NLBPool CR, manages NLB/EIP/Service lifecycle) │
└──────────┬──────────────────┬───────────────────────────┘
           │ creates           │ creates
           ▼                   ▼
┌─────────────────┐  ┌─────────────────┐
│  NLB Operator   │  │  EIP Operator   │
│ (reconciles NLB │  │ (reconciles EIP │
│  CR → cloud NLB)│  │  CR → cloud EIP)│
└────────┬────────┘  └────────┬────────┘
         │                    │
         └───────┬────────────┘
                 │ references
                 ▼
        ┌─────────────────┐
        │  Pre-warmed     │
        │  Services (LB)  │
        └────────┬────────┘
                 │ consumed by
                 ▼
        ┌─────────────────┐
        │  kruise-game    │
        │  V3 Plugin      │
        │ (binds Pod to   │
        │  available Svc) │
        └─────────────────┘
```

- **NLB Pool Operator**: Owns the NLBPool CR and orchestrates NLB, EIP, and Service creation.
- **NLB Operator** ([AlibabaCloud-NLB-Operator](https://github.com/chrisliu1995/AlibabaCloud-NLB-Operator)): Reconciles `NLB` CRs and creates real NLB instances on Alibaba Cloud.
- **EIP Operator** ([AlibabaCloud-EIP-Operator](https://github.com/chrisliu1995/AlibabaCloud-EIP-Operator)): Reconciles `EIP` CRs and allocates Elastic IPs on Alibaba Cloud.
- **kruise-game V3 Plugin**: Binds game server Pods to available Services from the pool by updating the Service selector and label.

## Prerequisites

- Kubernetes 1.24+
- [AlibabaCloud-NLB-Operator](https://github.com/chrisliu1995/AlibabaCloud-NLB-Operator) installed in the cluster
- [AlibabaCloud-EIP-Operator](https://github.com/chrisliu1995/AlibabaCloud-EIP-Operator) installed in the cluster
- [OpenKruiseGame](https://github.com/openkruise/kruise-game) with AutoNLBsV3 plugin enabled

## Installation

```bash
# Install CRDs
kubectl apply -f config/crd/bases/

# Deploy the operator
kubectl apply -f config/rbac/
kubectl apply -f config/manager/
```

## Usage

```yaml
apiVersion: nlbpool.kruise.io/v1alpha1
kind: NLBPool
metadata:
  name: game-nlb-pool
  namespace: default
spec:
  # VPC and zone mapping: "vpc-<id>@<zone1>:<vswitch1>,<zone2>:<vswitch2>"
  zoneMaps: "vpc-bp1xxxxxxxxx@cn-hangzhou-h:vsw-bp1yyyyyyy,cn-hangzhou-i:vsw-bp1zzzzzzz"
  minPort: 7000
  maxPort: 8000
  blockPorts:
    - 7979
  portsPerPod: 2
  protocols:
    - TCP
    - UDP
  eipIspTypes:
    - BGP
    - ChinaTelecom
  minAvailable: 10
  externalTrafficPolicy: Local
  healthCheck:
    flag: "on"
    type: tcp
    connectPort: "8080"
    connectTimeout: "3"
    interval: "5"
    healthyThreshold: "2"
    unhealthyThreshold: "2"
  securityProtectionTypes:
    - AntiDDoS.Basic
```

## Configuration

### NLBPoolSpec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `zoneMaps` | string | Yes | VPC and zone mapping in format `vpc-<id>@<zone1>:<vswitch1>,<zone2>:<vswitch2>`. At least 2 zone mappings required. |
| `minPort` | int32 | Yes | Minimum port number for NLB listener range. |
| `maxPort` | int32 | Yes | Maximum port number for NLB listener range. |
| `blockPorts` | []int32 | No | Ports to skip within the port range. |
| `portsPerPod` | int | Yes | Number of consecutive ports required per Pod. Determines how many Pods one NLB can serve (`PodsPerNLB = (maxPort - minPort + 1 - len(blockPorts)) / portsPerPod`). |
| `protocols` | []corev1.Protocol | Yes | Protocols for each port, cycled across ports. E.g., `[TCP, UDP]` with `portsPerPod: 2` assigns TCP+UDP per Pod. |
| `eipIspTypes` | []string | No | EIP ISP types to create. Options: `BGP`, `BGP_PRO`, `ChinaTelecom`, `ChinaUnicom`, `ChinaMobile`. One NLB is created per ISP type. |
| `minAvailable` | int | Yes | Minimum number of available (unbound) Services to maintain. Triggers NLB creation when below threshold. |
| `externalTrafficPolicy` | ServiceExternalTrafficPolicyType | No | Service external traffic policy (`Local` or `Cluster`). |
| `healthCheck` | NLBHealthCheckConfig | No | NLB health check configuration. |
| `securityProtectionTypes` | []string | No | EIP security protection types (e.g., `AntiDDoS.Basic`). |

### NLBHealthCheckConfig

| Field | Type | Description |
|-------|------|-------------|
| `flag` | string | Health check switch: `on` or `off`. |
| `type` | string | Health check type: `tcp` or `http`. |
| `connectPort` | string | Port for health check probes. |
| `connectTimeout` | string | Connection timeout in seconds. |
| `interval` | string | Check interval in seconds. |
| `healthyThreshold` | string | Consecutive successes to mark healthy. |
| `unhealthyThreshold` | string | Consecutive failures to mark unhealthy. |
| `domain` | string | HTTP health check domain (only when type=http). |
| `uri` | string | HTTP health check path (only when type=http). |
| `method` | string | HTTP health check method (only when type=http). |

### NLBPoolStatus

| Field | Type | Description |
|-------|------|-------------|
| `phase` | NLBPoolPhase | Current phase: `Pending`, `Ready`, or `Failed`. |
| `totalNLBs` | int | Total number of NLB instances. |
| `readyNLBs` | int | Number of NLB instances in Active state. |
| `totalServices` | int | Total number of pre-warmed Services. |
| `availableServices` | int | Number of unbound (available) Services. |
| `boundServices` | int | Number of bound Services. |

## How It Works

1. **Pre-warming**: When an NLBPool CR is created, the operator calculates how many Pods one NLB can serve (`PodsPerNLB = (maxPort - minPort + 1 - blockedPorts) / portsPerPod`). For each EIP ISP type, it creates EIP CRs and NLB CRs, then pre-warms `PodsPerNLB` Services per NLB with placeholder selectors.

2. **Binding**: The kruise-game AutoNLBsV3 plugin discovers available Services via the label `game.kruise.io/svc-pool-status=available` and matching `portsPerPod`/`protocols` labels. It binds a Pod to a Service by updating its selector and marking it as `bound`.

3. **On-demand Scaling**: The operator periodically requeues (every 30s) and checks if `availableServices < minAvailable`. When the threshold is breached, it automatically creates new NLB instances and EIPs, then pre-warms additional Services to replenish the pool.

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for details.
