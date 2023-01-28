package model

import "github.com/criyle/go-judge/envexec"

func ConvertStatusToConclusion(s envexec.Status) Conclusion {
	switch s {
	case envexec.StatusInvalid:
		return Conclusion_Invalid
	case envexec.StatusAccepted:
		return Conclusion_Accepted
	case envexec.StatusWrongAnswer:
		return Conclusion_WrongAnswer
	case envexec.StatusMemoryLimitExceeded:
		return Conclusion_MemoryLimitExceeded
	case envexec.StatusTimeLimitExceeded:
		return Conclusion_MemoryLimitExceeded
	case envexec.StatusOutputLimitExceeded:
		return Conclusion_OutputLimitExceeded
	case envexec.StatusFileError:
		return Conclusion_FileError
	case envexec.StatusNonzeroExitStatus:
		return Conclusion_NonZeroExitStatus
	case envexec.StatusSignalled:
		return Conclusion_Signalled
	case envexec.StatusDangerousSyscall:
		return Conclusion_DangerousSyscall
	case envexec.StatusJudgementFailed:
		return Conclusion_JudgementFailed
	case envexec.StatusInvalidInteraction:
		return Conclusion_InvalidInteraction
	case envexec.StatusInternalError:
		return Conclusion_InternalError
	}
	return 0
}
