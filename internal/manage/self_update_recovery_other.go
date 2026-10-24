//go:build !darwin

package manage

import "errors"

func selfUpdateLockActive(path string) (bool, error) {
	return false, errors.New("self_update_lock_observation_unsupported")
}
