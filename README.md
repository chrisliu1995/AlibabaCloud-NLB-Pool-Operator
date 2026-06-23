# AlibabaCloud NLB Pool Operator

A Kubernetes operator that manages Alibaba Cloud NLB (Network Load Balancer) resource pools for game server workloads. It pre-warms NLB instances, EIPs, ServerGroups, and Listeners so that game servers get public-facing endpoints instantly without cold-start latency.

## Architecture

```
NLBPool CR (user defines lanes, ports, slots)
   │
   ▼  NLB Pool Operator
   ├─ Path A (independent EIP):
   │    creates EIP CR per zone ──► EIP Operator ──► Alibaba Cloud EIP
   │    BGP → PayByTraffic; Single-ISP → PayByBandwidth
   │
   ├─ Path B (BGP bandwidth package, no EIP CR):
   │    passes bandwidthPackageId to NLB CR
   │    NLB auto-creates BGP PayByTraffic EIPs joined to the package
   │
   ├─ Path A+B hybrid (single-ISP + bandwidth package):
   │    creates EIP CR (PayByBandwidth) + passes both AllocationId
   │    and bandwidthPackageId to NLB CR; NLB auto-joins EIP to package
   │
   ├─ creates NLB CR per (lane, group) ──► NLB Operator ──► Alibaba Cloud NLB
   │
   └─ creates PortAllocation (PA) CRs ──► PA Controller provisions SG + Listener
                                              │
                                              ▼
                                         GameServerSet (V3 plugin)
                                              │  claims PA for Pod
                                              │  AddServersToServerGroup(Pod IP)
                                              ▼
                                         GS.status.networkStatus.externalAddresses
```

