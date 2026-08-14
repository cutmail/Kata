// Package redact は記録や出力から秘密を落とす。
//
// 取得元の URL には資格情報が埋め込まれうる（https://user:token@host/repo）。
// kata.lock はコミットされる前提のファイルなので、そこへ逐語で書くと
// トークンがそのまま公開リポジトリへ入る。表示やエラーも同じ経路になる。
package redact

import "net/url"

// URL は URL に埋め込まれた資格情報を落とす。
//
// 解析できない文字列はそのまま返す。scp 形式（git@host:path）のように
// URL として解析できない取得元もあるが、そこに秘密は入らない。
func URL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = url.User("redacted")
	return u.String()
}
