package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/vitus9988/psdns/internal/selfupdate"
)

// cliBinary is the base name self-update extracts from a release archive for the
// CLI, as opposed to the GUI's "psdns-gui". See internal/selfupdate.Checker.Binary.
const cliBinary = "psdns"

// runUpdate implements `psdns update`: it checks GitHub Releases for a newer
// build and, unless -check is given, downloads it, verifies the published
// SHA-256 checksum, and atomically replaces the running executable. The CLI does
// not restart itself; long-running `proxy`/`run` processes must be restarted to
// pick up the new binary.
func runUpdate(args []string) {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	checkOnly := fs.Bool("check", false, "only report whether a newer release exists; do not download or replace")
	timeout := fs.Duration("timeout", 20*time.Second, "network timeout for the check/download")
	_ = fs.Parse(args)

	ck := selfupdate.NewChecker(&http.Client{Timeout: *timeout})
	ck.Binary = cliBinary

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	res, err := ck.Check(ctx, true)
	if err != nil {
		if errors.Is(err, selfupdate.ErrRateLimited) {
			fmt.Fprintln(os.Stderr, "psdns: GitHub API 요청이 일시적으로 제한됐어요. 잠시 후 다시 시도해 주세요.")
		} else {
			fmt.Fprintf(os.Stderr, "psdns: 업데이트 확인에 실패했어요: %v\n", err)
		}
		os.Exit(1)
	}

	fmt.Printf("현재 버전: %s\n", res.Current)
	if res.Latest != "" {
		fmt.Printf("최신 릴리즈: %s\n", res.Latest)
	}

	if !res.Newer {
		if selfupdate.IsReleaseVersion(res.Current) {
			fmt.Println("이미 최신 버전을 쓰고 있어요.")
		} else {
			fmt.Println("현재 빌드(dev 등)는 정식 릴리즈가 아니라 자동 업데이트 대상이 아니에요.")
			if res.ReleaseURL != "" {
				fmt.Printf("릴리즈 바이너리로 교체하려면 받아 주세요: %s\n", res.ReleaseURL)
			}
		}
		return
	}

	if *checkOnly {
		fmt.Printf("새 버전 %s 가 있어요. 'psdns update' 로 설치할 수 있어요.\n", res.Latest)
		return
	}

	if !res.Available {
		fmt.Printf("새 버전 %s 가 있지만 이 플랫폼용 릴리즈 파일(%s)을 찾지 못했어요.\n", res.Latest, res.AssetName)
		if res.ReleaseURL != "" {
			fmt.Printf("릴리즈 페이지: %s\n", res.ReleaseURL)
		}
		return
	}

	fmt.Printf("새 버전 %s 를 내려받아 설치할게요...\n", res.Latest)
	if err := ck.Apply(ctx, updateProgress); err != nil {
		if errors.Is(err, selfupdate.ErrChecksumMismatch) {
			fmt.Fprintln(os.Stderr, "\npsdns: 내려받은 파일 검증(체크섬)에 실패해 교체하지 않았어요.")
		} else {
			fmt.Fprintf(os.Stderr, "\npsdns: 업데이트에 실패했어요: %v\n", err)
		}
		if res.ReleaseURL != "" {
			fmt.Fprintf(os.Stderr, "릴리즈 페이지에서 직접 받을 수 있어요: %s\n", res.ReleaseURL)
		}
		os.Exit(1)
	}
	fmt.Printf("psdns 를 %s 로 업데이트했어요. 다시 실행하면 새 버전이 적용돼요.\n", res.Latest)
}

// updateProgress renders Apply's overall progress (0..1) as a single rewritten
// stdout line, breaking to a new line when the replacement is done.
func updateProgress(stage selfupdate.Stage, pct float64) {
	fmt.Printf("\r  %-12s %3.0f%%", stageLabel(stage), pct*100)
	if stage == selfupdate.StageDone {
		fmt.Println()
	}
}

func stageLabel(s selfupdate.Stage) string {
	switch s {
	case selfupdate.StageDownload:
		return "내려받는 중"
	case selfupdate.StageVerify:
		return "검증 중"
	case selfupdate.StageExtract:
		return "압축 해제"
	case selfupdate.StageReplace:
		return "교체 중"
	case selfupdate.StageDone:
		return "완료"
	default:
		return "준비 중"
	}
}

// notifyUpdate runs a best-effort background check at startup and logs a single
// hint line when a newer release exists. It never applies anything and stays
// silent on error, on the current build being up to date, and on dev/non-release
// builds (which never report newer). Safe to call as `go notifyUpdate()`.
func notifyUpdate() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ck := selfupdate.NewChecker(&http.Client{Timeout: 5 * time.Second})
	ck.Binary = cliBinary
	res, err := ck.Check(ctx, false)
	if err != nil || !res.Newer {
		return
	}
	log.Printf("psdns: 새 버전 %s 가 있어요 (현재 %s) — 'psdns update' 로 갱신하세요", res.Latest, res.Current)
}
