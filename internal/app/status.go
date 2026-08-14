package app

import "context"

// StatusSummary は宣言と実配置のズレの要約。
type StatusSummary struct {
	// Counts は状態ごとの件数。配置できているものも含む全件。
	Counts map[Status]int `json:"counts"`
	// Drifted は宣言どおりに配置できていない項目。何を直せばよいかを示す。
	Drifted []Item `json:"drifted"`
	// Total は突き合わせた件数。
	Total int `json:"total"`
}

// InSync は全件が宣言どおりに配置されているかを返す。
// CI で使う終了コードはこの値だけで決まる。
func (s *StatusSummary) InSync() bool { return len(s.Drifted) == 0 }

// StatusSummary は宣言と実配置を突き合わせて要約を返す。
//
// 状態の判定そのものは List に任せ、ここでは集計しかしない。
// list と status が食い違って報告することがないよう、判定を 1 箇所に保つため。
func (a *App) StatusSummary(ctx context.Context) (*StatusSummary, error) {
	items, err := a.List(ctx)
	if err != nil {
		return nil, err
	}
	sum := &StatusSummary{Counts: map[Status]int{}, Total: len(items)}
	for _, it := range items {
		sum.Counts[it.Status]++
		if !it.Status.Deployed() {
			sum.Drifted = append(sum.Drifted, it)
		}
	}
	return sum, nil
}