- **NLB Pool Operator**: Owns NLBPool CR; orchestrates NLB, EIP, and PortAllocation lifecycle.
- **NLB Operator** ([repo](https://github.com/chrisliu1995/AlibabaCloud-NLB-Operator)): Reconciles NLB CRs into real NLB instances.
- **EIP Operator** ([repo](https://github.com/chrisliu1995/AlibabaCloud-EIP-Operator)): Reconciles EIP CRs into Elastic IPs (path A / A+B only).
- **kruise-game V3 Plugin** ([merged PR #335](https://github.com/openkruise/kruise-game/pull/335)): Binds Pods to available PortAllocations by writing Pod IP into ServerGroups.

## Prerequisites

- Kubernetes 1.28+
- Alibaba Cloud ACK cluster with Terway eniip CNI (Pod IPs must be VPC-routable for NLB backends)
- [AlibabaCloud-NLB-Operator](https://github.com/chrisliu1995/AlibabaCloud-NLB-Operator) v0.2.1+
- [AlibabaCloud-EIP-Operator](https://github.com/chrisliu1995/AlibabaCloud-EIP-Operator) v0.3.0+
- RAM AccessKey with `AliyunNLBFullAccess`, `AliyunEIPFullAccess`, `AliyunVPCFullAccess`
- Single-ISP lanes require the account to have "single-line bandwidth" whitelist enabled

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

### Scenario 1: Multi-ISP without bandwidth packages (simplest)

Each GS gets 4 carrier-specific public IPs. Single-ISP EIPs are PayByBandwidth (200Mbps default); BGP EIP is PayByTraffic.

```yaml
apiVersion: nlbpool.alibabacloud.com/v1alpha1
kind: NLBPool
metadata:
  name: multi-isp-pool
spec:
  region: cn-shanghai
  vpcId: vpc-uf6xxxxxxxxxxxxx
  zoneMaps:
    - { zone: cn-shanghai-m, vswitchId: vsw-uf6xxxxxxxxxxxxx }
    - { zone: cn-shanghai-n, vswitchId: vsw-uf6yyyyyyyyyyyyy }
  lanes:
    - { name: bgp, ispType: BGP }
    - { name: ctc, ispType: ChinaTelecom }
    - { name: cuc, ispType: ChinaUnicom }
    - { name: cmc, ispType: ChinaMobile }
  ports:
    - { name: game, protocol: TCP, containerPort: 7777 }
  portRange: { min: 30000, max: 30099 }
  slotsPerNLB: 3
  minAvailableNLBs: 1
```

Produces: 4 NLB + **8 EIP CRs** (BGP×2 PayByTraffic + CTC×2 + CUC×2 + CMC×2 PayByBandwidth).

### Scenario 2: Multi-ISP with per-ISP bandwidth packages

Each single-ISP lane uses a dedicated bandwidth package for unified billing. BGP lane also uses a BGP bandwidth package.

```yaml
lanes:
  - name: bgp
    ispType: BGP
    bandwidthPackageId: cbwp-bgp-xxx       # BGP bandwidth package
  - name: ctc
    ispType: ChinaTelecom
    bandwidthPackageId: cbwp-ctc-xxx       # ChinaTelecom bandwidth package
  - name: cuc
    ispType: ChinaUnicom
    bandwidthPackageId: cbwp-cuc-xxx       # ChinaUnicom bandwidth package
  - name: cmc
    ispType: ChinaMobile
    bandwidthPackageId: cbwp-cmc-xxx       # ChinaMobile bandwidth package
```

Produces: 4 NLB + **6 EIP CRs** (single-ISP lanes create EIP CRs; BGP lane skips EIP CR).

Path routing:
- **BGP + bandwidthPackageId** → path B: no EIP CR, NLB auto-creates BGP EIP and joins it to the BGP bandwidth package
- **Single-ISP + bandwidthPackageId** → path A+B hybrid: creates EIP CR (PayByBandwidth), NLB receives both `AllocationId` + `BandwidthPackageId` and auto-joins the EIP to the bandwidth package

> **Important**: Each bandwidth package's ISP must match its lane's `ispType`. Create separate bandwidth packages per ISP type.

### Scenario 3: BGP-only with bandwidth package

```yaml
lanes:
  - name: bgp
    ispType: BGP
    bandwidthPackageId: cbwp-uf6xxxxxxxxxxxxx
```

Produces: 1 NLB + **0 EIP CRs** (pure path B).

### Scenario 4: Giant (4 BGP lanes × 4 ports × 100 slots)

```yaml
apiVersion: nlbpool.alibabacloud.com/v1alpha1
kind: NLBPool
metadata:
  name: giant-pool
spec:
  region: cn-shanghai
  vpcId: vpc-uf6xxxxxxxxxxxxx
  zoneMaps:
    - { zone: cn-shanghai-m, vswitchId: vsw-uf6xxxxxxxxxxxxx }
    - { zone: cn-shanghai-n, vswitchId: vsw-uf6yyyyyyyyyyyyy }
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
  portRange: { min: 30000, max: 30399 }
  slotsPerNLB: 100
  minAvailableNLBs: 1
  healthCheck: { enabled: false }
```

Resource count: 4 NLB × 8 EIP × 100 PA × 400 SG × 1600 Listener. Pre-warming takes ~10 min.

## EIP Path Routing

The operator automatically selects the EIP management path based on `ispType` and `bandwidthPackageId`:

| Scenario | Path | EIP CR | EIP ChargeType |
|----------|------|--------|----------------|
| BGP, no bandwidthPackageId | A | Yes | PayByTraffic |
| BGP + bandwidthPackageId | B | No (NLB auto-creates) | PayByTraffic (joined to package) |
| Single-ISP, no bandwidthPackageId | A | Yes | PayByBandwidth (default 200Mbps) |
| Single-ISP + bandwidthPackageId | A+B hybrid | Yes | PayByBandwidth (NLB auto-joins to package) |

### NLB EIP constraints (verified)

| EIP ISP | PayByTraffic → NLB | PayByBandwidth → NLB |
|---------|-------------------|---------------------|
| BGP / BGP_PRO | ✅ | ❌ `OnlyPayByTrafficSupported` |
| ChinaTelecom / ChinaUnicom / ChinaMobile | ❌ (not available) | ✅ NLB allows it |

> NLB rejects EIPs that are already in a bandwidth package (`EipAlreadyInBandwidthPackage`). For the A+B hybrid path, the Operator creates EIPs **without** joining them to a bandwidth package first; NLB handles the join automatically when it receives both `AllocationId` and `BandwidthPackageId`.

## Verified Test Results (cn-shanghai)

| Scenario | EIP CRs | Bandwidth Packages | NLB | nc 4-lane | Status |
|----------|---------|-------------------|-----|-----------|--------|
| 4 ISP lanes, no CBWP | 8 (BGP×2 + CTC×2 + CUC×2 + CMC×2) | None | 4 Active | 4/4 ✅ | Verified |
| 3 single-ISP CBWP + BGP no CBWP | 8 (single-ISP only) | 3 (CTC+CUC+CMC) | 4 Active | 4/4 ✅ | Verified |
| All 4 lanes with CBWP | 6 (single-ISP only; BGP skipped) | 4 (BGP+CTC+CUC+CMC) | 4 Active | 4/4 ✅ | Verified |
| 100-slot giant (4 BGP) | 8 | None | 4 Active | 3/3 ✅ | Verified |

## API Reference

### NLBPoolSpec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `region` | string | Yes | Alibaba Cloud region (e.g., `cn-shanghai`). |
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
| `name` | string | Yes | Lane name, used in CR naming. |
| `ispType` | string | Yes | ISP type: `BGP`, `BGP_PRO`, `ChinaTelecom`, `ChinaUnicom`, `ChinaMobile`. |
| `bandwidthPackageId` | string | No | CommonBandwidthPackage ID. BGP lanes: pure path B (NLB auto-creates EIP). Single-ISP lanes: A+B hybrid (Operator creates EIP CR, NLB auto-joins to package). Package ISP must match lane ISP. |
| `bandwidth` | string | No | EIP bandwidth peak in Mbps. BGP default: 5 Mbps. Single-ISP default: 200 Mbps. |
| `securityProtectionTypes` | []string | No | EIP security protection types, e.g., `["AntiDDoS_Enhanced"]`. |

### PortConfig

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Port name, used in CR naming. |
| `protocol` | string | Yes | Protocol: `TCP`, `UDP`, or `TCPSSL`. |
| `containerPort` | int32 | No | Container port the backend Pod listens on. |

### NLBPoolStatus

| Field | Type | Description |
|-------|------|-------------|
| `phase` | string | `Pending`, `Provisioning`, `Ready`, `Deleting`, `Failed`. |
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
| EIP (path A / A+B) | `NLB_count × len(zoneMaps)` |
| EIP (pure path B) | Managed by NLB, not tracked as K8s CRs |
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

1. **Pre-warming**: The operator creates NLB and EIP CRs (path A / A+B) or delegates EIP lifecycle to NLB (path B), then creates one PortAllocation CR per slot. The PA Controller provisions a ServerGroup and Listener for each (port, lane) combination.

2. **Binding**: The kruise-game V3 plugin watches PortAllocations. When a GameServerSet Pod starts, the plugin claims an available PA, then calls `AddServersToServerGroup` to register the Pod IP. The Pod's `GS.status.networkStatus.externalAddresses` exposes a multi-lane endpoint like `<nlb-bgp>/bgp,<nlb-ctc>/ctc,<nlb-cuc>/cuc,<nlb-cmc>/cmc`.

3. **On-demand Scaling**: When available slots fall below `minAvailableNLBs × slotsPerNLB`, new NLB groups are created. Set `minAvailableNLBs: 0` to lock the pool.

4. **Deletion**: NLB/EIP CRs are deleted first (NLB cascade-deletes Listeners), then PAs (finalizers delete ServerGroups). Path B EIPs are released automatically with NLB. Path A+B EIPs are released via EIP CR deletion.

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for details.
