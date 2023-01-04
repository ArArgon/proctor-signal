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
		vis   = make(map[uint32]int, len(g.IDs))
		stk   = make([]uint32, len(g.IDs))
		pos   = 0
		color = 0
	)
	for _, id := range g.IDs {
		if vis[id] != 0 {
			continue
		}
		stk[pos] = id
		pos++
		vis[id] = color
		color++

		for pos > 0 {
			top := stk[pos]
			pos--

			for _, nxt := range g.Next[top] {
				if vis[nxt] == color {
					return false
				}
				if vis[nxt] != 0 {
					continue
				}
				vis[nxt] = color
				stk[pos] = nxt
				pos++
			}
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
