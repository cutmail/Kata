// kata は agent 向けの skill / command をマニフェストで管理する CLI。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/cutmail/kata/internal/app"
	"github.com/cutmail/kata/internal/manifest"
)

// version はビルド時に -ldflags で差し替える。
var version = "0.1.0-dev"

// exitError は「出力は済ませたので、終了コードだけ変えたい」ことを表す。
// 診断系コマンドが報告する問題は実行の失敗ではないため、Error: 行を出さずに終了する。
type exitError struct{ code int }

func (e *exitError) Error() string { return "" }

func main() {
	if err := newRootCmd().Execute(); err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			os.Exit(ee.code)
		}
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "kata",
		Short:         "Manage agent skills and commands from a single manifest",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newInitCmd(), newAddCmd(), newSyncCmd(), newListCmd(), newRemoveCmd(),
		newStatusCmd(), newImportCmd(), newUpdateCmd(), newDoctorCmd(), newPruneCmd(), newMCPCmd())
	return root
}

// openApp はカレントディレクトリを起点にマニフェストを探して App を開く。
func openApp() (*app.App, app.Config, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, app.Config{}, err
	}
	return app.OpenFrom(wd)
}

// encodeJSON は --json 系フラグの出力形式を統一する。
func encodeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func newInitCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "init [dir]",
		Short: "Create a kata.yml in the current directory",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			path, err := app.Init(dir)
			if err != nil {
				return err
			}
			if asJSON {
				return encodeJSON(initResult{Path: path})
			}
			fmt.Printf("created %s\n", short(path))
			fmt.Println("put your own skills under local/ and register them with 'kata add'")
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the result as JSON")
	return cmd
}

// initResult は init --json の出力形。
type initResult struct {
	Path string `json:"path"`
}

func newAddCmd() *cobra.Command {
	var spec app.AddSpec
	var noSync, asJSON bool

	cmd := &cobra.Command{
		Use:   "add <source>",
		Short: "Add a package to the manifest and deploy it",
		Long: "Add a package to the manifest.\n\n" +
			"The source is either a git repository (owner/repo, github.com/owner/repo, or a full URL)\n" +
			"or a path inside the manifest directory (./local/skills/my-skill).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, cfg, err := openApp()
			if err != nil {
				return err
			}
			spec.Source = args[0]
			ctx := context.Background()

			p, err := a.Add(ctx, spec)
			if err != nil {
				return err
			}
			if !asJSON {
				fmt.Printf("added %s (%s) to %s\n", p.Name, p.Type, short(cfg.ManifestPath))
			}
			if noSync {
				if asJSON {
					return encodeJSON(addResult{Package: p})
				}
				fmt.Println("run 'kata sync' to deploy it")
				return nil
			}

			// マニフェストを書き換えたので読み直してから同期する。
			a, _, err = openApp()
			if err != nil {
				return err
			}
			rep, serr := a.Sync(ctx, app.SyncOptions{})
			if asJSON {
				if jerr := encodeJSON(addResult{Package: p, Sync: rep}); jerr != nil {
					return jerr
				}
				return serr
			}
			printSync(rep)
			return serr
		},
	}
	cmd.Flags().StringVar(&spec.Name, "name", "", "package name (defaults to the last path element)")
	cmd.Flags().StringVar(&spec.Type, "type", "", "package type: skill or command")
	cmd.Flags().StringVar(&spec.Path, "path", "", "subdirectory inside the git repository")
	cmd.Flags().StringVar(&spec.Ref, "ref", "", "branch or tag (defaults to the default branch)")
	cmd.Flags().BoolVar(&spec.IsURL, "url", false, "treat the source as an archive URL rather than a git repository")
	cmd.Flags().StringVar(&spec.Scope, "scope", "", "deployment scope: user or project (defaults to user)")
	cmd.Flags().StringVar(&spec.Strategy, "strategy", "",
		"deployment strategy: link, copy or auto (defaults to link)")
	cmd.Flags().StringSliceVar(&spec.Profiles, "profile", nil, "profiles this package belongs to (repeatable)")
	cmd.Flags().BoolVar(&noSync, "no-sync", false, "only update the manifest, do not deploy")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the result as JSON")
	return cmd
}

