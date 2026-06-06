package gatewaycaching

import "sync"

type Response struct{
	// TODO: 
}

type CoalescingCache struct {
	mu       sync.Mutex
	inflight map[string]*sync.WaitGroup
	results  map[string]*Response
}

func (c *CoalescingCache) Get(key string, fetch func() (*Response, error)) (*Response, error) {
	c.mu.Lock()

	// Check if request already inflight
	if wg, exists := c.inflight[key]; exists {
		c.mu.Unlock()

		// Wait for inflight request to complete
		wg.Wait()

		// Return cached result
		return c.results[key], nil
	}

	// First request: mark as inflight
	wg := &sync.WaitGroup{}
	wg.Add(1)
	c.inflight[key] = wg
	c.mu.Unlock()

	// Execute request
	resp, err := fetch()

	// Store result and notify waiters
	c.mu.Lock()
	c.results[key] = resp
	delete(c.inflight, key)
	c.mu.Unlock()

	wg.Done()

	return resp, err
}
