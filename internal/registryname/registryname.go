package registryname

import "regexp"

var pattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,127}$`)

// Valid reports whether value is a lowercase, provider-neutral registry name.
func Valid(value string) bool {
	return pattern.MatchString(value)
}