// addResult は add --json の出力形。
type addResult struct {
	Package manifest.Package `json:"package"`
	Sync    *app.SyncReport  `json:"sync,omitempty"`
}

func newSyncCmd() *cobra.Command {
	var opts app.SyncOptions
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Make the deployed state match the manifest",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, _, err := openApp()
			if err != nil {
				return err
			}
			// 作業マシンごとの既定を shell に一度書けば済むようにする。
			if opts.Profile == "" {
				opts.Profile = os.Getenv("KATA_PROFILE")
			}
			rep, err := a.Sync(context.Background(), opts)
			if asJSON {
				if rep != nil && rep.Changes == nil {
					rep.Changes = []app.Change{}
				}
				if jerr := encodeJSON(rep); jerr != nil {
					return jerr
				}
				return err
			}
			printSync(rep)
			return err
		},
	}
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "show what would change without touching anything")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "move an existing file out of the way into the backup directory")
	cmd.Flags().StringVar(&opts.Profile, "profile", "", "only deploy packages in this profile (defaults to $KATA_PROFILE)")
	cmd.Flags().BoolVar(&opts.Prune, "prune", false, "also undeploy packages the profile leaves out")
	cmd.Flags().BoolVar(&opts.Adopt, "adopt", false,
		"take ownership of a copied destination whose contents already match")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the result as JSON")
	return cmd
}

// itemsResult は list --json の出力形。
type itemsResult struct {
	Items []app.Item `json:"items"`
}

func newListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "Show declared packages and their current state",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, _, err := openApp()
			if err != nil {
				return err
			}
			items, err := a.List(context.Background())
			if err != nil {
				return err
			}
			if asJSON {
				if items == nil {
					items = []app.Item{}
				}
				return encodeJSON(itemsResult{Items: items})
			}
			if len(items) == 0 {
				fmt.Println("no packages declared yet; add one with 'kata add'")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tTYPE\tSTATUS\tPROFILES\tSOURCE\tDEST")
			for _, it := range items {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					it.Name, it.Type, it.Status, profileLabel(it), sourceLabel(it), short(it.Dest))
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the packages as JSON")
	return cmd
}

func newStatusCmd() *cobra.Command {
	var quiet, asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Report whether the deployed state matches the manifest",
		Long: "Report whether the deployed state matches the manifest.\n\n" +
			"Only packages that need attention are listed. Exits with 1 when anything is out\n" +
			"of sync, so it works as a CI check. Use 'kata list' to see every declared package.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, _, err := openApp()
			if err != nil {
				return err
			}
			sum, err := a.StatusSummary(context.Background())
			if err != nil {
				return err
			}
			switch {
			case asJSON:
				if err := encodeJSON(sum); err != nil {
					return err
				}
			case !quiet:
				printStatus(sum)
			}
			if !sum.InSync() {
				// 出力は済んでいるので、Error: 行を足さずに終了コードだけ変える。
				return &exitError{code: 1}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "print nothing; report the result through the exit code")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the summary as JSON")
	return cmd
}

// printStatus はズレているものだけを並べ、末尾に件数をまとめる。
func printStatus(sum *app.StatusSummary) {
	if sum.Total == 0 {
		fmt.Println("no packages declared yet; add one with 'kata add'")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, it := range sum.Drifted {
		fmt.Fprintf(w, "%s %s\t%s\t%s\t%s\n",
			statusMarker(it.Status), it.Name, it.Type, it.Status, short(it.Dest))
	}
	_ = w.Flush()

	var parts []string
	for _, s := range app.AllStatuses {
		if n := sum.Counts[s]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, s))
		}
	}
	fmt.Println(strings.Join(parts, ", "))
	if sum.InSync() {
		fmt.Println("in sync")
	} else {
		fmt.Println("out of sync: run 'kata sync'")
	}
}

func statusMarker(s app.Status) string {
	switch s {
	case app.StatusBroken:
		return "!"
	case app.StatusMissing:
		return "+"
	case app.StatusOrphan:
		return "-"
	default:
		return "~"
	}
}

