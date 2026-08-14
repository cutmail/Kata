package mcpserver

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/cutmail/kata/internal/app"
	"github.com/cutmail/kata/internal/manifest"
)

// 各ツールの Dir フィールドには次のいずれかと同じ説明を jsonschema タグとして手書きする
// (struct タグは定数を展開できないため共有できない):
//   "directory to start looking for kata.yml upward from (like running the kata CLI after "
//   "'cd' there); defaults to the server's own working directory"

func registerTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "kata_init",
		Description: "Create a kata.yml and a local/ directory in dir.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: ptr(false)},
	}, handleInit)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "kata_list",
		Description: "Show every package declared in the manifest and its current deployment state.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, handleList)

	mcp.AddTool(server, &mcp.Tool{
		Name: "kata_status",
		Description: "Report only the packages that are out of sync with the manifest. " +
			"Having drifted entries is a normal result, not a tool failure.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, handleStatus)

	mcp.AddTool(server, &mcp.Tool{
		Name: "kata_doctor",
		Description: "Inspect the manifest, lock, deployment target and cache, and report anything " +
			"that needs attention. Runs entirely offline. Findings are a normal result, not a tool failure.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, handleDoctor)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "kata_add",
		Description: "Add a package to the manifest and, unless no_sync is set, deploy it immediately.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: ptr(false), IdempotentHint: false},
	}, handleAdd)

	mcp.AddTool(server, &mcp.Tool{
		Name: "kata_sync",
		Description: "Make the deployed state match the manifest. Safe to call repeatedly. " +
			"With dry_run it only previews changes and touches nothing.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: ptr(true), IdempotentHint: true},
	}, handleSync)

	mcp.AddTool(server, &mcp.Tool{
		Name: "kata_import",
		Description: "Adopt entries already present in the deployment target into the manifest. " +
			"By default nothing in the deployment target is touched (files are only copied into local/); " +
			"with adopt=true the originals are moved into the backup directory and replaced with a " +
			"kata-managed link, which is the destructive variant of this tool.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: ptr(true)},
	}, handleImport)

	mcp.AddTool(server, &mcp.Tool{
		Name: "kata_update",
		Description: "Re-resolve floating refs and move the lock forward. " +
			"Unlike kata_sync, dry_run still reaches the network to resolve refs " +
			"(only the lock write and deploy are skipped); it is not a network-free preview.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: ptr(true)},
	}, handleUpdate)

	mcp.AddTool(server, &mcp.Tool{
		Name: "kata_prune",
		Description: "Remove cached content nothing refers to any more. Nothing is removed unless " +
			"apply=true; without it this only previews what would be freed.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: ptr(true)},
	}, handlePrune)

	mcp.AddTool(server, &mcp.Tool{
		Name: "kata_remove",
		Description: "Remove a package from the manifest and undeploy it. " +
			"There is no dry-run for this tool — call kata_list first to confirm the target.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: ptr(true), IdempotentHint: true},
	}, handleRemove)
}

// ---- kata_init ----

type initInput struct {
	Dir string `json:"dir,omitempty" jsonschema:"directory to create kata.yml in; defaults to the server's own working directory"`
}

type initOutput struct {
	Path string `json:"path"`
}

func handleInit(_ context.Context, _ *mcp.CallToolRequest, in initInput) (*mcp.CallToolResult, initOutput, error) {
	dir := in.Dir
	if dir == "" {
		dir = "."
	}
	path, err := app.Init(dir)
	return nil, initOutput{Path: path}, err
}

// ---- kata_list ----

type listInput struct {
	Dir string `json:"dir,omitempty" jsonschema:"directory to start looking for kata.yml upward from (like running the kata CLI after 'cd' there); defaults to the server's own working directory"`
}

type listOutput struct {
	Items []app.Item `json:"items"`
}

func handleList(ctx context.Context, _ *mcp.CallToolRequest, in listInput) (*mcp.CallToolResult, listOutput, error) {
	dir, err := resolveDir(in.Dir)
	if err != nil {
		return nil, listOutput{Items: []app.Item{}}, err
	}
	a, _, err := app.OpenFrom(dir)
	if err != nil {
		return nil, listOutput{Items: []app.Item{}}, err
	}
	items, err := a.List(ctx)
	if items == nil {
		items = []app.Item{}
	}
	return nil, listOutput{Items: items}, err
}

// ---- kata_status ----

type statusInput struct {
	Dir string `json:"dir,omitempty" jsonschema:"directory to start looking for kata.yml upward from (like running the kata CLI after 'cd' there); defaults to the server's own working directory"`
}

func handleStatus(ctx context.Context, _ *mcp.CallToolRequest, in statusInput) (*mcp.CallToolResult, app.StatusSummary, error) {
	dir, err := resolveDir(in.Dir)
	if err != nil {
		return nil, app.StatusSummary{}, err
	}
	a, _, err := app.OpenFrom(dir)
	if err != nil {
		return nil, app.StatusSummary{}, err
	}
	sum, err := a.StatusSummary(ctx)
	if sum == nil {
		return nil, app.StatusSummary{}, err
	}
	return nil, *sum, err
}

// ---- kata_doctor ----

type doctorInput struct {
	Dir string `json:"dir,omitempty" jsonschema:"directory to start looking for kata.yml upward from (like running the kata CLI after 'cd' there); defaults to the server's own working directory. Unlike the other tools, doctor still works when no kata.yml exists yet."`
}

func handleDoctor(ctx context.Context, _ *mcp.CallToolRequest, in doctorInput) (*mcp.CallToolResult, app.DoctorReport, error) {
	dir, err := resolveDir(in.Dir)
	if err != nil {
		return nil, app.DoctorReport{}, err
	}
	// doctor はマニフェストが無くても診断できる既定を踏襲する（Loose）。
	cfg, err := app.DefaultConfigLoose(dir)
	if err != nil {
		return nil, app.DoctorReport{}, err
	}
	rep, err := app.Diagnose(ctx, cfg)
	if rep == nil {
		return nil, app.DoctorReport{}, err
	}
	return nil, *rep, err
}

// ---- kata_add ----

type addInput struct {
	Dir      string   `json:"dir,omitempty" jsonschema:"directory to start looking for kata.yml upward from (like running the kata CLI after 'cd' there); defaults to the server's own working directory"`
	Source   string   `json:"source" jsonschema:"the package source: owner/repo, a git URL, an archive URL, or a local path inside the manifest directory. A local path must start with './', '../', or be absolute -- it is resolved against dir, not against this server process's own working directory"`
	Name     string   `json:"name,omitempty" jsonschema:"package name (defaults to the last path element)"`
	Type     string   `json:"type,omitempty" jsonschema:"skill, command, or agent (inferred when omitted; agent must be explicit)"`
	Path     string   `json:"path,omitempty" jsonschema:"subdirectory inside the git repository or archive"`
	Ref      string   `json:"ref,omitempty" jsonschema:"branch or tag (defaults to the default branch)"`
	URL      bool     `json:"url,omitempty" jsonschema:"treat source as an archive URL rather than a git repository"`
	Scope    string   `json:"scope,omitempty" jsonschema:"user or project (defaults to user)"`
	Strategy string   `json:"strategy,omitempty" jsonschema:"link, copy, or auto (defaults to link)"`
	Profiles []string `json:"profiles,omitempty" jsonschema:"profiles this package belongs to"`
	NoSync   bool     `json:"no_sync,omitempty" jsonschema:"only update the manifest, do not deploy"`
}

type addOutput struct {
	Package manifest.Package `json:"package"`
	Sync    *app.SyncReport  `json:"sync,omitempty"`
}

// resolveLocalSource は明示的にローカルパスだと分かる source (相対の "./"・".." 始まり)
// を dir を基点にした絶対パスへ変換する。
//
// internal/app 側の source 解決は os.Getwd() 相対で組み立てられており、CLI では
// それがそのままマニフェストのディレクトリと一致するので問題にならない。しかし MCP には
// 「カレントディレクトリ」に相当するものがなく、dir はサーバープロセス自身の作業
// ディレクトリとは無関係なことがあるため、ここで先に絶対パスへ直しておく必要がある。
// owner/repo のような git 短縮形や URL、既に絶対パスのものはそのまま通す —
// 「同名のファイルが実在すればローカル扱いにする」という internal/app 側のあいまいな
// 判定は、プロセスの実 cwd に依存する挙動のため MCP 側では意図的に再現しない。
func resolveLocalSource(dir, source string) string {
	if filepath.IsAbs(source) || strings.Contains(source, "://") {
		return source
	}
	if source == "." || strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../") {
		return filepath.Join(dir, source)
	}
	return source
}

