//go:build !linux && !darwin

package overlay

func setNotSentLowat(uintptr, int) error { return nil }
