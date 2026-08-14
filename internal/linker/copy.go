package linker

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/cutmail/kata/internal/copyfs"
	"github.com/cutmail/kata/internal/digest"
)

// ErrModified は kata が配置したコピーが、配置後に編集されたことを示す。
//
// ErrOccupied と分けているのは、利用者に伝えるべきことが違うため。
// 占有は「それは kata のものではない」、こちらは「kata のものだったが、
// あなたが手を入れた」であり、直し方も変わる。
var ErrModified = errors.New("destination was modified after kata deployed it")

// CopyRequest は copy 戦略での配置要求。
type CopyRequest struct {
	// Dest は配置先、Src は複製元。
	Dest, Src string
	// Force が真なら、kata のものでない実体を退避してから配置する。
	Force bool
	// BackupDir は退避先のルート、Stamp はそのサブディレクトリ名。
	BackupDir string
	Stamp     string
	// Known は state に記録された前回の内容ダイジェスト。
	// 配置先の実際の内容がこれと一致するときだけ、kata の配置と判断する。
	Known string
	// LinkTarget は前回 link 戦略で配置していた場合のリンク先。
	// link から copy へ切り替えたときに、自分が張ったリンクだと判定するために使う。
	LinkTarget string
	// Adopt が真なら、配置先の内容がこれから置く内容と完全に同じ場合に、
	// ファイルには触れないまま kata の管理下として引き取る。
	Adopt bool
}

// ApplyCopy は Src の内容を Dest へ複製する。
//
// 返す digest は配置後の内容ダイジェストで、state へ記録して次回の判定に使う。
// symlink と違って実体には「誰が置いたか」が書かれていないため、この記録だけが
// 撤去や上書きを正当化する根拠になる。
func ApplyCopy(req CopyRequest) (Result, string, error) {
	// 複製元に symlink が含まれていればここで弾かれる。
	// copy 戦略は symlink を配置しないので、判定の前提と配置の中身を一致させる。
	want, err := digest.Tree(req.Src)
	if err != nil {
		return Unchanged, "", fmt.Errorf("cannot hash %s: %w", req.Src, err)
	}
	if err := os.MkdirAll(filepath.Dir(req.Dest), 0o755); err != nil {
		return Unchanged, "", err
	}

	fi, err := os.Lstat(req.Dest)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := copySwap(req.Dest, req.Src); err != nil {
			return Unchanged, "", err
		}
		return Created, want, nil
	case err != nil:
		return Unchanged, "", err
	}

	if fi.Mode()&os.ModeSymlink != 0 {
		return applyCopyOverLink(req, want)
	}
	return applyCopyOverReal(req, want)
}

// applyCopyOverLink は配置先が symlink だった場合を扱う。
func applyCopyOverLink(req CopyRequest, want string) (Result, string, error) {
	cur, err := os.Readlink(req.Dest)
	if err != nil {
		return Unchanged, "", err
	}
	// 前回 kata が張ったリンクなら、link から copy への切り替えとして差し替える。
	if req.LinkTarget != "" && cur == req.LinkTarget {
		if err := copySwap(req.Dest, req.Src); err != nil {
			return Unchanged, "", err
		}
		return Updated, want, nil
	}
	return replaceIfForced(req, want, fmt.Errorf("%w: %s", ErrOccupied, req.Dest))
}

// applyCopyOverReal は配置先に実体があった場合を扱う。
func applyCopyOverReal(req CopyRequest, want string) (Result, string, error) {
	actual, derr := digest.Tree(req.Dest)
	if derr != nil {
		// 内容を確かめられない以上、kata が置いたものだと言い切れない。
		// 検証できないものは絶対に上書きしない。
		return replaceIfForced(req, want,
			fmt.Errorf("%w: %s: cannot verify the contents: %v", ErrOccupied, req.Dest, derr))
	}

	switch {
	case actual == want:
		// 既に置くべき内容と同じ。ディスクを触る必要がない。
		if req.Known != "" {
			return Unchanged, want, nil
		}
		if req.Adopt {
			// 記録が無いものを管理下に取り込む。以後 kata が撤去できるようになる
			// ため、黙って行わず呼び出し側に伝える。
			return Adopted, want, nil
		}
		// 記録が無いので kata のものとは言えない。引き取りは明示的な指示を要求する。
		return Unchanged, "", fmt.Errorf(
			"%w: %s (the contents already match; pass --adopt to take ownership)", ErrOccupied, req.Dest)

	case digest.Equal(actual, req.Known):
		// kata が置いたまま手が入っていない。差し替えてよい。
		if err := copySwap(req.Dest, req.Src); err != nil {
			return Unchanged, "", err
		}
		return Updated, want, nil

	case req.Known != "":
		// kata が置いたが、その後で編集された。黙って捨てない。
		return replaceIfForced(req, want, fmt.Errorf("%w: %s", ErrModified, req.Dest))

	default:
		return replaceIfForced(req, want, fmt.Errorf("%w: %s", ErrOccupied, req.Dest))
	}
}

