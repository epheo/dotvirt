package clusterstate

import "k8s.io/client-go/tools/cache"

// countingStore wraps a cache.Indexer and fires on() after any mutation a
// reflector applies (Add/Update/Delete/Replace). It is how the snapshot turns a
// stream of watch deltas into a single coalesced "something moved" signal for the
// hub, without the read methods having to diff anything. Replace fires once for a
// whole relist rather than per item, which is exactly the coarse signal we want,
// and also marks this reflector's initial LIST complete (synced) for WaitForSync.
type countingStore struct {
	cache.Indexer
	on      func()
	synced  func() // called once on the first Replace (initial relist landed)
	didSync bool
}

func (c *countingStore) Add(obj any) error {
	if err := c.Indexer.Add(obj); err != nil {
		return err
	}
	c.on()
	return nil
}

func (c *countingStore) Update(obj any) error {
	if err := c.Indexer.Update(obj); err != nil {
		return err
	}
	c.on()
	return nil
}

func (c *countingStore) Delete(obj any) error {
	if err := c.Indexer.Delete(obj); err != nil {
		return err
	}
	c.on()
	return nil
}

func (c *countingStore) Replace(list []any, rv string) error {
	if err := c.Indexer.Replace(list, rv); err != nil {
		return err
	}
	if !c.didSync {
		c.didSync = true
		if c.synced != nil {
			c.synced()
		}
	}
	c.on()
	return nil
}
