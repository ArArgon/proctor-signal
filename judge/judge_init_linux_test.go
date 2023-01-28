package judge

import "github.com/criyle/go-sandbox/container"

func initGoJudge() {
	err := container.Init()
	if err != nil {
		panic("faild to init container: " + err.Error())
	}
}