func handleAdd(ctx context.Context, _ *mcp.CallToolRequest, in addInput) (*mcp.CallToolResult, addOutput, error) {
	dir, err := resolveDir(in.Dir)
	if err != nil {
		return nil, addOutput{}, err
	}
	a, _, err := app.OpenFrom(dir)
	if err != nil {
		return nil, addOutput{}, err
	}
	pkg, err := a.Add(ctx, app.AddSpec{
		Source:   resolveLocalSource(dir, in.Source),
		Name:     in.Name,
		Type:     in.Type,
		Path:     in.Path,
		Ref:      in.Ref,
		IsURL:    in.URL,
		Scope:    in.Scope,
		Strategy: in.Strategy,
		Profiles: in.Profiles,
	})
	if err != nil {
		return nil, addOutput{}, err
	}
	if in.NoSync {
		return nil, addOutput{Package: pkg}, nil
	}

	// マニフェストを書き換えたので読み直してから同期する（cmd/kata の add と同じ手順）。
	a, _, err = app.OpenFrom(dir)
	if err != nil {
		return nil, addOutput{Package: pkg}, err
	}
	rep, serr := a.Sync(ctx, app.SyncOptions{})
	normalizeSync(rep)
	return nil, addOutput{Package: pkg, Sync: rep}, serr
}

