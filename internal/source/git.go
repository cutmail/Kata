package source

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/cutmail/kata/internal/redact"
	"github.com/cutmail/kata/internal/safepath"
	"github.com/cutmail/kata/internal/store"
)

// Git は git リポジトリからの取得を担う。
type Git struct {
	Store *store.Store
}

// NewGit は Git フェッチャを返す。
func NewGit(s *store.Store) *Git { return &Git{Store: s} }

// Fetch はリポジトリをキャッシュへ用意し、サブパスを解決して返す。
// Commit が指定されていればその内容であることを保証する。
func (g *Git) Fetch(ctx context.Context, req Request) (Fetched, error) {
	// 手元のリポジトリを指す取得元は、マニフェストのディレクトリ配下に限る。
	// clone してきた kata.yml が利用者の私有リポジトリを名指しして、
	// その中身を ~/.claude へ引き込むのを防ぐ。
	if req.LocalGit {
		abs, err := filepath.Abs(strings.TrimPrefix(req.Git, "file://"))
		if err != nil {
			return Fetched{}, err
		}
		if req.BaseDir == "" {
			return Fetched{}, fmt.Errorf("a local git source requires a manifest directory")
		}
		if err := safepath.VerifyUnder(req.BaseDir, abs); err != nil {
			return Fetched{}, fmt.Errorf(
				"local git source %s must live inside the manifest directory: %w", abs, err)
		}
		if rel, rerr := filepath.Rel(req.BaseDir, abs); rerr == nil &&
			(rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return Fetched{}, fmt.Errorf(
				"local git source %s is outside the manifest directory", abs)
		}
	}

	// lock 済みで既にキャッシュがあれば取得は不要。
	if req.Commit != "" {
		key := store.RepoKey(req.Git, req.Commit)
		if g.Store.Has(key) {
			root, err := resolveSubpath(g.Store.Dir(key), req.Path)
			if err != nil {
				return Fetched{}, err
			}
			return Fetched{Root: root, Commit: req.Commit}, nil
		}
	}

	staging, err := g.Store.NewStaging()
	if err != nil {
		return Fetched{}, err
	}
	defer g.Store.Discard(staging)

	repoDir := filepath.Join(staging, "repo")
	commit, err := clone(ctx, repoDir, req.Git, req.Ref, req.Commit)
	if err != nil {
		return Fetched{}, fmt.Errorf("fetch %s: %w", redact.URL(req.Git), err)
	}
	// 小さなリポジトリでも入れ子のツリーでチェックアウトは何桁も膨らむ。
	// キャッシュへ移す前に歯止めをかける。
	if err := safepath.CheckTreeSize(repoDir, safepath.DefaultTreeLimits); err != nil {
		return Fetched{}, fmt.Errorf("fetch %s: %w", redact.URL(req.Git), err)
	}

	dir, err := g.Store.Promote(repoDir, store.RepoKey(req.Git, commit))
	if err != nil {
		return Fetched{}, err
	}
	root, err := resolveSubpath(dir, req.Path)
	if err != nil {
		return Fetched{}, err
	}
	return Fetched{Root: root, Commit: commit}, nil
}

// clone はリポジトリを dir へ取得し、解決されたコミットを返す。
// 通常は depth=1 の浅い取得で済ませ、必要なときだけ完全な取得に切り替える。
func clone(ctx context.Context, dir, url, ref, want string) (string, error) {
	repo, err := shallowClone(ctx, dir, url, ref)
	if err == nil {
		head, herr := headCommit(repo)
		if herr != nil {
			return "", herr
		}
		if want == "" || head == want {
			return head, nil
		}
	}
	// 浅い取得では目的のコミットに届かないため、完全な取得へ切り替える。
	_ = os.RemoveAll(dir)
	repo, err = git.PlainCloneContext(ctx, dir, false, &git.CloneOptions{URL: url})
	if err != nil {
		return "", err
	}
	rev := want
	if rev == "" {
		rev = ref
	}
	if rev != "" {
		hash, err := repo.ResolveRevision(plumbing.Revision(rev))
		if err != nil {
			return "", fmt.Errorf("resolve %q: %w", rev, err)
		}
		wt, err := repo.Worktree()
		if err != nil {
			return "", err
		}
		if err := wt.Checkout(&git.CheckoutOptions{Hash: *hash}); err != nil {
			return "", err
		}
	}
	return headCommit(repo)
}

// shallowClone は ref をブランチ、次にタグとして解釈しながら浅い取得を試みる。
func shallowClone(ctx context.Context, dir, url, ref string) (*git.Repository, error) {
	candidates := []plumbing.ReferenceName{""}
	if ref != "" {
		candidates = []plumbing.ReferenceName{
			plumbing.NewBranchReferenceName(ref),
			plumbing.NewTagReferenceName(ref),
		}
	}
	var lastErr error
	for _, name := range candidates {
		opts := &git.CloneOptions{URL: url, Depth: 1, SingleBranch: true, Tags: git.NoTags}
		if name != "" {
			opts.ReferenceName = name
		}
		repo, err := git.PlainCloneContext(ctx, dir, false, opts)
		if err == nil {
			return repo, nil
		}
		lastErr = err
		_ = os.RemoveAll(dir)
	}
	return nil, lastErr
}

func headCommit(repo *git.Repository) (string, error) {
	head, err := repo.Head()
	if err != nil {
		return "", err
	}
	return head.Hash().String(), nil
}