func newImportCmd() *cobra.Command {
	var opts app.ImportOptions
	var types string
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "import [name...]",
		Short: "Adopt existing skills and commands into the manifest",
		Long: "Scan the deployment target for entries kata does not manage, copy them into\n" +
			"local/, and declare each one in the manifest.\n\n" +
			"By default nothing in the deployment target is touched: the originals stay\n" +
			"exactly where they are. Pass --adopt to move each original into the backup\n" +
			"directory and replace it with a link to the copy under local/.",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, _, err := openApp()
			if err != nil {
				return err
			}
			opts.Names = args
			if types != "" {
				opts.Types = strings.Split(types, ",")
			}
			rep, err := a.Import(context.Background(), opts)
			if asJSON {
				if rep != nil && rep.Items == nil {
					rep.Items = []app.ImportItem{}
				}
				if jerr := encodeJSON(rep); jerr != nil {
					return jerr
				}
				return err
			}
			printImport(rep)
			return err
		},
	}
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "show what would be imported without writing anything")
	cmd.Flags().BoolVar(&opts.Adopt, "adopt", false, "move the originals aside and link to the copies under local/")
	cmd.Flags().StringVar(&types, "type", "", "only import these types (comma separated: skill,command,agent)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the result as JSON")
	return cmd
}

// printImport は取り込みの結果を人が読める形で出力する。
func printImport(rep *app.ImportReport) {
	if rep == nil {
		return
	}
	if rep.DryRun {
		fmt.Println("dry run: nothing was changed")
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, it := range rep.Items {
		if it.Action == app.ImportImported {
			fmt.Fprintf(w, "+ %s\t%s\t%s\t-> %s\n", it.Name, it.Type, short(it.Src), it.Local)
		}
	}
	_ = w.Flush()

	// 取り込まなかったものは理由を必ず見せる。黙って飛ばすと、
	// なぜ取り込まれないのかを利用者が調べようがない。
	for _, it := range rep.Items {
		for _, n := range it.Notes {
			fmt.Printf("  note: %s: %s\n", it.Name, n)
		}
		switch it.Action {
		case app.ImportSkipped:
			fmt.Printf("  skip %s: %s\n", it.Name, it.Reason)
		case app.ImportFailed:
			fmt.Fprintf(os.Stderr, "  error: %s: %v\n", it.Name, it.Err)
		}
	}

	counts := rep.Counts()
	verb := "imported"
	if rep.DryRun {
		verb = "to import"
	}
	fmt.Printf("%d %s, %d skipped", counts[app.ImportImported], verb, counts[app.ImportSkipped])
	if n := counts[app.ImportFailed]; n > 0 {
		fmt.Printf(", %d failed", n)
	}
	fmt.Println()

	if len(rep.Orphans) > 0 {
		fmt.Println("these copies were left behind without a declaration; remove them if you do not want them:")
		for _, p := range rep.Orphans {
			fmt.Println("  " + short(p))
		}
	}
	if !rep.DryRun && !rep.Adopt && counts[app.ImportImported] > 0 {
		fmt.Println("the originals are still in place; run 'kata sync --force' to replace them with links")
		fmt.Println("(each original is moved into the backup directory first, never deleted)")
	}
}

func newUpdateCmd() *cobra.Command {
	var opts app.UpdateOptions
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "update [name...]",
		Short: "Re-resolve floating refs and move the lock forward",
		Long: "Resolve each package's ref again and record the result in the lock file.\n\n" +
			"This is the only command besides 'add' that moves the lock: 'kata sync' always\n" +
			"deploys what the lock already pins. Resolving a ref requires fetching, so this\n" +
			"reaches the network even with --dry-run.",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, _, err := openApp()
			if err != nil {
				return err
			}
			opts.Names = args
			rep, err := a.Update(context.Background(), opts)
			if asJSON {
				if rep != nil {
					if rep.Changes == nil {
						rep.Changes = []app.UpdateChange{}
					}
					if rep.Sync != nil && rep.Sync.Changes == nil {
						rep.Sync.Changes = []app.Change{}
					}
				}
				if jerr := encodeJSON(rep); jerr != nil {
					return jerr
				}
				return err
			}
			printUpdate(rep)
			return err
		},
	}
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "show which commits would move without writing the lock")
	cmd.Flags().BoolVar(&opts.NoSync, "no-sync", false, "update the lock but leave the deployment as it is")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the result as JSON")
	return cmd
}