// ---- kata_sync ----

type syncInput struct {
	Dir     string `json:"dir,omitempty" jsonschema:"directory to start looking for kata.yml upward from (like running the kata CLI after 'cd' there); defaults to the server's own working directory"`
	DryRun  bool   `json:"dry_run,omitempty" jsonschema:"preview changes without touching anything"`
	Force   bool   `json:"force,omitempty" jsonschema:"move an existing file into the backup directory before deploying"`
	Profile string `json:"profile,omitempty" jsonschema:"only deploy packages in this profile"`
	Prune   bool   `json:"prune,omitempty" jsonschema:"also undeploy packages the profile leaves out"`
	Adopt   bool   `json:"adopt,omitempty" jsonschema:"take ownership of a copied destination whose contents already match"`
}

func handleSync(ctx context.Context, _ *mcp.CallToolRequest, in syncInput) (*mcp.CallToolResult, app.SyncReport, error) {
	dir, err := resolveDir(in.Dir)
	if err != nil {
		return nil, app.SyncReport{}, err
	}
	a, _, err := app.OpenFrom(dir)
	if err != nil {
		return nil, app.SyncReport{}, err
	}
	rep, err := a.Sync(ctx, app.SyncOptions{
		DryRun:  in.DryRun,
		Force:   in.Force,
		Profile: in.Profile,
		Prune:   in.Prune,
		Adopt:   in.Adopt,
	})
	normalizeSync(rep)
	if rep == nil {
		return nil, app.SyncReport{}, err
	}
	return nil, *rep, err
}

func normalizeSync(rep *app.SyncReport) {
	if rep != nil && rep.Changes == nil {
		rep.Changes = []app.Change{}
	}
}

// ---- kata_import ----

type importInput struct {
	Dir    string   `json:"dir,omitempty" jsonschema:"directory to start looking for kata.yml upward from (like running the kata CLI after 'cd' there); defaults to the server's own working directory"`
	DryRun bool     `json:"dry_run,omitempty" jsonschema:"preview what would be imported without writing anything"`
	Adopt  bool     `json:"adopt,omitempty" jsonschema:"move the originals aside and link to the copies under local/ (destructive)"`
	Types  []string `json:"types,omitempty" jsonschema:"only import these types: skill, command, agent"`
	Names  []string `json:"names,omitempty" jsonschema:"only import these names"`
}

func handleImport(ctx context.Context, _ *mcp.CallToolRequest, in importInput) (*mcp.CallToolResult, app.ImportReport, error) {
	dir, err := resolveDir(in.Dir)
	if err != nil {
		return nil, app.ImportReport{}, err
	}
	a, _, err := app.OpenFrom(dir)
	if err != nil {
		return nil, app.ImportReport{}, err
	}
	rep, err := a.Import(ctx, app.ImportOptions{
		DryRun: in.DryRun,
		Adopt:  in.Adopt,
		Types:  in.Types,
		Names:  in.Names,
	})
	if rep == nil {
		return nil, app.ImportReport{}, err
	}
	if rep.Items == nil {
		rep.Items = []app.ImportItem{}
	}
	return nil, *rep, err
}

