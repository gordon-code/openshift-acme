package openshiftacmeexposer

import (
	"testing"
)

func TestOptionsValidate(t *testing.T) {
	tests := []struct {
		name     string
		listenIP string
		port     uint16
		wantErr  bool
	}{
		{name: "valid IPv4", listenIP: "127.0.0.1", port: uint16(8080), wantErr: false},
		{name: "invalid IP", listenIP: "not-an-ip", port: uint16(8080), wantErr: true},
		{name: "invalid port low", listenIP: "127.0.0.1", port: uint16(0), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &Options{
				ListenIP:     tt.listenIP,
				ResponseFile: "/tmp/fake",
				Port:         tt.port,
			}
			errs := o.Validate()
			if (errs != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", errs, tt.wantErr)
			}
		})
	}
}
