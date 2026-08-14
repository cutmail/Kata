# kata

AIエージェント向けのスキルパッケージマネージャ。

[English](README.md)

`kata.yml` と `kata.lock`、それに自作スキルの実体（`local/`）だけを git 管理しておけば、
別のマシンで `kata sync` を 1 回叩くだけで同じ環境が再現する。

## できること

- **skill / command / agent** を `~/.claude/` 配下、またはプロジェクトの `.claude/` へ配置
- 取得元は **git リポジトリ**（サブディレクトリ指定可）、**書庫の URL**（tar.gz / tgz / zip）、
  **リポジトリ同梱のローカル実体**
- 配置戦略は **symlink / コピー / 自動**（symlink が使えない環境ではコピーに切り替わる）
- **kata.lock** でコミット（url では内容ダイジェスト）を固定し、別マシンでも同じ内容を復元
- **宣言との差分を収束**：宣言から消したものは配置も撤去される（冪等）
- **profiles** で配置対象を絞り込み
- **既存ファイルを壊さない**：kata が作っていないものは上書きも削除もしない

## インストール

macOS は Homebrew の cask で入る。

```console
$ brew install --cask cutmail/tap/kata
```

各リリースにはビルド済みバイナリ（macOS / Linux / Windows、amd64 と arm64）が添付されている。

```console
$ curl -L https://github.com/cutmail/kata/releases/latest/download/kata_darwin_arm64.tar.gz | tar xz
$ sudo mv kata /usr/local/bin/
```

ソースからビルドする場合。

```
go build -o ~/bin/kata ./cmd/kata
```

### ダウンロードしたものを検証する

