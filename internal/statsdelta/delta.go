package statsdelta

import "errors"

// OutcomeKind 是相邻两次计数的规范化结果：
//   - BASELINE：首次见到 all-time 计数，只建立基线，不产生任何增量
//     （设计 §19.1 统计口径红线）。
//   - DELTA：正常递增，产生 (cur-prev) 增量。
//   - RESET：qB 重启或计数回退；delta 为 0，不产生负流量，并以当前计数
//     重建基线。
type OutcomeKind string

const (
	KindBaseline OutcomeKind = "BASELINE"
	KindDelta    OutcomeKind = "DELTA"
	KindReset    OutcomeKind = "RESET"
)

type Counter struct {
	Downloaded int64
	Uploaded   int64
}

type Outcome struct {
	Kind  OutcomeKind
	Delta Counter
	Next  Counter
}

// Advance 计算上一次计数到当前计数的规范化结果；prev 为零值表示尚无基线。
// 负数防护：任一字段回退即整体 RESET，绝不输出负增量。
func Advance(prev Counter, hasPrev bool, current Counter) (Outcome, error) {
	if current.Downloaded < 0 || current.Uploaded < 0 {
		return Outcome{}, errors.New("current counter is negative")
	}
	if !hasPrev {
		return Outcome{Kind: KindBaseline, Next: current}, nil
	}
	if prev.Downloaded < 0 || prev.Uploaded < 0 {
		return Outcome{}, errors.New("previous counter is negative")
	}
	if current.Downloaded < prev.Downloaded || current.Uploaded < prev.Uploaded {
		return Outcome{Kind: KindReset, Next: current}, nil
	}
	return Outcome{
		Kind:  KindDelta,
		Delta: Counter{Downloaded: current.Downloaded - prev.Downloaded, Uploaded: current.Uploaded - prev.Uploaded},
		Next:  current,
	}, nil
}
