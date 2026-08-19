package controller

import (
	"context"
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	nlbpoolv1alpha1 "github.com/chrisliu1995/AlibabaCloud-NLB-Pool-Operator/apis/v1alpha1"
)

// portMappingResult 是写入绑定 Pod 注解 AnnotationPortMappingResult 的
// JSON 结构（Issue #13）。不进入 CRD schema，仅供容器经 Downward API 消费。
type portMappingResult struct {
	Lane  string            `json:"lane"`
	IP    string            `json:"ip"`
	Ports []portMappingPort `json:"ports"`
}

type portMappingPort struct {
	Name          string `json:"name"`
	ListenerPort  int32  `json:"listenerPort"`
	ContainerPort int32  `json:"containerPort"`
	Protocol      string `json:"protocol"`
}

// buildPortMappingResult 从 pa.Spec.Endpoints 构造端口映射结果 JSON 字符串。
// struct 序列化字段顺序固定，输出字节级稳定，可用作幂等比较。
// ContainerPort 为 0 时回退 ListenerPort（与 lookupContainerPortForSG 一致）。
func buildPortMappingResult(pa *nlbpoolv1alpha1.PortAllocation) string {
	results := make([]portMappingResult, 0, len(pa.Spec.Endpoints))
	for _, ep := range pa.Spec.Endpoints {
		item := portMappingResult{
			Lane:  ep.Lane,
			IP:    ep.EIP,
			Ports: make([]portMappingPort, 0, len(ep.Ports)),
		}
		for _, p := range ep.Ports {
			containerPort := p.ContainerPort
			if containerPort == 0 {
				containerPort = p.ListenerPort
			}
			item.Ports = append(item.Ports, portMappingPort{
				Name:          p.Name,
				ListenerPort:  p.ListenerPort,
				ContainerPort: containerPort,
				Protocol:      p.Protocol,
			})
		}
		results = append(results, item)
	}
	data, err := json.Marshal(results)
	if err != nil {
		// 结构体序列化不会失败；兜底返回空数组。
		return "[]"
	}
	return string(data)
}

// syncPodResultAnnotation 将端口映射结果写入 Pod 注解。
// 期望值与当前注解一致时直接返回（幂等守卫），否则以 MergeFrom Patch 写入。
// 调用方负责处理返回的错误（写失败不回退 PA Phase，由下轮重试自愈）。
func (r *PortAllocationReconciler) syncPodResultAnnotation(ctx context.Context, pa *nlbpoolv1alpha1.PortAllocation, pod *corev1.Pod) error {
	if !r.EnablePodResultAnnotation {
		return nil
	}
	want := buildPortMappingResult(pa)
	if pod.Annotations[nlbpoolv1alpha1.AnnotationPortMappingResult] == want {
		return nil
	}
	patch := client.MergeFrom(pod.DeepCopy())
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[nlbpoolv1alpha1.AnnotationPortMappingResult] = want
	return r.Patch(ctx, pod, patch)
}

// clearPodResultAnnotation 删除绑定 Pod 上的端口映射结果注解（只删 result，
// 保留 claim）。仿 clearPodClaim：BoundPod 为空返回；Pod NotFound 静默返回；
// 仅当 claim 仍指向本 PA 时才删除；注解本就不存在则不发起 Patch；best-effort。
func (r *PortAllocationReconciler) clearPodResultAnnotation(ctx context.Context, pa *nlbpoolv1alpha1.PortAllocation) {
	if !r.EnablePodResultAnnotation {
		return
	}
	if pa.Spec.BoundPod == "" {
		return
	}
	pod := &corev1.Pod{}
	if err := r.Get(ctx, types.NamespacedName{Name: pa.Spec.BoundPod, Namespace: pa.Namespace}, pod); err != nil {
		if errors.IsNotFound(err) {
			return
		}
		return
	}
	if pod.Annotations[nlbpoolv1alpha1.AnnotationPAClaim] != pa.Name {
		return
	}
	if _, ok := pod.Annotations[nlbpoolv1alpha1.AnnotationPortMappingResult]; !ok {
		return
	}
	patch := client.MergeFrom(pod.DeepCopy())
	delete(pod.Annotations, nlbpoolv1alpha1.AnnotationPortMappingResult)
	_ = r.Patch(ctx, pod, patch)
}
