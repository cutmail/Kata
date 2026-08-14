# kata

AIエージェント向けのスキルパッケージマネージャー。`kata.yml` で宣言した skill / command / agent を
Claude Code の設定ディレクトリへ配置し、`kata.lock` で取得元を固定して別マシンでも同じ環境を再現する。

## 開発コマンド

```bash
go test ./...          # ネットワークを使うテストを含む
go test -short ./...   # オフラインのみ
go vet ./...
gofmt -l .             # 出力が空であること
go build -o /tmp/kata ./cmd/kata
```

変更を入れたら必ず `go test ./...` と `gofmt -l .` を通すこと。

CI は linux・macOS・**windows** の 3 つで `go test -short` を回し、linux でのみネットワークを
含む全テストを回す。symlink が作れない環境や、パス区切りが `\` になる環境で壊れる変更は
手元では気づけないので、その 2 点に触れるときは特に注意する。

## アーキテクチャ

拡張点は 2 つのインターフェースに集約している。新機能はまずここに収まらないかを検討する。

- `source.Fetcher` — 取得元（git / url / local）。取得元を増やすときはここを実装する
- `target.Resolver` — 配置先（Claude Code）。他エージェント対応はここを実装する

`cmd/kata`（CLI）と `internal/mcpserver`（MCP サーバー）はどちらも `internal/app` を呼ぶだけの
薄い層で、対等な「入口」の関係にある。ビジネスロジックをどちらか一方だけに書かないこと —
`internal/app` に置けば両方から使える。

```
cmd/kata/          CLI（cobra）と `kata mcp` の配線
internal/manifest  kata.yml のパース・正規化・検証
internal/lockfile  kata.lock
internal/state     配置実績の記録（state.json）
internal/store     取得物のキャッシュ
internal/source    取得元と書庫の展開
internal/target    配置先解決
internal/linker    配置と撤去（symlink / コピー）
internal/digest    内容ダイジェスト。所有の判定と取得物の検証に使う
internal/copyfs    ディレクトリ木の複製。import が local/ へ取り込むのに使う
internal/app       オーケストレーション
internal/mcpserver kata の操作を MCP ツールとして公開する（`kata mcp` の実体）
```

## 守るべき不変条件

**1. kata が作っていないものは触らない。**
撤去の対象は `state.json` に記録があるものだけ。配置先に kata 管理外の実体があれば、上書きせず
失敗として報告する。`--force` 指定時のみ `~/.kata/backups/<timestamp>/` へ退避する。削除はしない。
この条件を壊す変更は入れないこと。

**2. sync は冪等。**
コマンドは `sync` に寄せる宣言的モデル。同じ状態で何度実行しても結果が変わらないこと。
マニフェストから消えたものは配置も撤去される。

**3. lock が正。**
`sync` は `kata.lock` のコミット（url 取得元では内容ダイジェスト）を使う。
lock を更新するのは `add` と `update` のみ。`sync` が勝手に上流へ追随してはいけない。
`--profile` で絞ったときも、選外パッケージのピンを消してはいけない。宣言に残っている限り
lock も残す。ここを取り違えると、別マシンでの再現性が恒久的に失われる。

**4. 配置はアトミック。**
一時リンクを作ってから `rename` で差し替える。中途半端な状態を残さない。
copy 戦略ではディレクトリを rename で上書きできないため、新しい内容を作り切ってから
既存を横へどけ、入れ替えたうえで古いものを捨てる。

**5. `~/.kata/store` は消えてよい。**
純粋なキャッシュとして扱い、失われても `sync` で復元できる状態を保つ。
逆に `state.json` は失うと撤去ができなくなるため、書き込みは原子的に行う。

**6. copy 戦略では、証明できるものだけを撤去・上書きする。**
実体には「誰が置いたか」が書かれていないため、`state.json` に記録した内容ダイジェストと
一致することだけが `os.RemoveAll` を正当化する。一致しないもの（利用者が編集した、
記録が無い、検証できない）は警告して残す。`--force` でも削除には格上げしない。
権限の正規化規則は `digest`・`copyfs`・アーカイブ展開の 3 箇所で必ず共有する。
片方だけ規則が違うと、配置直後に「編集された」と誤判定して冪等性が壊れる。

**7. 取得元は信用しない。**
書庫の展開では、書き込む前にパスが展開先の内側に収まることを確かめ、リンクや特殊ファイルの
エントリは拒否する。上限の判定に書庫の自己申告サイズを使わず、実際に読んだバイト数で見る。
ダイジェストの照合は展開の前に行う。

## コーディング規約

- コメントは日本語。「何をするか」ではなく「なぜそうするか」を書く
- エクスポートされた識別子には必ずコメントを付ける
- 利用者の目に触れるもの（CLI 出力・エラーメッセージ）は英語
- 破壊的操作を追加するときは、必ず対応するテストで「触らないこと」を検証する

## テスト方針

- `internal/app` の統合テストは `Config` を明示的に注入して一時ディレクトリで完結させる
- ネットワークを使うテストは `testing.Short()` でスキップできるようにする
- symlink を作るテストは、作れない環境では `t.Skip` する（CI に windows がいる）
- 「壊さないこと」の検証を必ず含める（既存ファイルが無傷であることのアサーション）
- 新しく書いたテストは、意図的に実装を壊して落ちることを一度確かめる。
  通っているが何も守っていないテストを増やさないため

## 対応範囲

対応済み:

- コマンド: `init`/`add`/`sync`/`list`/`status`/`import`/`update`/`doctor`/`prune`/`remove`/`mcp`
- 種別: skill / command / agent
- 取得元: git（サブパス可）/ url（tar.gz・tgz・zip）/ local
- 配置戦略: link / copy / auto
- スコープ: user / project
- profiles によるパッケージの絞り込み
- 全コマンドの `--json` 出力（`internal/app` のレポート型に json タグを付与）
- `kata mcp`: kata 自身の操作（init/add/sync/list/status/import/update/doctor/prune/remove）を
  `internal/mcpserver` 経由で MCP ツールとして stdio 越しに公開する。各ツールは CLI の
  カレントディレクトリに相当する `dir` 引数を明示的に取る（`internal/app.OpenFrom`）

未対応: MCP 設定のマージ（kata が*他の*ツールの MCP 設定を配置管理する機能。上の
`kata mcp` — kata *自身*が MCP サーバーになる機能 — とは別物なので混同しないこと）、
他エージェント向け Resolver（Cursor 等）、`kata.yml` の JSON Schema 公開。

## 設計上の注意

- `import` は既定では配置先に一切触れない。`--adopt` のときだけ退避して置き換える
- `sync` は `kata.yml` の `ref` や取得元の変更を黙って無視する（lock が正）。
  `doctor` がその食い違いを名指しして `update` を促す役割を負っている
- `prune` は `--apply` が無ければ何も消さない。`sync --dry-run` とは逆の非対称
- `~/.kata/backups` は kata が消す手段を持たない。報告するだけ
- url 取得元では、マニフェストの `sha256` と lock のダイジェストが食い違ったら停止する。
  ダイジェストは「浮動する参照」ではなく「完全性の主張」なので、`ref` の変更とは
  意図的に非対称にしてある
- `store` はマシン全体で共有される。孤児かどうかを判断するときは、このマニフェストの lock
  だけでなく全 origin の `state.json` を見る。片方だけ見ると別リポジトリの配置を壊す
