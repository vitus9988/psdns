package supervisor

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
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
// for the UI. It detects the cases users actually hit — missing privilege and a
// port already in use — using cross-platform errno comparisons (syscall.EACCES /
// syscall.EADDRINUSE are defined on every OS, mapped to the WSA equivalents on
// Windows), so no build tags are needed. The permission message is port-aware:
// a privileged port (e.g. :53) points at admin rights, while a high port that is
// still denied is almost always a Windows reserved range (Hyper-V/WSL/Docker
// excluded port ranges) or a firewall rather than a privilege problem.
func classifyBindErr(kind, addr string, err error) string {
	switch {
	case errors.Is(err, syscall.EACCES), errors.Is(err, os.ErrPermission):
		if isPrivilegedPort(addr) {
			return fmt.Sprintf("%s(%s) 주소를 열 권한이 없어요. 53번처럼 낮은 포트는 관리자 권한이 필요해요 — 5353같이 1024 이상 포트로 바꾸거나 관리자 권한으로 실행해 주세요.", addr, kind)
		}
		return fmt.Sprintf("%s(%s) 주소를 열 권한이 없어요. Windows라면 예약된 포트 범위(Hyper-V·WSL·Docker)이거나 방화벽이 막은 것일 수 있어요 — 다른 포트로 바꿔 주세요.", addr, kind)
	case errors.Is(err, syscall.EADDRINUSE):
		return fmt.Sprintf("%s(%s) 포트가 이미 사용 중이에요. 다른 포트로 바꾸거나 그 포트를 쓰는 프로그램을 종료해 주세요.", addr, kind)
	default:
		return fmt.Sprintf("%s(%s) 주소를 여는 데 실패했어요: %v", addr, kind, unwrapListenErr(err))
	}
}

// isPrivilegedPort reports whether addr's port is in the privileged range
// (1–1023), which on Unix needs elevated rights to bind.
func isPrivilegedPort(addr string) bool {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	p, err := strconv.Atoi(portStr)
	if err != nil {
		return false
	}
	return p > 0 && p < 1024
}

// unwrapListenErr strips the redundant "listen tcp <addr>:" wrapper that the net
// package adds, so the friendly message (which already shows the address) does
// not repeat it.
func unwrapListenErr(err error) error {
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Err != nil {
		return opErr.Err
	}
	return err
}
