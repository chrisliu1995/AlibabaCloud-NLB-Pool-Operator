# AlibabaCloud NLB Pool Operator

A Kubernetes operator that manages Alibaba Cloud NLB (Network Load Balancer) resource pools for game server workloads. It pre-warms NLB instances, EIPs, ServerGroups, and Listeners so that game servers get public-facing endpoints instantly without cold-start latency.

## Architecture

```
NLBPool CR (user defines lanes, ports, slots)
   │
   ▼  NLB Pool Operator
   ├─ Path A (independent EIP):  creates EIP CR per zone ──► EIP Operator ──► Alibaba Cloud EIP
   ├─ Path B (bandwidth package): skips EIP CR, passes bandwidthPackageId to NLB CR
   │                               NLB auto-creates EIPs joined to the bandwidth package
   │
   ├─ creates NLB CR per (lane, group) ──► NLB Operator ──► Alibaba Cloud NLB
   │
   └─ creates PortAllocation (PA) CRs ──► PA Controller provisions SG + Listener via cloud API
                                              │
                                              ▼
                                         GameServerSet (V3 plugin)
                                              │  kruise-game watches PA
                                              │  claims PA for Pod
                                              │  AddServersToServerGroup(Pod IP)
                                              ▼
                                         GS.status.networkStatus.externalAddresses
```

