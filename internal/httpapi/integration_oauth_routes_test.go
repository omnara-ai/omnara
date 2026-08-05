package httpapi

import "testing"

func TestValidateSlackSetupPublicURL(t *testing.T) {
	for _, raw := range []string{
		"https://app.example.com",
		"https://moss-solar-rip-bathroom.trycloudflare.com",
		" https://app.example.com/ ",
	} {
		t.Run("valid "+raw, func(t *testing.T) {
			if err := validateSlackSetupPublicURL(raw); err != nil {
				t.Fatalf("validateSlackSetupPublicURL(%q): %v", raw, err)
			}
		})
	}

	for _, raw := range []string{
		"",
		"not-a-url",
		"http://app.example.com",
		"https://localhost:5173",
		"https://api.localhost",
		"https://omnara.local",
		"https://127.0.0.1:8080",
		"https://[::1]:8080",
		"https://10.0.0.10",
		"https://192.168.1.10",
		"https://172.16.0.10",
		"https://100.64.0.10",
		"https://198.51.100.10",
	} {
		t.Run("invalid "+raw, func(t *testing.T) {
			if err := validateSlackSetupPublicURL(raw); err == nil {
				t.Fatalf("validateSlackSetupPublicURL(%q) succeeded", raw)
			}
		})
	}
}
