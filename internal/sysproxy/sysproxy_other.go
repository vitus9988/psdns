//go:build !darwin && !windows && !linux

package sysproxy

import "errors"

// errUnsupported is returned on platforms without a system-proxy implementation.
var errUnsupported = errors.New("이 운영체제에서는 시스템 프록시 자동 설정을 지원하지 않아요")

func supported() bool { return false }

func capture() (Backup, error) { return Backup{}, errUnsupported }

func apply(Settings) error { return errUnsupported }

func restore(Backup) error { return errUnsupported }
