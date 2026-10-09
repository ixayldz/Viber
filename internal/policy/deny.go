package policy

import "strings"

func DeniedPath(name string, scopes []string) bool {
	name = strings.ToLower(strings.TrimSuffix(name, "/"))
	for _, scope := range scopes {
		scope = strings.ToLower(scope)
		if strings.HasSuffix(scope, "/**") {
			prefix := strings.TrimSuffix(scope, "/**")
			if name == prefix || strings.HasPrefix(name, prefix+"/") {
				return true
			}
		} else if name == scope {
			return true
		}
	}
	return false
}
