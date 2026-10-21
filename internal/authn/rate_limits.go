package authn

// ClientRateLimits sets per-client authentication budgets. Zero fields use the defaults.
type ClientRateLimits struct {
	Signup              int
	Login               int
	PasswordReset       int
	Token               int
	OAuthLogin          int
	OAuthLoginConnector int
	DeviceAuthorization int
	DevicePoll          int
}

func (limits ClientRateLimits) WithDefaults() ClientRateLimits {
	for _, setting := range []struct {
		value    *int
		fallback int
	}{
		{&limits.Signup, 25},
		{&limits.Login, 100},
		{&limits.PasswordReset, 25},
		{&limits.Token, 100},
		{&limits.OAuthLogin, 120},
		{&limits.OAuthLoginConnector, 30},
		{&limits.DeviceAuthorization, 25},
		{&limits.DevicePoll, 600},
	} {
		if *setting.value == 0 {
			*setting.value = setting.fallback
		}
	}
	return limits
}
