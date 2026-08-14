package app

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/cutmail/kata/internal/state"
)

// processLock は ~/.kata 全体に対する排他。
//
// state.json は「読み込み → 変更 → 全体を書き戻す」形で更新される。2 つの kata が
// 同時に走ると後勝ちで記録が消え、配置されているのに記録が無い状態が生まれる。
// そうなると kata は二度とその配置を撤去できず、掃除の対象にもならないので
// 回収もできない。記録を読み直してから書き終えるまでを、この錠で囲う。
type processLock struct{ f *os.File }

// lockKataHome は ~/.kata の排他を取る。取れるまで待つ。
// 待つ相手は同じ利用者の別の kata であり、いずれ終わる。
func lockKataHome(kataHome string) (*processLock, error) {
	if kataHome == "" {
		return nil, fmt.Errorf("the kata home directory is not configured")
	}
	// 記録もキャッシュも取得元の可視性を引き継ぐべきなので、本人だけが読める権限で作る。
	if err := os.MkdirAll(kataHome, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(kataHome, "lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("cannot lock %s: %w", path, err)
	}
	return &processLock{f: f}, nil
}

// release は錠を返す。錠のファイル自体は次回のために残す。
func (l *processLock) release() {
	if l == nil || l.f == nil {
		return
	}
	_ = unlockFile(l.f)
	_ = l.f.Close()
}

// mutate は排他を取り、記録を読み直してから fn を実行する。
//
// 錠を待っている間に別のプロセスが記録を書き換えている可能性があるため、
// 読み直しは必須。読み直さずに書き戻すと、待っていた側がその変更を消す。
func (a *App) mutate(fn func() error) error {
	l, err := lockKataHome(a.cfg.KataHome)
	if err != nil {
		return err
	}
	defer l.release()

	st, err := state.Load(a.cfg.StatePath())
	if err != nil {
		return err
	}
	a.st = st
	return fn()
}
