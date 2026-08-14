package app

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/cutmail/kata/internal/safepath"
	"github.com/cutmail/kata/internal/store"
)

// liveStoreKeys は「今も使われている」キャッシュキーの集合を返す。
//
// 判定の根拠は 2 つ。現在の kata.lock が指すものと、**すべての origin** の
// 配置実績が指すもの。
//
// state.json を origin で絞らないのが要点になる。~/.kata/store はマシン全体で
// 共有されているため、このマニフェストの lock だけを見て孤児と判断すると、
// 同じマシンにある別のリポジトリが使っているキャッシュを消してしまう。
//
// 漏れるのは「別のマニフェストで宣言済みだが一度も sync していない」ものだけで、
// それが消えても再取得で復元できる（store は純粋なキャッシュ）。
func (a *App) liveStoreKeys() map[string]bool {
	live := map[string]bool{}

	for _, e := range a.lock.Resolved {
		if u, ok := strings.CutPrefix(e.Source, "git+"); ok && e.Commit != "" {
			live[store.RepoKey(u, e.Commit)] = true
		}
		// url 取得元にはコミットが無く、ダイジェストがピンの役割を果たす。
		// ここを見落とすと、lock で固定した書庫のキャッシュを孤児と誤認して消す。
		// 上流が消えていればその版は二度と復元できず、lock の意味が失われる。
		if u, ok := strings.CutPrefix(e.Source, "url+"); ok && e.Digest != "" {
			live[store.ArchiveKey(u, e.Digest)] = true
		}
	}

	reposRoot := a.store.ReposRoot()
	for _, e := range a.st.Entries {
		if key, ok := storeKeyOf(reposRoot, e.Target); ok {
			live[key] = true
		}
	}
	return live
}

// storeKeyOf は配置が指している先から、キャッシュのキーを取り出す。
// キャッシュの外を指している（local 取得元など）なら偽を返す。
func storeKeyOf(reposRoot, target string) (string, bool) {
	if target == "" {
		return "", false
	}
	rel, err := filepath.Rel(reposRoot, target)
	if err != nil || rel == "." {
		return "", false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	key, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
	if key == "" {
		return "", false
	}
	return key, true
}

// deadStateEntries は配置先を失った記録を返す。
//
// 落としてよいのは「配置先がもう存在しない」ものだけ。別のものに置き換わって
// いる場合は残す。撤去は記録と実体の突き合わせで安全性を担保しており、
// 記録を消すと「kata がそこに置いた」という事実そのものが失われるため。
func (a *App) deadStateEntries() []stateRef {
	var out []stateRef
	for _, e := range a.st.Entries {
		if _, err := os.Lstat(e.Dest); os.IsNotExist(err) {
			out = append(out, stateRef{Origin: e.Origin, Name: e.Name, Dest: e.Dest})
		}
	}
	return out
}

// stateRef は state のエントリを指す最小の情報。
type stateRef struct {
	Origin string
	Name   string
	Dest   string
}

// dirSize はディレクトリ以下の合計バイト数を返す。読めないものは数えない。
func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			total += fi.Size()
		}
		return nil
	})
	return total
}

// safeUnder は path が root の内側にあることを確かめる。
//
// 掃除の削除は必ずこれを通す。設定の取り違えやパスの組み立てミスで、
// ホームディレクトリのような想定外の場所を消してしまう事故を防ぐため。
func safeUnder(root, path string) bool {
	if root == "" || path == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." {
		return false
	}
	if strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	// 字句の判定だけでは足りない。root やその途中が symlink だと、
	// 内側に見えるパスがリンク先の実体を指し、掃除がそこを消してしまう。
	return safepath.VerifyUnder(root, path) == nil && safepath.NotSymlink(root) == nil
}
