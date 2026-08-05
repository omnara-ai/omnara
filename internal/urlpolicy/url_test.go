package urlpolicy

import "testing"

func TestRequireHTTPSOrLoopback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		rawURL  string
		wantErr bool
	}{
		{name: "HTTPS", rawURL: "https://auth.example.com/token"},
		{name: "localhost", rawURL: "http://localhost:8080/token"},
		{name: "absolute localhost", rawURL: "http://localhost.:8080/token"},
		{name: "IPv4 loopback", rawURL: "http://127.0.0.2/token"},
		{name: "IPv6 loopback", rawURL: "http://[::1]:8080/token"},
		{name: "public HTTP", rawURL: "http://auth.example.com/token", wantErr: true},
		{name: "relative", rawURL: "/token", wantErr: true},
		{name: "opaque HTTPS", rawURL: "https:token", wantErr: true},
		{name: "missing hostname", rawURL: "https://:443/token", wantErr: true},
		{name: "user information", rawURL: "https://user:secret@auth.example.com/token", wantErr: true},
		{name: "fragment", rawURL: "https://auth.example.com/token#fragment", wantErr: true},
		{name: "unsupported scheme", rawURL: "ftp://auth.example.com/token", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := RequireHTTPSOrLoopback(test.rawURL)
			if (err != nil) != test.wantErr {
				t.Fatalf("RequireHTTPSOrLoopback(%q) error = %v, wantErr %v", test.rawURL, err, test.wantErr)
			}
		})
	}
}
