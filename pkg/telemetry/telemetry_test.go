package telemetry

import "testing"

// The DSN's scheme decides TLS; a token never goes over plain HTTP outside the local machine.
func TestExportTarget(t *testing.T) {
	cases := []struct {
		dsn          string
		wantEndpoint string
		wantInsecure bool
		wantErr      bool
	}{
		{"https://token@api.uptrace.dev?grpc=4317", "api.uptrace.dev", false, false},
		{"https://token@uptrace.example.com:8443", "uptrace.example.com:8443", false, false},
		{"http://token@localhost:14318?grpc=14317", "localhost:14318", true, false},
		{"http://token@127.0.0.1:14318", "127.0.0.1:14318", true, false},
		{"http://uptrace.observability:14318", "uptrace.observability:14318", true, false},
		{"http://token@uptrace.observability:14318", "", false, true}, // token in clear over the network
		{"ftp://token@api.uptrace.dev", "", false, true},
		{"not a url", "", false, true},
	}
	for _, c := range cases {
		endpoint, insecure, err := exportTarget(c.dsn)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v, want error %v", c.dsn, err, c.wantErr)
			continue
		}
		if endpoint != c.wantEndpoint || insecure != c.wantInsecure {
			t.Errorf("%s: got (%q, insecure=%v), want (%q, insecure=%v)", c.dsn, endpoint, insecure, c.wantEndpoint, c.wantInsecure)
		}
	}
}
