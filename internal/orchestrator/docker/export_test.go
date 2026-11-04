package docker

// PullWaiters returns how many spawns are blocked on the in-flight
// pull-if-missing of img (0 when none is in flight).
func (c *Client) PullWaiters(img string) int {
	c.pullMu.Lock()
	defer c.pullMu.Unlock()
	if p, ok := c.pulls[img]; ok {
		return p.waiters
	}
	return 0
}
