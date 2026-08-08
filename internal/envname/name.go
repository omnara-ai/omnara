package envname

import "regexp"

// Pattern is the environment-name contract shared by machine configuration,
// agent source validation, and managed-provider bootstrap rendering.
const Pattern = `^[A-Za-z_][A-Za-z0-9_]*$`

var pattern = regexp.MustCompile(Pattern)

func Valid(name string) bool {
	return pattern.MatchString(name)
}