- **NLB Pool Operator**: Owns NLBPool CR; orchestrates NLB, EIP, and PortAllocation lifecycle.
- **NLB Operator** ([repo](https://github.com/chrisliu1995/AlibabaCloud-NLB-Operator)): Reconciles NLB CRs into real NLB instances.
- **EIP Operator** ([repo](https://github.com/chrisliu1995/AlibabaCloud-EIP-Operator)): Reconciles EIP CRs into Elastic IPs (path A only).
- **kruise-game V3 Plugin** ([PR #335](https://github.com/openkruise/kruise-game/pull/335)): Binds Pods to available PortAllocations by writing Pod IP into ServerGroups.

## Prerequisites

- Kubernetes 1.28+ (1.30+ recommended for CEL validation support)
- Alibaba Cloud ACK cluster with Terway eniip CNI (Pod IPs must be VPC-routable for NLB backends)
- [AlibabaCloud-NLB-Operator](https://github.com/chrisliu1995/AlibabaCloud-NLB-Operator) v0.2.1+
- [AlibabaCloud-EIP-Operator](https://github.com/chrisliu1995/AlibabaCloud-EIP-Operator) v0.3.0+
- RAM AccessKey with `AliyunNLBFullAccess`, `AliyunEIPFullAccess`, `AliyunVPCFullAccess`

## Installation

The recommended way is via the unified Helm chart:

```bash
git clone https://github.com/chrisliu1995/AlibabaCloud-Operator-Charts.git
cd AlibabaCloud-Operator-Charts

helm install alibabacloud-operators . \
  --set global.alibabacloud.accessKeyId="$AK" \
  --set global.alibabacloud.accessKeySecret="$SK" \
  --set global.alibabacloud.region=cn-shanghai
```

This installs NLB Operator, EIP Operator, NLB Pool Operator, and all CRDs in one step. See the [chart repo](https://github.com/chrisliu1995/AlibabaCloud-Operator-Charts) for `values.yaml` options.

## Usage

### Minimal smoke test (1 BGP lane, 1 port, 3 slots)

```yaml
apiVersion: nlbpool.alibabacloud.com/v1alpha1
kind: NLBPool
metadata:
  name: smoke-pool
spec:
  region: cn-shanghai
  vpcId: vpc-uf6xxxxxxxxxxxxx
  zoneMaps:
    - zone: cn-shanghai-m
      vswitchId: vsw-uf6xxxxxxxxxxxxx
    - zone: cn-shanghai-n
      vswitchId: vsw-uf6yyyyyyyyyyyyy
  lanes:
    - name: bgp
      ispType: BGP
  ports:
    - name: game
      protocol: TCP
      containerPort: 7777
  portRange:
    min: 10000
    max: 20000
  slotsPerNLB: 3
  minAvailableNLBs: 1
```

### Pay-by-bandwidth via shared bandwidth package

```yaml
lanes:
  - name: bgp-bw
    ispType: BGP
    bandwidthPackageId: cbwp-uf6xxxxxxxxxxxxx   # CommonBandwidthPackage ID
```

When `bandwidthPackageId` is set, the operator skips EIP CR creation entirely (path B). NLB auto-creates PayByTraffic EIPs and joins them to the bandwidth package. Actual billing follows the bandwidth package (PayByBandwidth).

### Multi-lane with single-ISP (requires bandwidthPackageId)

```yaml
lanes:
  - name: bgp
    ispType: BGP
    bandwidthPackageId: cbwp-uf6xxxxxxxxxxxxx
  - name: ctc
    ispType: ChinaTelecom
    bandwidthPackageId: cbwp-uf6xxxxxxxxxxxxx
  - name: cuc
    ispType: ChinaUnicom
    bandwidthPackageId: cbwp-uf6xxxxxxxxxxxxx
  - name: cmc
    ispType: ChinaMobile
    bandwidthPackageId: cbwp-uf6xxxxxxxxxxxxx
```

> **Note**: Single-ISP lanes (ChinaTelecom/ChinaUnicom/ChinaMobile) **require** `bandwidthPackageId`. NLB rejects directly-attached single-ISP EIPs (`OperationDenied.OnlyPayByTrafficSupported`). The CRD enforces this via CEL validation — `kubectl apply` will reject the manifest immediately if a single-ISP lane lacks `bandwidthPackageId`.

### Giant scenario (4 lanes × 4 ports × 100 slots)

```yaml
apiVersion: nlbpool.alibabacloud.com/v1alpha1
kind: NLBPool
metadata:
  name: giant-pool
spec:
  region: cn-shanghai
  vpcId: vpc-uf6xxxxxxxxxxxxx
  zoneMaps:
    - zone: cn-shanghai-m
      vswitchId: vsw-uf6xxxxxxxxxxxxx
    - zone: cn-shanghai-n
      vswitchId: vsw-uf6yyyyyyyyyyyyy
  lanes:
    - { name: bgp-1, ispType: BGP }
    - { name: bgp-2, ispType: BGP }
    - { name: bgp-3, ispType: BGP }
    - { name: bgp-4, ispType: BGP }
  ports:
    - { name: game,    protocol: TCP, containerPort: 7777 }
    - { name: voice,   protocol: UDP, containerPort: 7778 }
    - { name: http,    protocol: TCP, containerPort: 7779 }
    - { name: metrics, protocol: TCP, containerPort: 7780 }
  portRange:
    min: 30000
    max: 30399
  slotsPerNLB: 100
  minAvailableNLBs: 1
  healthCheck:
    enabled: false
```

Resource count: 4 NLB × 8 EIP × 100 PA × 400 SG × 1600 Listener. Pre-warming takes ~10 min.

## API Reference

### NLBPoolSpec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `region` | string | Yes | Alibaba Cloud region (e.g., `cn-shanghai`, `cn-hongkong`). |
| `vpcId` | string | Yes | VPC ID where NLB instances are created. |
| `zoneMaps` | []ZoneMapEntry | Yes | Availability zones and their VSwitches. At least 2 zones required. |
| `lanes` | []LaneConfig | Yes | Network lanes. Each lane produces one NLB per group. |
| `ports` | []PortConfig | Yes | Ports exposed per Pod. Each port creates one ServerGroup per slot. |
| `portRange` | PortRange | Yes | Listener port range. Must cover `slotsPerNLB × len(ports)` ports. |
| `slotsPerNLB` | int32 | Yes | Number of slots (PortAllocations) per NLB instance. Max: `500 / len(ports)`. |
| `minAvailableNLBs` | int32 | Yes | Minimum idle NLB groups to keep pre-warmed. Set to 0 to lock pool size. |
| `healthCheck` | NLBHealthCheckConfig | No | NLB health check configuration. |

### LaneConfig

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Lane name, used in CR naming (e.g., `bgp-1`). |
| `ispType` | string | Yes | ISP type: `BGP`, `BGP_PRO`, `ChinaTelecom`, `ChinaUnicom`, `ChinaMobile`. |
| `bandwidthPackageId` | string | No | CommonBandwidthPackage ID. When set, NLB auto-creates PayByTraffic EIPs joined to this package (path B). **Required for single-ISP lanes** — enforced by CRD CEL validation. |
| `bandwidth` | string | No | EIP bandwidth peak in Mbps (path A only, ignored when `bandwidthPackageId` is set). Default: cloud default 5 Mbps for PayByTraffic. |
| `securityProtectionTypes` | []string | No | EIP security protection types, e.g., `["AntiDDoS_Enhanced"]`. Path A only. |

### PortConfig

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Port name, used in CR naming. |
| `protocol` | string | Yes | Protocol: `TCP`, `UDP`, or `TCPSSL`. |
| `containerPort` | int32 | No | Container port the backend Pod listens on. |

### NLBPoolStatus

| Field | Type | Description |
|-------|------|-------------|
| `phase` | string | Current phase: `Pending`, `Provisioning`, `Ready`, `Deleting`, `Failed`. |
| `totalSlots` | int32 | Total PortAllocation count. |
| `availableSlots` | int32 | Unbound (available) slots. |
| `boundSlots` | int32 | Slots bound to Pods. |
| `nlbsPerLane` | int32 | Current NLB groups per lane. |
| `lanes` | []LaneStatus | Per-lane status (NLB ID, EIP, readiness). |
| `message` | string | Human-readable status message. |

## Resource Count Formula

| Resource | Count |
|----------|-------|
| NLB | `len(lanes) × ceil((boundSlots + slotsPerNLB × minAvailableNLBs) / slotsPerNLB)` |
| EIP (path A) | `NLB_count × len(zoneMaps)` |
| EIP (path B) | Managed by NLB, not tracked as K8s CRs |
| PortAllocation | `NLB_groups × slotsPerNLB` |
| ServerGroup | `PA_count × len(ports)` |
| Listener | `PA_count × len(ports) × len(lanes)` |

### Single NLB Limits

| Item | Limit |
|------|-------|
| Listeners per NLB | 500 |
| ServerGroups per NLB | 500 |
| **Constraint** | `slotsPerNLB × len(ports) ≤ 500` |

## How It Works

1. **Pre-warming**: The operator creates NLB and EIP CRs (or delegates EIP to the NLB service when using bandwidth packages), then creates one PortAllocation CR per slot. The PA Controller provisions a ServerGroup and Listener for each (port, lane) combination via the Alibaba Cloud API.

2. **Binding**: The kruise-game V3 plugin watches PortAllocations. When a GameServerSet Pod starts, the plugin claims an available PA via optimistic-lock update, then calls `AddServersToServerGroup` to register the Pod IP as a backend. The Pod's `GS.status.networkStatus.externalAddresses` exposes the public NLB DNS + port.

3. **On-demand Scaling**: The operator monitors `boundSlots` vs `totalSlots`. When available slots fall below `minAvailableNLBs × slotsPerNLB`, it creates new NLB groups with fresh PAs. Set `minAvailableNLBs: 0` to lock the pool and prevent auto-scaling during testing.

4. **Deletion**: Deleting the NLBPool CR triggers a phased cleanup: NLB/EIP CRs are deleted first (cloud NLB deletion cascade-deletes all Listeners), then PAs are removed (their finalizers delete ServerGroups). Path B EIPs are automatically released when the cloud NLB is deleted.

## EIP Billing Modes

NLB only accepts PayByTraffic EIPs for direct attachment. To use pay-by-bandwidth billing, create a `CommonBandwidthPackage` and pass its ID via `lanes[].bandwidthPackageId`. The NLB will auto-create PayByTraffic EIPs and join them to the package; actual billing follows the package.

| Scenario | Path | LaneConfig |
|----------|------|------------|
| BGP pay-by-traffic (default) | A | `ispType: BGP` |
| BGP pay-by-traffic with custom peak | A | `ispType: BGP`, `bandwidth: "100"` |
| BGP pay-by-bandwidth | B | `ispType: BGP`, `bandwidthPackageId: cbwp-xxx` |
| Single-ISP (ChinaTelecom/Unicom/Mobile) | B (required) | `ispType: ChinaTelecom`, `bandwidthPackageId: cbwp-xxx` |

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for details.
