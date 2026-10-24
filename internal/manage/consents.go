package manage

import (
	"errors"
	"sort"
)

const (
	ConsentRuntimeUpdate     = "runtime_update"
	ConsentPrivilegedInstall = "privileged_install"
	ConsentRestartSystem     = "restart_system"
)

// NormalizeConsents accepts only named product actions, never an elevation wildcard.
func NormalizeConsents(values []string) ([]string, error) {
	seen := map[string]bool{}
	for _, value := range values {
		switch value {
		case ConsentRuntimeUpdate, ConsentPrivilegedInstall, ConsentRestartSystem:
			seen[value] = true
		default:
			return nil, errors.New("unknown consent")
		}
	}
	var result []string
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func HasConsents(selection Selection, required ...string) bool {
	if _, err := NormalizeConsents(required); err != nil {
		return false
	}
	if _, err := NormalizeConsents(selection.Consents); err != nil {
		return false
	}
	for _, consent := range required {
		if !contains(selection.Consents, consent) {
			return false
		}
	}
	return true
}