リリースの各アーカイブには [GitHub ネイティブの SLSA build
provenance](https://docs.github.com/actions/security-guides/using-artifact-attestations-to-establish-provenance-for-builds)
の attestation が付いている。そのアーカイブが、このリポジトリのリリースワークフローで、
タグの付いたコミットからビルドされたことを証明するもの。[GitHub CLI](https://cli.github.com/) で確認する。

```console
$ gh attestation verify kata_darwin_arm64.tar.gz -R cutmail/kata
```

`checksums.txt` は成果物と同じ GitHub Releases に置かれるため、
ダウンロードが壊れていないことしか保証できない。リリースを差し替えられる攻撃者は
checksums.txt も差し替えられる。差し替えを検出できるのは attestation のほうなので、こちらを使う。

### macOS の Gatekeeper について

kata は Apple の署名も notarization も受けていない。
macOS はインターネットから取得したファイルを隔離（quarantine）するため、
初回起動時に「開発元を確認できない」といった警告で実行がブロックされることがある。
これは Homebrew の cask で入れた場合も同じで、**kata は隔離属性を勝手に剥がさない**。

パッケージが黙って隔離属性を剥がすということは、
「未署名のコードがこれから動く」という唯一の警告を、利用者に知らせないまま消すということ。
しかもそれは `brew upgrade` のたびに、全利用者に対して、以後ずっと効き続ける。
未署名のバイナリを信用するかどうかは、出所を検証したうえで利用者自身が決めること。

まず上記の手順で検証し、そのうえで次のどちらかを行う。

- Finder でバイナリを右クリックして「開く」を選び、確認する。以後は記憶される。
- または、自分で隔離属性を外す。

  ```console
  $ xattr -d com.apple.quarantine /usr/local/bin/kata
  ```

  Homebrew で入れた場合のパスは `$(brew --prefix)/bin/kata`。

## クイックスタート

```console
$ mkdir my-agent-config && cd my-agent-config
$ git init && kata init
created kata.yml

# 公開リポジトリのスキルを取り込む
$ kata add anthropics/skills --path skills/pdf --ref main
added pdf (skill) to kata.yml
+ pdf  skill  ~/.claude/skills/pdf

# 自作スキルは local/ に置いて登録する
$ mkdir -p local/skills/my-review && vim local/skills/my-review/SKILL.md
$ kata add ./local/skills/my-review
+ my-review  skill  ~/.claude/skills/my-review

$ kata list
NAME       TYPE   STATUS  SOURCE                                            DEST
pdf        skill  linked  git+https://github.com/anthropics/skills@f17010c  ~/.claude/skills/pdf
my-review  skill  linked  local:./local/skills/my-review                    ~/.claude/skills/my-review

$ git add -A && git commit -m "setup" && git push
```

別のマシンでは、clone して `kata sync` するだけ。

```console
$ git clone <repo> && cd <repo>
$ kata sync
+ my-review  skill  ~/.claude/skills/my-review
+ pdf        skill  ~/.claude/skills/pdf
2 created, 0 updated, 0 removed, 0 unchanged
```

## コマンド

| コマンド | 説明 |
|---|---|
| `kata init` | `kata.yml` と `local/` を作る |
| `kata add <source>` | パッケージを追記して配置する |
| `kata sync` | 宣言どおりの状態に収束させる（冪等） |
| `kata list` | 宣言と実配置を突き合わせて全件表示する |
| `kata status` | ズレているものだけを表示する。ズレがあれば終了コード 1 |
| `kata import` | 既存の `~/.claude` を走査して取り込む |
| `kata update [name...]` | 可変 ref を解決し直して lock を進める |
| `kata doctor` | 環境を診断し、おかしい点と直し方を示す |
| `kata prune` | 参照されなくなったキャッシュを掃除する |
| `kata remove <name>` | 宣言から外して配置も撤去する |

主なフラグ：

- `kata add` — `--type skill|command|agent` / `--name` / `--path` / `--ref` / `--url` /
  `--scope user|project` / `--strategy link|copy|auto` / `--profile` / `--no-sync`
- `kata sync` — `--dry-run` / `--force` / `--profile` / `--prune` / `--adopt`
- `kata import` — `--dry-run` / `--adopt` / `--type`
- `kata prune` — `--apply`（付けなければ何も消さない）/ `--store` / `--staging` / `--state`

`--type` と `--name` は省略時に推測する（`.md` なら command、ディレクトリなら skill）。
agent も `.md` なので形からは区別できず、`--type agent` の明示が必要。

### 既存の ~/.claude から移行する

```console
$ kata import --dry-run          # 何が取り込まれるか見る
$ kata import                    # local/ へ複製して kata.yml に追記（配置先は無傷）
$ kata import --adopt            # さらに元を退避して symlink に置き換える
```

既定では配置先に一切触れない。`--adopt` を付けたときだけ、元を
`~/.kata/backups/<timestamp>/` へ退避してから置き換える。削除はしない。

### profiles

`profiles:` を書いていないパッケージは常に選ばれる。`kata sync --profile work` は
`work` を含むものだけを配置し、**選外のものは配置も lock もそのまま残す**。
剥がしたいときだけ `--prune` を付ける。`KATA_PROFILE` で既定値を決められる。

## kata.yml

```yaml
version: 1

defaults:
  scope: user
  strategy: link

sources:
  anthropic:
    git: https://github.com/anthropics/skills
    ref: main

packages:
  # 共有ソースからサブパスを切り出す
  - name: pdf
    type: skill
    from: anthropic
    path: skills/pdf

  # タグで固定する
  - name: mcp-builder
    type: skill
    git: https://github.com/anthropics/skills
    ref: v1.2.0
    path: skills/mcp-builder

  # 書庫を HTTPS で取得する。内容ダイジェストがピンになる
  - name: toolkit
    type: skill
    url: https://example.com/toolkit-1.4.0.tar.gz
    sha256: 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
    path: toolkit-1.4.0/skills/toolkit

  # 自作スキル（実体もこのリポジトリに入る）
  - name: my-review
    type: skill
    local: ./local/skills/my-review

  # サブエージェント定義
  - name: reviewer
    type: agent
    local: ./local/agents/reviewer.md

  # profile を指定したときだけ配置する
  - name: work-notes
    type: skill
    local: ./local/skills/work-notes
    profiles: [work]

  # リポジトリの .claude へ実体で配置し、チームで共有する
  - name: house-style
    type: skill
    local: ./local/skills/house-style
    scope: project
    strategy: copy
```

`kata.lock` には解決済みのコミット（url では内容ダイジェスト）が記録される。
`sync` はロックを正として動くので、`ref: main` のような可変参照でも、別マシンでは
同じコミットが復元される。

ロックが正であるため、`ref:` や `git:` を書き換えても `sync` は何もしない。仕様どおりだが
気づきにくいので、`kata doctor` がその食い違いを名指しして `kata update` を促す。
`sha256:` がロックと食い違う場合だけは扱いが違い、どちらかを黙って採用せずに停止する。
ダイジェストは「浮動する参照」ではなく「完全性の主張」だから。

### スコープと戦略

`scope: project` は `kata.yml` と同じ階層の `.claude/` へ配置する。`strategy: link` では
リンクが絶対パスを持つため、`.claude/` は `.gitignore` に入れて `kata sync` で再生成する運用に
する。`strategy: copy` なら実体なのでコミットでき、これがチーム共有の形になる。

`strategy: auto` は symlink が使えれば link、使えなければ copy を選ぶ（Windows 向け）。

## 仕組み

```
kata.yml ──┐
           ├─→ 取得（git は ~/.kata/store にキャッシュ、local はリポジトリ内をそのまま）
kata.lock ─┘        │
                    ↓
              ~/.claude/skills/<name>  →  symlink
              ~/.claude/commands/<name>.md
                    │
                    ↓
              ~/.kata/state.json（kata が置いたものの記録）
```

- 配置は一時リンクを作ってから `rename` するため、切り替えは常に原子的
- `~/.kata/store` は純粋なキャッシュ。消しても `kata sync` で戻る
- **kata が置いた記録があるものだけ**を撤去対象にする。手で置いたファイルには触らない

環境変数 `KATA_HOME`（既定 `~/.kata`）と `CLAUDE_CONFIG_DIR`（既定 `~/.claude`）で置き場を変更できる。

## 安全性について

配置先に kata 管理外の実ファイルがあると、`sync` はそのパッケージを失敗として報告し、
**既存ファイルには一切手を触れない**。退避してよい場合だけ `--force` を付けると、
`~/.kata/backups/<timestamp>/` へ移してから配置する。削除はしない。

`strategy: copy` では所有を示す symlink が無いので、配置した内容のダイジェストを記録し、
触る直前に照合する。配置先を編集していれば、その旨を報告して編集を残す
（`--force` でも退避するだけで削除しない）。

`kata prune` は `--apply` を付けなければ何も消さない。消す対象は `~/.kata` 内で自分が
組み立てたパスだけで、同じマシンのどのリポジトリかが参照しているキャッシュは残す。
`~/.kata/backups` には決して触れない。あれは利用者自身のファイルなので、消すかどうかは
利用者が決める。

脆弱性の報告は [SECURITY.md](SECURITY.md) を参照（公開 issue には書かないこと）。

## 開発

```
go test ./...              # ネットワークを使うテストを含む
go test -short ./...       # ネットワーク不要のものだけ
```

## 今後

- MCP サーバ設定のマージ
- 他エージェント（Cursor 等）への配置
- `kata.yml` の JSON Schema 公開