// ---- kata_update ----

type updateInput struct {
	Dir    string   `json:"dir,omitempty" jsonschema:"directory to start looking for kata.yml upward from (like running the kata CLI after 'cd' there); defaults to the server's own working directory"`
	Names  []string `json:"names,omitempty" jsonschema:"only update these packages (defaults to every declared package)"`
	DryRun bool     `json:"dry_run,omitempty" jsonschema:"resolve refs without writing the lock; still reaches the network"`
	NoSync bool     `json:"no_sync,omitempty" jsonschema:"update the lock but leave the deployment as it is"`
}

func handleUpdate(ctx context.Context, _ *mcp.CallToolRequest, in updateInput) (*mcp.CallToolResult, app.UpdateReport, error) {
	dir, err := resolveDir(in.Dir)
	if err != nil {
		return nil, app.UpdateReport{}, err
	}
	a, _, err := app.OpenFrom(dir)
	if err != nil {
		return nil, app.UpdateReport{}, err
	}
	rep, err := a.Update(ctx, app.UpdateOptions{
		Names:  in.Names,
		DryRun: in.DryRun,
		NoSync: in.NoSync,
	})
	if rep == nil {
		return nil, app.UpdateReport{}, err
	}
	if rep.Changes == nil {
		rep.Changes = []app.UpdateChange{}
	}
	normalizeSync(rep.Sync)
	return nil, *rep, err
}

// ---- kata_prune ----

type pruneInput struct {
	Dir       string `json:"dir,omitempty" jsonschema:"directory to start looking for kata.yml upward from (like running the kata CLI after 'cd' there); defaults to the server's own working directory"`
	Apply     bool   `json:"apply,omitempty" jsonschema:"actually remove the listed items (nothing is removed without it)"`
	Store     bool   `json:"store,omitempty" jsonschema:"consider cached fetches that nothing refers to"`
	Staging   bool   `json:"staging,omitempty" jsonschema:"consider leftovers from interrupted fetches"`
	State     bool   `json:"state,omitempty" jsonschema:"consider deployment records whose destination is gone"`
	OlderThan string `json:"older_than,omitempty" jsonschema:"Go duration string (e.g. '720h'); only consider items older than this"`
}

func handlePrune(ctx context.Context, _ *mcp.CallToolRequest, in pruneInput) (*mcp.CallToolResult, app.PruneReport, error) {
	dir, err := resolveDir(in.Dir)
	if err != nil {
		return nil, app.PruneReport{}, err
	}
	var olderThan time.Duration
	if in.OlderThan != "" {
		olderThan, err = time.ParseDuration(in.OlderThan)
		if err != nil {
			return nil, app.PruneReport{}, err
		}
	}
	a, _, err := app.OpenFrom(dir)
	if err != nil {
		return nil, app.PruneReport{}, err
	}
	rep, err := a.Prune(ctx, app.PruneOptions{
		Apply:     in.Apply,
		Store:     in.Store,
		Staging:   in.Staging,
		State:     in.State,
		OlderThan: olderThan,
	})
	if err != nil {
		return nil, app.PruneReport{}, err
	}
	if rep.Items == nil {
		rep.Items = []app.PruneItem{}
	}
	return nil, *rep, nil
}

// ---- kata_remove ----

type removeInput struct {
	Dir  string `json:"dir,omitempty" jsonschema:"directory to start looking for kata.yml upward from (like running the kata CLI after 'cd' there); defaults to the server's own working directory"`
	Name string `json:"name" jsonschema:"the declared package name to remove"`
}

func handleRemove(ctx context.Context, _ *mcp.CallToolRequest, in removeInput) (*mcp.CallToolResult, app.RemoveResult, error) {
	dir, err := resolveDir(in.Dir)
	if err != nil {
		return nil, app.RemoveResult{}, err
	}
	a, _, err := app.OpenFrom(dir)
	if err != nil {
		return nil, app.RemoveResult{}, err
	}
	res, err := a.Remove(ctx, in.Name)
	return nil, res, err
}
