package supervisor

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
)

var (
	// ErrAlreadyRunning is returned by Start (or SetConfig) when the supervisor
	// is already running.
	ErrAlreadyRunning = errors.New("supervisor: already running")
	// ErrNotRunning is returned by Stop when nothing is running.
	ErrNotRunning = errors.New("supervisor: not running")
	// ErrInvalidMode is returned by Start for an unknown mode.
	ErrInvalidMode = errors.New("supervisor: invalid mode")
)

// isClosed reports whether err is the benign "listener closed by Stop" error,
// which must not be surfaced to the user as a bind failure.
func isClosed(err error) bool { return errors.Is(err, net.ErrClosed) }

// classifyBindErr turns a raw listen/serve error into a friendly Korean message
// for the UI. It detects the two cases users actually hit — missing privilege
// (e.g. binding :53) and a port already in use — using cross-platform errno
// comparisons (syscall.EACCES / syscall.EADDRINUSE are defined on every OS,
// mapped to the WSA equivalents on Windows), so no build tags are needed.
func classifyBindErr(kind, addr string, err error) string {
	switch {
	case errors.Is(err, syscall.EACCES), errors.Is(err, os.ErrPermission):
		return fmt.Sprintf("%s(%s) 주소를 열 권한이 없어요. 53번처럼 낮은 포트는 관리자 권한이 필요해요 — 5353같이 1024 이상 포트로 바꾸거나 관리자 권한으로 실행해 주세요.", addr, kind)
	case errors.Is(err, syscall.EADDRINUSE):
		return fmt.Sprintf("%s(%s) 포트가 이미 사용 중이에요. 다른 포트로 바꾸거나 그 포트를 쓰는 프로그램을 종료해 주세요.", addr, kind)
	default:
		return fmt.Sprintf("%s(%s) 주소를 여는 데 실패했어요: %v", addr, kind, err)
	}
}
