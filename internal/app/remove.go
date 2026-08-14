package app

import (
	"context"
	"fmt"
)

// RemoveResult は削除操作の結果。
type RemoveResult struct {
	Name string
	Dest string
	// Unlinked は実際にリンクを取り除いたかを示す。
	Unlinked bool
	// Warning は配置先に手が加わっていた場合などの注意。
	Warning string
}

// Remove は宣言と配置の両方からパッケージを取り除く。
func (a *App) Remove(_ context.Context, name string) (RemoveResult, error) {
	var res RemoveResult
	var opErr error
	if err := a.mutate(func() error {
		res, opErr = a.remove(name)
		return nil
	}); err != nil {
		return res, err
	}
	return res, opErr
}

func (a *App) remove(name string) (RemoveResult, error) {
	res := RemoveResult{Name: name}

	if _, ok := a.man.Find(name); !ok {
		if _, tracked := a.st.Get(a.Origin(), name); !tracked {
			return res, fmt.Errorf("package %q is not declared in %s", name, a.cfg.ManifestPath)
		}
	}

	origin := a.Origin()
	if e, ok := a.st.Get(origin, name); ok {
		res.Dest = e.Dest
		unlinked, err := a.undeploy(e)
		if err != nil {
			return res, err
		}
		res.Unlinked = unlinked
		if !unlinked {
			res.Warning = "left in place: not a kata-managed link anymore"
		}
		a.st.Delete(origin, name)
	}

	a.man.Remove(name)
	a.lock.Delete(name)

	if err := a.man.Save(a.cfg.ManifestPath); err != nil {
		return res, err
	}
	if err := a.persist(); err != nil {
		return res, err
	}
	return res, nil
}
