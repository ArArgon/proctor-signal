package resource

import (
	"github.com/pkg/errors"

	"proctor-signal/model"
)

func verifyProblem(p *model.Problem) error {
	g := model.NewSubtaskGraph(p)
	if !g.IsDAG() {
		return errors.Errorf("invalid problem : contains cycle, id: %s, ver: %s", p.Id, p.Ver)
	}

	return nil
}