// printUpdate は更新の結果を人が読める形で出力する。
func printUpdate(rep *app.UpdateReport) {
	if rep == nil {
		return
	}
	if rep.DryRun {
		fmt.Println("dry run: the lock was not written")
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, c := range rep.Changes {
		switch {
		case c.Action == app.ActionUpdate:
			fmt.Fprintf(w, "~ %s\t%s\t%s -> %s\t%s\n",
				c.Name, c.Type, shortCommit(c.From), shortCommit(c.To), refLabel(c.Ref))
		case c.Reason != "":
			fmt.Fprintf(w, "  %s\t%s\tskipped: %s\t\n", c.Name, c.Type, c.Reason)
		case c.Action == app.ActionUnchanged:
			fmt.Fprintf(w, "  %s\t%s\tunchanged\t%s\n", c.Name, c.Type, refLabel(c.Ref))
		}
	}
	_ = w.Flush()

	for _, c := range rep.Changes {
		if c.Err != nil {
			fmt.Fprintf(os.Stderr, "  error: %s: %v\n", c.Name, c.Err)
		}
	}
	counts := rep.Counts()
	fmt.Printf("%d updated, %d unchanged", counts[app.ActionUpdate], counts[app.ActionUnchanged])
	if n := counts[app.ActionFailed]; n > 0 {
		fmt.Printf(", %d failed", n)
	}
	fmt.Println()

	if rep.Sync != nil {
		printSync(rep.Sync)
	}
}

func shortCommit(c string) string {
	if c == "" {
		return "(none)"
	}
	return c[:min(7, len(c))]
}

func refLabel(ref string) string {
	if ref == "" {
		return "(default branch)"
	}
	return ref
}

func newDoctorCmd() *cobra.Command {
	var strict, asJSON bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check the environment and explain anything that looks wrong",
		Long: "Inspect the manifest, the lock, the deployment target and the cache, and report\n" +
			"anything that needs attention along with how to fix it.\n\n" +
			"Runs entirely offline, and works even when there is no manifest yet.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			// kata.yml が無いこと自体を診断結果として扱いたいので、緩い設定で開く。
			cfg, err := app.DefaultConfigLoose(wd)
			if err != nil {
				return err
			}
			rep, err := app.Diagnose(context.Background(), cfg)
			if err != nil {
				return err
			}
			if asJSON {
				if err := encodeJSON(rep); err != nil {
					return err
				}
			} else {
				printDoctor(rep)
			}
			worst := rep.Worst()
			if worst == app.LevelError || (strict && worst == app.LevelWarn) {
				return &exitError{code: 1}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&strict, "strict", false, "exit with 1 on warnings too")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the report as JSON")
	return cmd
}

// printDoctor は診断結果を人が読める形で出力する。
func printDoctor(rep *app.DoctorReport) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, c := range rep.Checks {
		fmt.Fprintf(w, "%s\t%s\t%s\n", c.Level, c.Name, c.Detail)
		if c.Hint != "" {
			fmt.Fprintf(w, "\t\thint: %s\n", c.Hint)
		}
	}
	_ = w.Flush()

	counts := rep.Counts()
	fmt.Printf("%d ok, %d warning(s), %d error(s)\n",
		counts[app.LevelOK], counts[app.LevelWarn], counts[app.LevelError])
}

func newPruneCmd() *cobra.Command {
	var opts app.PruneOptions
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove cached content that nothing refers to any more",
		Long: "Remove cached fetches and stale deployment records.\n\n" +
			"Nothing is removed unless --apply is given: unlike 'sync --dry-run', the safe\n" +
			"direction here is to do nothing by default.\n\n" +
			"The backup directory is never touched. Those are your own files, moved aside\n" +
			"on your behalf; deleting them is left to you.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, _, err := openApp()
			if err != nil {
				return err
			}
			rep, err := a.Prune(context.Background(), opts)
			if err != nil {
				return err
			}
			if asJSON {
				if rep.Items == nil {
					rep.Items = []app.PruneItem{}
				}
				return encodeJSON(rep)
			}
			printPrune(rep)
			return nil
		},
	}
	cmd.Flags().BoolVar(&opts.Apply, "apply", false, "actually remove the listed items")
	cmd.Flags().BoolVar(&opts.Store, "store", false, "consider cached fetches that nothing refers to")
	cmd.Flags().BoolVar(&opts.Staging, "staging", false, "consider leftovers from interrupted fetches")
	cmd.Flags().BoolVar(&opts.State, "state", false, "consider deployment records whose destination is gone")
	cmd.Flags().DurationVar(&opts.OlderThan, "older-than", 0, "only consider items older than this (e.g. 720h)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the result as JSON")
	return cmd
}

