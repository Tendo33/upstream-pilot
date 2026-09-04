package quality

import (
	"fmt"
	"math"
	"sort"
)

type Strategy string

const (
	PriceFirst Strategy = "price"
	SpeedFirst Strategy = "speed"
	Balanced   Strategy = "balanced"
)

type GroupPolicy struct {
	MinimumSpareSlots   int      `json:"minimum_spare_slots"`
	MinLatencyMS        int      `json:"min_latency_improvement_ms"`
	MinLatencyPercent   float64  `json:"min_latency_improvement_percent"`
	MinPricePercent     float64  `json:"min_price_improvement_percent"`
	HoldSeconds         int      `json:"hold_seconds"`
	MaxChanges          int      `json:"max_changes_per_cycle"`
	RolloutPercent      int      `json:"rollout_percent"`
	EffectWindowSeconds int      `json:"effect_window_seconds"`
	Strategy            Strategy `json:"strategy"`
	PriceWeight         float64  `json:"price_weight"`
	SpeedWeight         float64  `json:"speed_weight"`
	MinimumHealthy      int      `json:"minimum_healthy"`
}

func DefaultGroupPolicy() GroupPolicy {
	return GroupPolicy{Strategy: Balanced, PriceWeight: 1, SpeedWeight: 1, MinimumHealthy: 1, MinLatencyMS: 250, MinLatencyPercent: 10, MinPricePercent: 5, HoldSeconds: 300, MaxChanges: 10, RolloutPercent: 100, EffectWindowSeconds: 600}
}
func (p GroupPolicy) Validate() error {
	if p.MinimumSpareSlots < 0 || p.MinimumSpareSlots > 100000 || p.MinLatencyMS < 0 || p.MinLatencyMS > 600000 || !finite(p.MinLatencyPercent) || p.MinLatencyPercent < 0 || p.MinLatencyPercent > 100 || !finite(p.MinPricePercent) || p.MinPricePercent < 0 || p.MinPricePercent > 100 || p.HoldSeconds < 0 || p.HoldSeconds > 86400 || p.MaxChanges < 1 || p.MaxChanges > 1000 || p.RolloutPercent < 0 || p.RolloutPercent > 100 || p.EffectWindowSeconds < 60 || p.EffectWindowSeconds > 86400 {
		return fmt.Errorf("防抖、灰度范围或效果窗口设置无效")
	}
	if p.Strategy != PriceFirst && p.Strategy != SpeedFirst && p.Strategy != Balanced {
		return fmt.Errorf("未知调度策略")
	}
	if !finite(p.PriceWeight) || !finite(p.SpeedWeight) || p.PriceWeight < 0 || p.SpeedWeight < 0 || p.PriceWeight > 100 || p.SpeedWeight > 100 || p.PriceWeight+p.SpeedWeight == 0 || p.MinimumHealthy < 1 || p.MinimumHealthy > 100 {
		return fmt.Errorf("策略权重或备用数量无效")
	}
	return nil
}

type Candidate struct {
	PriceBasis                       string
	PoolPrices                       map[string]*float64
	PoolPriceBases                   map[string]string
	RiskWorsened                     bool
	ID                               string
	Pools                            []string
	Baseline, Current, Desired, Tier int
	Healthy, Mutable, Available      bool
	Price                            *float64
	Latency                          *int
}
type Assignment struct {
	Priority int      `json:"priority"`
	Rank     int      `json:"rank"`
	Error    string   `json:"error"`
	Pools    []string `json:"pools"`
}

