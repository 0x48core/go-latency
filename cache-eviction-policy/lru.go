package cacheevictionpolicy

type LRUCache struct {
	capacity int
	cache    map[int]*Node
	head     *Node // Most recent
	tail     *Node // Least recent
}

type Node struct {
	key   int
	value int
	prev  *Node
	next  *Node
}

func (lru *LRUCache) Get(key int) int {
	if node, exists := lru.cache[key]; exists {
		lru.moveToHead(node)
		return node.value
	}
	return -1
}

func (lru *LRUCache) Put(key, value int) {
	if node, exists := lru.cache[key]; exists {
		node.value = value
		lru.moveToHead(node)
		return
	}

	newNode := &Node{
		key:   key,
		value: value,
	}

	lru.cache[key] = newNode
	lru.addToHead(newNode)

	if len(lru.cache) > lru.capacity {
		removed := lru.removeTail()
		delete(lru.cache, removed.key)
	}
}

func (lru *LRUCache) moveToHead(node *Node) {
	if node == lru.head {
		return
	}

	// Remove node from current position
	if node.prev != nil {
		node.prev.next = node.next
	}

	if node.next != nil {
		node.next.prev = node.prev
	}

	// Update tail if needed
	if node == lru.tail {
		lru.tail = node.prev
	}

	// Add to head
	node.prev = nil
	node.next = lru.head

	if lru.head != nil {
		lru.head.prev = node
	}

	lru.head = node

	if lru.tail == nil {
		lru.tail = node
	}
}

func (lru *LRUCache) addToHead(node *Node) {
	node.prev = nil
	node.next = lru.head

	if lru.head != nil {
		lru.head.prev = node
	}

	lru.head = node

	// First node
	if lru.tail == nil {
		lru.tail = node
	}
}

func (lru *LRUCache) removeTail() *Node {
	if lru.tail == nil {
		return nil
	}

	removed := lru.tail

	if lru.head == lru.tail {
		lru.head = nil
		lru.tail = nil
		return removed
	}

	lru.tail = removed.prev
	lru.tail.next = nil

	removed.prev = nil
	removed.next = nil

	return removed
}
