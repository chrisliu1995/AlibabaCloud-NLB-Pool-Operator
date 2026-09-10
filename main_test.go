package main

import "testing"

func TestValidateAPILimiter(t *testing.T) {
	tests := []struct {
		name    string
		qps     float64
		burst   int
		wantErr bool
	}{
		{name: "limiting disabled ignores burst", qps: 0, burst: 0},
		{name: "positive qps accepts positive burst", qps: 30, burst: 60},
		{name: "positive qps rejects zero burst", qps: 30, burst: 0, wantErr: true},
		{name: "positive qps rejects negative burst", qps: 30, burst: -1, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAPILimiter(tt.qps, tt.burst)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateAPILimiter(%v, %d) error = %v, wantErr %v", tt.qps, tt.burst, err, tt.wantErr)
			}
		})
	}
}
