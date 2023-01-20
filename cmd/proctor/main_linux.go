package main

import (
	"github.com/criyle/go-sandbox/container"
	"github.com/samber/lo"
)

func init() {
	lo.Must0(container.Init(), "failed to launch container runtime")
}
