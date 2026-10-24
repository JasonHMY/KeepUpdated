//go:build !darwin

package manage

import "errors"

func exchangeApplications(a, b string) error {
	return errors.New("application_atomic_swap_requires_macos")
}
