package realtimecards

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/sseserver"
)

// Card 是单个 qB 实例的实时瞬时卡片：速度、任务数与 freshness。
// 瞬时数据不持久化、不占持久 event ID；断线后由 REST realtime 端点
// 以当前快照重建（设计 §16.6 stats.realtime 规则）。
type Card struct {
	InstanceID       string
	SyncState        string // 来自 sync_instance_state，STALE/OFFLINE 原样输出
	DownloadRate     int64
	UploadRate       int64
	DownloadingCount int
	SeedingCount     int
	Source           string // 统计来源标记（STAT-01：SYNC_SERVER_STATE/TRANSFER_INFO）
	SampledAt        time.Time
}

// Freshness 报告卡片新鲜度：超过 staleAfter 未更新的卡片标记为
// NOT_FRESH，绝不美化成在线。
func (c Card) Freshness(now time.Time, staleAfter time.Duration) string {
	if c.SampledAt.IsZero() || now.Sub(c.SampledAt) > staleAfter {
		return "NOT_FRESH"
	}
	return "FRESH"
}

// Registry 保存每个实例的最新卡片；更新时向 SSE Hub 广播瞬时事件。
type Registry struct {
	mu         sync.RWMutex
	cards      map[string]Card
	hub        *sseserver.Hub
	StaleAfter time.Duration
}

func NewRegistry(hub *sseserver.Hub, staleAfter time.Duration) *Registry {
	if staleAfter <= 0 {
		staleAfter = 30 * time.Second
	}
	return &Registry{cards: make(map[string]Card), hub: hub, StaleAfter: staleAfter}
}

// Update 记录一张新卡片并广播瞬时事件（不占持久 event ID）。
func (r *Registry) Update(card Card, now time.Time) {
	card.SampledAt = now.UTC()
	r.mu.Lock()
	r.cards[card.InstanceID] = card
	r.mu.Unlock()
	if r.hub != nil {
		payload, err := marshalCard(card)
		if err == nil {
			r.hub.Publish(sseserver.Row{
				EventType:     sseserver.EventRealtime,
				AggregateType: "instance",
				AggregateID:   card.InstanceID,
				SafeDataJSON:  payload,
				OccurredAt:    now.UTC(),
			})
		}
	}
}

// Snapshot 返回全部实例卡片的当前视图：从未上报或已过期的实例保持
// NOT_FRESH 标记，绝不补造数值。
func (r *Registry) Snapshot(now time.Time) []CardView {
	r.mu.RLock()
	defer r.mu.RUnlock()
	views := make([]CardView, 0, len(r.cards))
	for _, card := range r.cards {
		views = append(views, CardView{Card: card, Freshness: card.Freshness(now, r.StaleAfter)})
	}
	return views
}

// CardForInstance 返回单实例视图；未知实例返回 NOT_FRESH 空卡片。
func (r *Registry) CardForInstance(instanceID string, now time.Time) CardView {
	r.mu.RLock()
	card, ok := r.cards[instanceID]
	r.mu.RUnlock()
	if !ok {
		return CardView{Card: Card{InstanceID: instanceID}, Freshness: "NOT_FRESH"}
	}
	return CardView{Card: card, Freshness: card.Freshness(now, r.StaleAfter)}
}

// CardView 是带新鲜度标记的只读视图。
type CardView struct {
	Card
	Freshness string `json:"freshness"`
}

func marshalCard(card Card) (string, error) {
	raw, err := json.Marshal(CardView{Card: card, Freshness: "FRESH"})
	return string(raw), err
}
