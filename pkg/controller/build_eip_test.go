package controller

import (
	"reflect"
	"strings"
	"testing"

	nlbpoolv1alpha1 "github.com/chrisliu1995/AlibabaCloud-NLB-Pool-Operator/apis/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildEIPCR(t *testing.T) {
	pool := &nlbpoolv1alpha1.NLBPool{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
	}

	cases := []struct {
		name          string
		lane          nlbpoolv1alpha1.LaneConfig
		wantBandwidth string
		wantSecTypes  []string
	}{
		{
			name:          "BGP default (no override)",
			lane:          nlbpoolv1alpha1.LaneConfig{Name: "bgp-1", ISPType: "BGP"},
			wantBandwidth: "",
		},
		{
			name:          "BGP with explicit bandwidth=100",
			lane:          nlbpoolv1alpha1.LaneConfig{Name: "bgp-1", ISPType: "BGP", Bandwidth: "100"},
			wantBandwidth: "100",
		},
		{
			name:          "BGP_PRO with bandwidth=50",
			lane:          nlbpoolv1alpha1.LaneConfig{Name: "bgp-pro", ISPType: "BGP_PRO", Bandwidth: "50"},
			wantBandwidth: "50",
		},
		{
			name: "BGP with SecurityProtectionTypes",
			lane: nlbpoolv1alpha1.LaneConfig{
				Name: "bgp-1", ISPType: "BGP",
				SecurityProtectionTypes: []string{"AntiDDoS_Enhanced"},
			},
			wantBandwidth: "",
			wantSecTypes:  []string{"AntiDDoS_Enhanced"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &NLBPoolReconciler{}
			eip := r.buildEIPCR(pool, tc.lane, "eip-test")

			// buildEIPCR always sets PayByTraffic (single-ISP rejected upstream by CEL)
			if eip.Spec.InternetChargeType != "PayByTraffic" {
				t.Errorf("ChargeType: want PayByTraffic got %q", eip.Spec.InternetChargeType)
			}
			if eip.Spec.Bandwidth != tc.wantBandwidth {
				t.Errorf("Bandwidth: want %q got %q", tc.wantBandwidth, eip.Spec.Bandwidth)
			}
			if !reflect.DeepEqual(eip.Spec.SecurityProtectionTypes, tc.wantSecTypes) {
				t.Errorf("SecurityProtectionTypes: want %v got %v", tc.wantSecTypes, eip.Spec.SecurityProtectionTypes)
			}
			if eip.Spec.ISP != tc.lane.ISPType {
				t.Errorf("ISP: want %q got %q", tc.lane.ISPType, eip.Spec.ISP)
			}
		})
	}
}

func TestValidateLaneConfig(t *testing.T) {
	cases := []struct {
		name          string
		lanes         []nlbpoolv1alpha1.LaneConfig
		wantMsgSubstr string // empty = expect no error
	}{
		{
			name: "all BGP, all valid",
			lanes: []nlbpoolv1alpha1.LaneConfig{
				{Name: "bgp-1", ISPType: "BGP"},
				{Name: "bgp-2", ISPType: "BGP_PRO", Bandwidth: "100"},
			},
		},
		{
			name: "BGP with optional bandwidthPackageId (path B for BGP)",
			lanes: []nlbpoolv1alpha1.LaneConfig{
				{Name: "bgp-cbwp", ISPType: "BGP", BandwidthPackageId: "cbwp-foo"},
			},
		},
		{
			name: "ChinaTelecom with bandwidthPackageId (valid path B)",
			lanes: []nlbpoolv1alpha1.LaneConfig{
				{Name: "ctc", ISPType: "ChinaTelecom", BandwidthPackageId: "cbwp-foo"},
			},
		},
		{
			name: "ChinaTelecom without bandwidthPackageId (invalid)",
			lanes: []nlbpoolv1alpha1.LaneConfig{
				{Name: "ctc-bad", ISPType: "ChinaTelecom"},
			},
			wantMsgSubstr: "lane ctc-bad uses single-ISP ChinaTelecom",
		},
		{
			name: "ChinaUnicom_L2 without bandwidthPackageId (L2 variant also single-ISP)",
			lanes: []nlbpoolv1alpha1.LaneConfig{
				{Name: "cu-l2", ISPType: "ChinaUnicom_L2"},
			},
			wantMsgSubstr: "lane cu-l2 uses single-ISP ChinaUnicom_L2",
		},
		{
			name: "mixed: 1 BGP + 1 single-ISP invalid (reports first invalid)",
			lanes: []nlbpoolv1alpha1.LaneConfig{
				{Name: "bgp-1", ISPType: "BGP"},
				{Name: "cm-bad", ISPType: "ChinaMobile"},
			},
			wantMsgSubstr: "lane cm-bad uses single-ISP ChinaMobile",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := validateLaneConfig(tc.lanes)
			if tc.wantMsgSubstr == "" {
				if got != "" {
					t.Errorf("expected empty (valid), got %q", got)
				}
			} else {
				if !strings.Contains(got, tc.wantMsgSubstr) {
					t.Errorf("expected substring %q in %q", tc.wantMsgSubstr, got)
				}
			}
		})
	}
}
