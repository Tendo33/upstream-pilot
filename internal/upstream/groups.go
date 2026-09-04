package upstream

import (
	"context"
	"errors"
	"net/http"
	"strconv"
)

type GroupUpdate struct {
	RateMultiplier *float64 `json:"rate_multiplier,omitempty"`
}

func (c *Sub2Client) UpdateGroup(ctx context.Context, groupID int64, update GroupUpdate) (Sub2Group, error) {
	if groupID <= 0 {
		return Sub2Group{}, errors.New("Sub2API group ID must be positive")
	}
	if update.RateMultiplier == nil {
		return Sub2Group{}, errors.New("Sub2API group update is empty")
	}
	if !finite(*update.RateMultiplier) || *update.RateMultiplier <= 0 || *update.RateMultiplier > 100000 {
		return Sub2Group{}, errors.New("Sub2API group rate multiplier must be finite and between 0 and 100000")
	}
	path := "/groups/" + strconv.FormatInt(groupID, 10)
	raw, err := c.request(ctx, http.MethodPut, path, update, "application/json")
	if err != nil {
		return Sub2Group{}, err
	}
	group, err := decodeObject[Sub2Group](raw)
	if err != nil {
		return Sub2Group{}, err
	}
	if group.ID != groupID {
		return Sub2Group{}, errors.New("Sub2API returned a mismatched group ID")
	}
	if group.RateMultiplier == nil || !finite(*group.RateMultiplier) || *group.RateMultiplier <= 0 {
		return Sub2Group{}, errors.New("Sub2API returned an invalid group rate multiplier")
	}
	return group, nil
}