// printPrune は掃除の結果を人が読める形で出力する。
func printPrune(rep *app.PruneReport) {
	if !rep.Applied {
		fmt.Println("dry run: nothing was removed (pass --apply to remove)")
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, it := range rep.Items {
		size := ""
		if it.Bytes > 0 {
			size = humanBytes(it.Bytes)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", it.Kind, short(it.Path), it.Reason, size)
	}
	_ = w.Flush()

	for _, it := range rep.Items {
		if it.Err != nil {
			fmt.Fprintf(os.Stderr, "  error: %s: %v\n", short(it.Path), it.Err)
		}
	}

	verb := "would be freed"
	if rep.Applied {
		verb = "freed"
	}
	fmt.Printf("%d item(s), %s %s\n", len(rep.Items), humanBytes(rep.Bytes), verb)

	if rep.BackupCount > 0 {
		fmt.Printf("note: %s holds %d snapshot(s) (%s) that kata moved aside for you.\n",
			short(rep.BackupDir), rep.BackupCount, humanBytes(rep.BackupBytes))
		fmt.Println("      kata never removes them; delete them yourself when you no longer need them.")
	}
}

// humanBytes はバイト数を読みやすい単位にする。
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

func newRemoveCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "remove <name>",
		Aliases: []string{"rm"},
		Short:   "Remove a package from the manifest and undeploy it",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, _, err := openApp()
			if err != nil {
				return err
			}
			res, err := a.Remove(context.Background(), args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return encodeJSON(res)
			}
			if res.Unlinked {
				fmt.Printf("removed %s (%s)\n", res.Name, short(res.Dest))
			} else {
				fmt.Printf("removed %s from the manifest\n", res.Name)
			}
			if res.Warning != "" {
				fmt.Printf("  note: %s\n", res.Warning)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the result as JSON")
	return cmd
}

// printSync は sync の結果を人が読める形で出力する。
func printSync(rep *app.SyncReport) {
	if rep == nil {
		return
	}
	if rep.DryRun {
		fmt.Println("dry run: nothing was changed")
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, c := range rep.Changes {
		if c.Action == app.ActionUnchanged {
			continue
		}
		fmt.Fprintf(w, "%s %s\t%s\t%s\n", marker(c.Action), c.Name, c.Type, short(c.Dest))
	}
	_ = w.Flush()

	for _, c := range rep.Changes {
		if c.Warning != "" {
			fmt.Printf("  note: %s: %s\n", c.Name, c.Warning)
		}
		if c.Err != nil {
			fmt.Fprintf(os.Stderr, "  error: %s: %v\n", c.Name, c.Err)
		}
	}

	counts := rep.Counts()
	fmt.Printf("%d created, %d updated, %d removed, %d unchanged",
		counts[app.ActionCreate], counts[app.ActionUpdate],
		counts[app.ActionRemove], counts[app.ActionUnchanged])
	if n := counts[app.ActionFailed]; n > 0 {
		fmt.Printf(", %d failed", n)
	}
	fmt.Println()
}

func marker(a app.Action) string {
	switch a {
	case app.ActionCreate:
		return "+"
	case app.ActionUpdate:
		return "~"
	case app.ActionRemove:
		return "-"
	case app.ActionFailed:
		return "!"
	default:
		return " "
	}
}

// profileLabel は所属プロファイルを 1 列に畳む。
// 未指定は「どの profile でも選ばれる」ことを示すため - ではなく all と書く。
func profileLabel(it app.Item) string {
	if len(it.Profiles) == 0 {
		return "all"
	}
	return strings.Join(it.Profiles, ",")
}

func sourceLabel(it app.Item) string {
	s := it.Source
	if it.Commit != "" {
		s += "@" + it.Commit[:min(7, len(it.Commit))]
	} else if it.Ref != "" {
		s += "@" + it.Ref
	}
	return s
}

// short はホームディレクトリを ~ に畳んで読みやすくする。
func short(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || p == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}
