package model

import (
	"container/list"
)

type Graph[T any] struct {
	IDs    []uint32
	Models map[uint32]T
	In     map[uint32]int
	Next   map[uint32][]uint32
}

func (g *Graph[T]) IsDAG() bool {
	var (
		vis      = make(map[uint32]bool, len(g.IDs))
		stk      = make(map[uint32]bool, len(g.IDs))
		isCyclic func(node uint32) bool
	)

	isCyclic = func(node uint32) bool {
		stk[node] = true
		vis[node] = true
		for _, nxt := range g.Next[node] {
			if stk[nxt] {
				return true
			}
			if !vis[nxt] && isCyclic(nxt) {
				return true
			}
		}
		stk[node] = false
		return false
	}

	for _, node := range g.IDs {
		if !vis[node] && isCyclic(node) {
			return false
		}
	}

	return true
}

// Traverse visits every possible node in topological order and returns a map indicating
// whether a node is visited.
func (g *Graph[T]) Traverse(f func(T) bool) (vis map[uint32]bool) {
	queue := list.New()
	vis = make(map[uint32]bool, len(g.IDs))

	for _, id := range g.IDs {
		if g.In[id] == 0 {
			queue.PushBack(id)
		}
	}

	for queue.Len() > 0 {
		top := queue.Remove(queue.Front())
		id := top.(uint32)
		vis[id] = true
		if f(g.Models[id]) {
			// Remove next & push nodes whose in == 0.
			for _, nxt := range g.Next[id] {
				g.In[nxt]--
				if g.In[nxt] == 0 {
					queue.PushBack(nxt)
				}
			}
		}
	}
	return
}

// AddEdge adds an edge a -> b.
func (g *Graph[T]) AddEdge(a, b uint32) {
	if _, ok := g.Next[a]; !ok {
		g.Next[a] = make([]uint32, 0)
	}
	g.Next[a] = append(g.Next[a], b)
	g.In[b]++
}

func (g *Graph[T]) AddNode(id uint32, val T) {
	g.Models[id] = val
}

func NewSubtaskGraph(p *Problem) *Graph[*Subtask] {
	res := Graph[*Subtask]{
		IDs:    make([]uint32, 0, len(p.Subtasks)),
		Models: make(map[uint32]*Subtask, len(p.Subtasks)),
		In:     make(map[uint32]int, len(p.Subtasks)),
		Next:   make(map[uint32][]uint32, len(p.Subtasks)),
	}
	for _, s := range p.Subtasks {
		res.Next[s.Id] = s.Dependencies
		res.Models[s.Id] = s
		res.IDs = append(res.IDs, s.Id)
		for _, nxt := range s.Dependencies {
			res.In[nxt]++
		}
	}
	return &res
}