// replaceIfForced は Force が指定されていれば退避してから配置し、
// そうでなければ渡された理由をそのまま失敗として返す。
func replaceIfForced(req CopyRequest, want string, refusal error) (Result, string, error) {
	if !req.Force {
		return Unchanged, "", refusal
	}
	if err := backup(req.Dest, req.BackupDir, req.Stamp); err != nil {
		return Unchanged, "", err
	}
	if err := copySwap(req.Dest, req.Src); err != nil {
		return Unchanged, "", err
	}
	return Updated, want, nil
}

// RemoveCopy は state に記録されたダイジェストと一致する実体だけを取り除く。
//
// 一致しない（＝利用者が編集した）場合は false を返し、何も消さない。
// os.RemoveAll は取り返しがつかないので、「配置した時点から変わっていない」という
// 証明が取れたときにだけ実行する。証明できない場合は必ず残す側に倒す。
func RemoveCopy(dest, known string) (bool, error) {
	// 記録が無ければ、kata が置いたことを証明できない。
	if known == "" {
		return false, nil
	}
	fi, err := os.Lstat(dest)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// symlink は link 戦略の管轄。取り違えて消さないよう防御的に外す。
	if fi.Mode()&os.ModeSymlink != 0 {
		return false, nil
	}
	actual, err := digest.Tree(dest)
	if err != nil {
		// 確かめられないなら触らない。
		return false, nil
	}
	if !digest.Equal(actual, known) {
		// 編集されている。残したことを利用者へ伝えるのは呼び出し側の仕事。
		return false, nil
	}
	if err := os.RemoveAll(dest); err != nil {
		return false, err
	}
	return true, nil
}

// copySwap は複製を作り切ってから配置先と入れ替える。
//
// os.Rename は中身のあるディレクトリの上には失敗するため、symlink 同士で使っている
// swap は通用しない。代わりに
//  1. 新しい内容を同じ親の一時ディレクトリに完成させる
//  2. 既存があれば横へどける
//  3. 新しいものを配置先へ rename する（失敗したら 2 を戻す）
//
// という順で入れ替える。2 と 3 の間だけ配置先が存在しない瞬間があるが、
// ディレクトリの入れ替えではこれ以上詰められない。
//
// 一時ディレクトリを必ず配置先と同じ親に作るのは、別のファイルシステムを
// またぐと rename が原子的でなくなるため。
func copySwap(dest, src string) error {
	parent := filepath.Dir(dest)
	base := filepath.Base(dest)

	newDir, err := os.MkdirTemp(parent, ".kata-new-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(newDir) }()

	staged := filepath.Join(newDir, base)
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		_, err = copyfs.Dir(src, staged)
	} else {
		_, err = copyfs.File(src, staged)
	}
	if err != nil {
		return err
	}

	var parked, oldDir string
	if _, err := os.Lstat(dest); err == nil {
		oldDir, err = os.MkdirTemp(parent, ".kata-old-")
		if err != nil {
			return err
		}
		parked = filepath.Join(oldDir, base)
		if err := os.Rename(dest, parked); err != nil {
			_ = os.RemoveAll(oldDir)
			return err
		}
	}
	if err := os.Rename(staged, dest); err != nil {
		// 入れ替えに失敗したら、どけたものを戻す。
		if parked != "" {
			if rerr := os.Rename(parked, dest); rerr != nil {
				// 戻せなかった。ここで oldDir を片付けると利用者の実体ごと消える。
				// 消さずに残し、どこにあるかを必ず伝える。
				return fmt.Errorf(
					"could not put %s back after a failed swap (%v); "+
						"the original is preserved at %s: %w", dest, rerr, parked, err)
			}
		}
		if oldDir != "" {
			_ = os.RemoveAll(oldDir)
		}
		return err
	}
	if oldDir != "" {
		_ = os.RemoveAll(oldDir)
	}
	return nil
}