// Plan constructs precedence constraints for each group/model pool and embeds
// their union in account-level integer priorities. Cycles and incompatible fixed
// priorities freeze the affected connected component rather than choosing a
// winner according to iteration order. Equal scores retain a shared rank.
func Plan(input []Candidate, policies map[string]GroupPolicy) map[string]Assignment {
	nodes := append([]Candidate(nil), input...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	byID := map[string]Candidate{}
	pools := map[string][]Candidate{}
	edges := map[string]map[string]bool{}
	adj := map[string]map[string]bool{}
	result := map[string]Assignment{}
	for _, n := range nodes {
		byID[n.ID] = n
		edges[n.ID] = map[string]bool{}
		adj[n.ID] = map[string]bool{}
		result[n.ID] = Assignment{Priority: n.Desired, Pools: n.Pools}
		if !n.Mutable {
			r := result[n.ID]
			r.Priority = n.Current
			result[n.ID] = r
		}
		if n.Available && (n.Healthy || n.Tier > 0) {
			for _, pool := range n.Pools {
				pools[pool] = append(pools[pool], n)
			}
		}
	}
	for pool, members := range pools {
		members = append([]Candidate(nil), members...)
		common := ""
		comparable := true
		for i := range members {
			n := &members[i]
			if n.PoolPrices != nil {
				n.Price = n.PoolPrices[pool]
				n.PriceBasis = n.PoolPriceBases[pool]
			}
			if n.Price == nil || n.PriceBasis == "" {
				comparable = false
			}
			if common == "" {
				common = n.PriceBasis
			} else if common != n.PriceBasis {
				comparable = false
			}
		}
		if !comparable {
			for i := range members {
				members[i].Price = nil
			}
		}

		policy, ok := policies[pool]
		if !ok {
			policy = DefaultGroupPolicy()
		}
		priceMin, priceMax := bounds(members, true)
		speedMin, speedMax := bounds(members, false)
		score := func(n Candidate) float64 {
			cost := normalized(n.Price, priceMin, priceMax)
			var latency *float64
			if n.Latency != nil {
				v := float64(*n.Latency)
				latency = &v
			}
			speed := normalized(latency, speedMin, speedMax)
			switch policy.Strategy {
			case PriceFirst:
				return cost
			case SpeedFirst:
				return speed
			default:
				return policy.PriceWeight*cost + policy.SpeedWeight*speed
			}
		}
		for i, a := range members {
			for _, b := range members[i+1:] {
				cmp := 0
				if a.Healthy != b.Healthy {
					if a.Healthy {
						cmp = -1
					} else {
						cmp = 1
					}
				} else if a.Tier != b.Tier {
					if a.Tier < b.Tier {
						cmp = -1
					} else {
						cmp = 1
					}
				} else {
					diff := score(a) - score(b)
					if math.Abs(diff) > 1e-9 {
						if diff < 0 {
							cmp = -1
						} else {
							cmp = 1
						}
					}
					if cmp != 0 {
						better, worse := a, b
						if cmp > 0 {
							better, worse = b, a
						}
						if !materialImprovement(better, worse, policy) {
							cmp = comparePriority(a.Current, b.Current)
						}
					}
				}
				if cmp == 0 {
					continue
				}
				from, to := a.ID, b.ID
				if cmp > 0 {
					from, to = to, from
				}
				edges[from][to] = true
				adj[from][to] = true
				adj[to][from] = true
			}
		}
	}
	seen := map[string]bool{}
	for _, root := range nodes {
		if seen[root.ID] {
			continue
		}
		component := []string{}
		queue := []string{root.ID}
		seen[root.ID] = true
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			component = append(component, id)
			for other := range adj[id] {
				if !seen[other] {
					seen[other] = true
					queue = append(queue, other)
				}
			}
		}
		if len(component) == 1 && len(adj[root.ID]) == 0 {
			continue
		}
		sort.Strings(component)
		indegree := map[string]int{}
		lower := map[string]int{}
		upper := map[string]int{}
		rank := map[string]int{}
		start := 1000000
		for _, id := range component {
			n := byID[id]
			start = min(start, n.Baseline)
			upper[id] = 1000000
			if n.Mutable && n.Tier > 0 && n.RiskWorsened {
				lower[id] = n.Current
			}
			if !n.Mutable {
				lower[id] = n.Current
				upper[id] = n.Current
			}
			for child := range edges[id] {
				indegree[child]++
			}
		}
		ready := []string{}
		for _, id := range component {
			if indegree[id] == 0 {
				ready = append(ready, id)
			}
		}
		order := []string{}
		for len(ready) > 0 {
			sort.Strings(ready)
			id := ready[0]
			ready = ready[1:]
			order = append(order, id)
			for child := range edges[id] {
				rank[child] = max(rank[child], rank[id]+1)
				lower[child] = max(lower[child], lower[id]+1)
				indegree[child]--
				if indegree[child] == 0 {
					ready = append(ready, child)
				}
			}
		}
		problem := ""
		if len(order) != len(component) {
			problem = "跨组策略存在循环，无法映射为同一账号优先级"
		}
		if problem == "" {
			for i := len(order) - 1; i >= 0; i-- {
				id := order[i]
				for child := range edges[id] {
					upper[id] = min(upper[id], upper[child]-1)
				}
			}
			for _, id := range order {
				if lower[id] > upper[id] || upper[id] < 0 || lower[id] > 1000000 {
					problem = "人工固定优先级与组内顺序冲突，保留当前值"
					break
				}
			}
		}
		if problem != "" {
			for _, id := range component {
				n := byID[id]
				result[id] = Assignment{Priority: n.Current, Error: problem, Pools: n.Pools}
			}
			continue
		}
		for _, id := range order {
			n := byID[id]
			value := n.Current
			if n.Mutable {
				value = max(lower[id], min(upper[id], start+rank[id]*10))
			}
			for child := range edges[id] {
				lower[child] = max(lower[child], value+1)
			}
			result[id] = Assignment{Priority: value, Rank: rank[id], Pools: n.Pools}
		}
	}
	return result
}

func comparePriority(a, b int) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// Compare raw units, not normalized scores: a 1 ms difference must not become
// a full-scale improvement merely because a pool has just two candidates.
func materialImprovement(better, worse Candidate, p GroupPolicy) bool {
	price, speed := false, false
	if better.Price != nil && worse.Price != nil && *better.Price < *worse.Price {
		price = 100*(*worse.Price-*better.Price)/math.Max(*worse.Price, 1e-12) >= p.MinPricePercent
	}
	if better.Latency != nil && worse.Latency != nil && *better.Latency < *worse.Latency {
		delta := *worse.Latency - *better.Latency
		speed = delta >= p.MinLatencyMS && 100*float64(delta)/math.Max(float64(*worse.Latency), 1) >= p.MinLatencyPercent
	}
	switch p.Strategy {
	case PriceFirst:
		return price
	case SpeedFirst:
		return speed
	default:
		return p.PriceWeight > 0 && price || p.SpeedWeight > 0 && speed
	}
}
func bounds(nodes []Candidate, price bool) (float64, float64) {
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, n := range nodes {
		var v *float64
		if price {
			v = n.Price
		} else if n.Latency != nil {
			x := float64(*n.Latency)
			v = &x
		}
		if v != nil && finite(*v) {
			lo = math.Min(lo, *v)
			hi = math.Max(hi, *v)
		}
	}
	return lo, hi
}
func normalized(v *float64, lo, hi float64) float64 {
	if v == nil || !finite(*v) {
		return 2
	}
	if !finite(lo) || hi == lo {
		return 0
	}
	return (*v - lo) / (hi - lo)
}
