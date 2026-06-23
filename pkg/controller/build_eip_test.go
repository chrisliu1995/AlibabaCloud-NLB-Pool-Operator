package controller

import (
	"reflect"
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
		wantCharge    string
		wantBandwidth string
		wantSecTypes  []string
	}{
		{
			name:          "BGP default",
			lane:          nlbpoolv1alpha1.LaneConfig{Name: "bgp-1", ISPType: "BGP"},
			wantCharge:    "PayByTraffic",
			wantBandwidth: "",
		},
		{
			name:          "BGP with bandwidth=100",
			lane:          nlbpoolv1alpha1.LaneConfig{Name: "bgp-1", ISPType: "BGP", Bandwidth: "100"},
			wantCharge:    "PayByTraffic",
			wantBandwidth: "100",
		},
		{
			name:          "BGP_PRO with bandwidth=50",
			lane:          nlbpoolv1alpha1.LaneConfig{Name: "bgp-pro", ISPType: "BGP_PRO", Bandwidth: "50"},
			wantCharge:    "PayByTraffic",
			wantBandwidth: "50",
		},
		{
			name: "BGP with SecurityProtectionTypes",
			lane: nlbpoolv1alpha1.LaneConfig{
				Name: "bgp-1", ISPType: "BGP",
				SecurityProtectionTypes: []string{"AntiDDoS_Enhanced"},
			},
			wantCharge:    "PayByTraffic",
			wantBandwidth: "",
			wantSecTypes:  []string{"AntiDDoS_Enhanced"},
		},
		{
			name:          "ChinaTelecom default → PayByBandwidth + 200",
			lane:          nlbpoolv1alpha1.LaneConfig{Name: "ctc", ISPType: "ChinaTelecom"},
			wantCharge:    "PayByBandwidth",
			wantBandwidth: "200",
		},
		{
			name:          "ChinaUnicom with bandwidth=50 → PayByBandwidth + 50",
			lane:          nlbpoolv1alpha1.LaneConfig{Name: "cuc", ISPType: "ChinaUnicom", Bandwidth: "50"},
			wantCharge:    "PayByBandwidth",
			wantBandwidth: "50",
		},
		{
			name:          "ChinaMobile default → PayByBandwidth + 200",
			lane:          nlbpoolv1alpha1.LaneConfig{Name: "cmc", ISPType: "ChinaMobile"},
			wantCharge:    "PayByBandwidth",
			wantBandwidth: "200",
		},
		{
			name:          "ChinaTelecom_L2 default → PayByBandwidth + 200",
			lane:          nlbpoolv1alpha1.LaneConfig{Name: "ctc-l2", ISPType: "ChinaTelecom_L2"},
			wantCharge:    "PayByBandwidth",
			wantBandwidth: "200",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &NLBPoolReconciler{}
			eip := r.buildEIPCR(pool, tc.lane, "eip-test")

			if eip.Spec.InternetChargeType != tc.wantCharge {
				t.Errorf("ChargeType: want %q got %q", tc.wantCharge, eip.Spec.InternetChargeType)
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

func TestIsSingleISP(t *testing.T) {
	cases := []struct {
		ispType string
		want    bool
	}{
		{"BGP", false},
		{"BGP_PRO", false},
		{"ChinaTelecom", true},
		{"ChinaUnicom", true},
		{"ChinaMobile", true},
		{"ChinaTelecom_L2", true},
		{"ChinaUnicom_L2", true},
		{"ChinaMobile_L2", true},
		{"Unknown", false},
	}
	for _, tc := range cases {
		t.Run(tc.ispType, func(t *testing.T) {
			if got := isSingleISP(tc.ispType); got != tc.want {
				t.Errorf("isSingleISP(%q) = %v, want %v", tc.ispType, got, tc.want)
			}
		})
	}
}

func TestValidateLaneConfig(t *testing.T) {
	cases := []struct {
		name  string
		lanes []nlbpoolv1alpha1.LaneConfig
	}{
		{
			name:  "all BGP",
			lanes: []nlbpoolv1alpha1.LaneConfig{{Name: "bgp", ISPType: "BGP"}},
		},
		{
			name:  "ChinaTelecom without bandwidthPackageId (valid — direct EIP path)",
			lanes: []nlbpoolv1alpha1.LaneConfig{{Name: "ctc", ISPType: "ChinaTelecom"}},
		},
		{
			name:  "ChinaTelecom with bandwidthPackageId (valid — A+B hybrid)",
			lanes: []nlbpoolv1alpha1.LaneConfig{{Name: "ctc", ISPType: "ChinaTelecom", BandwidthPackageId: "cbwp-foo"}},
		},
		{
			name: "mixed lanes all valid",
			lanes: []nlbpoolv1alpha1.LaneConfig{
				{Name: "bgp", ISPType: "BGP", BandwidthPackageId: "cbwp-bgp"},
				{Name: "ctc", ISPType: "ChinaTelecom"},
				{Name: "cmc", ISPType: "ChinaMobile", BandwidthPackageId: "cbwp-cm"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if msg := validateLaneConfig(tc.lanes); msg != "" {
				t.Errorf("unexpected validation error: %s", msg)
			}
		})
	}
}
