package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGraph_Traverse(t *testing.T) {
	nodes := []uint32{1, 2, 3, 4, 5, 6, 7, 8}
	newGraph := func() *Graph[uint32] {
		g := Graph[uint32]{
			IDs:    nodes,
			Models: make(map[uint32]uint32, len(nodes)),
			In:     make(map[uint32]int, len(nodes)),
			Next:   make(map[uint32][]uint32, len(nodes)),
		}
		for _, id := range nodes {
			g.AddNode(id, id)
		}
		return &g
	}

	t.Run("linked-list", func(t *testing.T) {
		val := []int{
			-1, 1, 1, 1, 1, 0, 1, 1, 1,
		}
		g := newGraph()

		g.AddEdge(1, 2)
		g.AddEdge(2, 3)
		g.AddEdge(3, 4)
		g.AddEdge(4, 5)
		g.AddEdge(5, 6)
		g.AddEdge(6, 7)
		g.AddEdge(7, 8)

		vis := g.Traverse(func(u uint32) bool {
			return val[u] == 1
		})

		assert.Equal(t, map[uint32]bool{
			1: true,
			2: true,
			3: true,
			4: true,
			5: true,
			//6: false,
			//7: false,
			//8: false,
		}, vis)
	})

	t.Run("forest", func(t *testing.T) {
		val := []int{
			-1, 1, 1, 1, 1, 0, 0, 1, 1,
		}
		g := newGraph()

		vis := g.Traverse(func(u uint32) bool {
			return val[u] == 1
		})

		assert.Equal(t, map[uint32]bool{
			1: true,
			2: true,
			3: true,
			4: true,
			5: true,
			6: true,
			7: true,
			8: true,
		}, vis)
	})

	t.Run("complex-1", func(t *testing.T) {
		val := []int{
			-1, 1, 1, 1, 0, 0, 1, 1, 0,
		}
		g := newGraph()
		g.AddEdge(1, 3)
		g.AddEdge(1, 2)
		g.AddEdge(5, 7)
		g.AddEdge(5, 6)
		g.AddEdge(3, 4)
		g.AddEdge(3, 5)
		g.AddEdge(6, 2)

		vis := g.Traverse(func(u uint32) bool {
			return val[u] == 1
		})

		assert.Equal(t, map[uint32]bool{
			1: true,
			//2: false,
			3: true,
			4: true,
			5: true,
			//6: false,
			//7: false,
			8: true,
		}, vis)
	})

	t.Run("complex-2", func(t *testing.T) {
		val := []int{
			-1, 1, 1, 1, 0, 1, 1, 0, 1,
		}
		g := newGraph()
		g.AddEdge(1, 2)
		g.AddEdge(2, 3)
		g.AddEdge(3, 4)
		g.AddEdge(4, 5)
		g.AddEdge(5, 6)
		g.AddEdge(2, 6)
		g.AddEdge(7, 8)

		vis := g.Traverse(func(u uint32) bool {
			return val[u] == 1
		})

		assert.Equal(t, map[uint32]bool{
			1: true,
			2: true,
			3: true,
			4: true,
			//5: false,
			//6: false,
			7: true,
			//8: false,
		}, vis)
	})

	t.Run("complex-3", func(t *testing.T) {
		val := []int{
			-1, 1, 1, 1, 1, 1, 0, 1, 1,
		}
		g := newGraph()
		g.AddEdge(1, 4)
		g.AddEdge(4, 6)
		g.AddEdge(3, 2)
		g.AddEdge(2, 4)
		g.AddEdge(4, 5)
		g.AddEdge(6, 7)
		g.AddEdge(7, 5)
		g.AddEdge(5, 8)

		vis := g.Traverse(func(u uint32) bool {
			return val[u] == 1
		})

		assert.Equal(t, map[uint32]bool{
			1: true,
			2: true,
			3: true,
			4: true,
			//5: false,
			6: true,
			//7: false,
			//8: false,
		}, vis)
	})
}

func TestGraph_IsDAG(t *testing.T) {
	nodes := []uint32{1, 2, 3, 4, 5, 6, 7, 8}
	newGraph := func() *Graph[uint32] {
		return &Graph[uint32]{
			IDs:    nodes,
			Models: make(map[uint32]uint32, len(nodes)),
			In:     make(map[uint32]int, len(nodes)),
			Next:   make(map[uint32][]uint32, len(nodes)),
		}
	}
	t.Run("tree", func(t *testing.T) {
		g := newGraph()
		g.AddEdge(1, 2)
		g.AddEdge(1, 3)
		g.AddEdge(1, 4)
		g.AddEdge(4, 5)
		g.AddEdge(6, 8)
		g.AddEdge(6, 7)
		g.AddEdge(5, 6)
		assert.True(t, g.IsDAG())
	})

	t.Run("forest", func(t *testing.T) {
		g := newGraph()
		g.AddEdge(1, 4)
		g.AddEdge(2, 3)
		g.AddEdge(4, 5)
		g.AddEdge(6, 8)
		g.AddEdge(6, 7)
		g.AddEdge(5, 6)
		assert.True(t, g.IsDAG())
	})

	t.Run("no-cycle-1", func(t *testing.T) {
		g := newGraph()
		g.AddEdge(1, 3)
		g.AddEdge(1, 2)
		g.AddEdge(5, 7)
		g.AddEdge(5, 6)
		g.AddEdge(3, 4)
		g.AddEdge(3, 5)
		g.AddEdge(6, 2)
		assert.True(t, g.IsDAG())
	})

	t.Run("no-cycle-2", func(t *testing.T) {
		g := newGraph()
		g.AddEdge(1, 3)
		g.AddEdge(1, 2)
		g.AddEdge(5, 7)
		g.AddEdge(5, 6)
		g.AddEdge(3, 4)
		g.AddEdge(6, 2)
		assert.True(t, g.IsDAG())
	})

	t.Run("no-cycle-3", func(t *testing.T) {
		g := newGraph()
		g.AddEdge(1, 3)
		g.AddEdge(2, 1)
		g.AddEdge(8, 2)
		g.AddEdge(8, 7)
		g.AddEdge(4, 1)
		g.AddEdge(4, 7)
		g.AddEdge(5, 6)
		assert.True(t, g.IsDAG())
	})

	t.Run("contains-cycle-1", func(t *testing.T) {
		g := newGraph()
		g.AddEdge(1, 3)
		g.AddEdge(1, 2)
		g.AddEdge(5, 7)
		g.AddEdge(5, 6)
		g.AddEdge(3, 4)
		g.AddEdge(3, 5)
		g.AddEdge(6, 1)
		assert.False(t, g.IsDAG())
	})

	t.Run("contains-cycle-2", func(t *testing.T) {
		g := newGraph()
		g.AddEdge(1, 2)
		g.AddEdge(2, 3)
		g.AddEdge(3, 4)
		g.AddEdge(4, 5)
		g.AddEdge(5, 6)
		g.AddEdge(6, 2)
		g.AddEdge(7, 8)
		assert.False(t, g.IsDAG())
	})
}
